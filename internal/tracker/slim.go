package tracker

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/dhis2/facility-hub/internal/store"
)

// Objects read from the index are slim: top-level scalar fields (identifiers, status,
// dates), a tracked entity's attribute values and a relationship's ends, which is what
// queries filter and sort on, stored as each row's slim JSON. Rendering and merging read
// the full payload, which is sealed per row (docs/PERFORMANCE.md).

func slimmed(o *Obj) *Obj {
	n := *o
	d := M{}
	for k, v := range o.Data {
		switch v.(type) {
		case string, bool, float64, json.Number, nil:
			d[k] = v
		}
	}
	switch o.Kind {
	case store.KindTrackedEntity:
		var attrs []any
		for _, a := range anyList(o.Data["attributes"]) {
			if m, ok := a.(M); ok {
				attrs = append(attrs, M{"attribute": m["attribute"], "value": m["value"]})
			}
		}
		d["attributes"] = attrs
		if po, ok := o.Data["programOwners"]; ok {
			d["programOwners"] = po
		}
	case store.KindRelationship:
		d["from"], d["to"] = o.Data["from"], o.Data["to"]
	}
	n.Data, n.slim = d, true
	return &n
}

// Full returns the object's full payload: from the store for a slim object. Treat it
// as read-only.
func (ix *Index) Full(o *Obj) M {
	if o == nil || !o.slim {
		if o == nil {
			return nil
		}
		return o.Data
	}
	so, err := ix.store.GetObject(o.UID)
	if err != nil || so == nil {
		slog.Warn("payload not readable; using the slim copy", "uid", o.UID, "err", err)
		return o.Data
	}
	var d M
	if err := json.Unmarshal(so.Payload, &d); err != nil {
		return o.Data
	}
	return d
}

// FullMany returns the full payloads of these objects, by uid, read in one go.
func (ix *Index) FullMany(objs []*Obj) map[string]M {
	out := map[string]M{}
	var uids []string
	for _, o := range objs {
		if o.slim {
			uids = append(uids, o.UID)
		} else {
			out[o.UID] = o.Data
		}
	}
	if len(uids) == 0 || ix.store == nil {
		return out
	}
	raw, err := ix.store.Payloads(uids)
	if err != nil {
		slog.Warn("reading payloads", "err", err)
		return out
	}
	for uid, b := range raw {
		var d M
		if json.Unmarshal(b, &d) == nil {
			out[uid] = d
		}
	}
	return out
}

// ProgramOwner returns the org unit that owns the tracked entity in the program, as
// central last reported it (programOwners), or "" when unknown.
func ProgramOwner(te *Obj, program string) string {
	if te == nil {
		return ""
	}
	for _, x := range anyList(te.Data["programOwners"]) {
		if m, ok := x.(M); ok && Str(m, "program") == program {
			return Str(m, "orgUnit")
		}
	}
	return ""
}

// SetProgramOwner returns a copy of a tracked entity payload with the program's owner
// set.
func SetProgramOwner(d M, te, program, owner string) M {
	n := clone(d)
	var out []any
	for _, x := range anyList(d["programOwners"]) {
		if m, ok := x.(M); ok && Str(m, "program") == program {
			continue
		}
		out = append(out, x)
	}
	n["programOwners"] = append(out, M{"orgUnit": owner, "trackedEntity": te, "program": program})
	return n
}

// WithData returns a copy of the object holding this full payload (for writing back).
func (o *Obj) WithData(d M) *Obj {
	n := *o
	n.Data, n.slim = d, false
	return &n
}

// ownedElsewhere reports whether the hub holds an enrollment of te in program that is
// no longer the facility's (Hub.ApplyTransfer).
func (ix *Index) ownedElsewhere(te, program, facility string) bool {
	for _, e := range ix.EnrollmentsOf(te) {
		if e.Program == program && e.OwnerOrgUnit != facility {
			return true
		}
	}
	return false
}

// fullFor is Full, from the payloads prefetched for this request when there.
func (ix *Index) fullFor(v *Viewer, o *Obj) M {
	if v != nil && o != nil && o.slim {
		if d, ok := v.payloads[o.UID]; ok {
			return d
		}
	}
	return ix.Full(o)
}

// prefetch loads in one go the full payloads that rendering objs with these fields
// will read: each object, and under it the enrollments, events and relationships the
// fields ask for (nil fields: all of them).
func (ix *Index) prefetch(v *Viewer, objs []*Obj, f *Fields) {
	if ix.store == nil || v == nil {
		return
	}
	seen := map[string]bool{}
	var uids []string
	add := func(os []*Obj) {
		for _, o := range os {
			if o != nil && o.slim && !seen[o.UID] {
				if _, ok := v.payloads[o.UID]; !ok {
					seen[o.UID] = true
					uids = append(uids, o.UID)
				}
			}
		}
	}
	has := func(f *Fields, name string) bool { return f == nil || f.Has(name) }
	sub := func(f *Fields, name string) *Fields {
		if f == nil {
			return nil
		}
		return f.Sub(name)
	}
	ix.loadChildren(v, objs, f)
	for _, o := range objs {
		add([]*Obj{o})
		switch o.Kind {
		case store.KindTrackedEntity:
			// Enrollments always: a program-scoped answer merges their attributes.
			enrs := ix.enrollmentsFor(v, o.UID)
			add(enrs)
			if ef := sub(f, "enrollments"); has(f, "enrollments") && has(ef, "events") {
				for _, e := range enrs {
					add(ix.eventsFor(v, e.UID)) // as RenderEnrollment will ask
				}
			}
			if has(f, "relationships") {
				add(ix.relationshipsFor(v, o.UID))
			}
		case store.KindEnrollment:
			if has(f, "events") {
				add(ix.eventsFor(v, o.UID))
			}
			if has(f, "relationships") {
				add(ix.relationshipsFor(v, o.UID))
			}
		case store.KindEvent:
			if has(f, "relationships") {
				add(ix.relationshipsFor(v, o.UID))
			}
		}
	}
	if len(uids) == 0 {
		return
	}
	got := ix.loadPayloads(uids)
	if v.payloads == nil {
		v.payloads = make(map[string]M, len(got))
	}
	for uid, d := range got {
		v.payloads[uid] = d
	}
}

// loadChildren reads the children rendering objs will ask for, for all of them at
// once, into the request's lookups (a page of 50 people asked for each one's
// enrollments and each enrollment's events separately).
func (ix *Index) loadChildren(v *Viewer, objs []*Obj, f *Fields) {
	has := func(f *Fields, name string) bool { return f == nil || f.Has(name) }
	var tes, enrs, rels []string
	for _, o := range objs {
		switch o.Kind {
		case store.KindTrackedEntity:
			tes = append(tes, o.UID)
		case store.KindEnrollment:
			enrs = append(enrs, o.UID)
		}
		if has(f, "relationships") {
			rels = append(rels, o.UID)
		}
	}
	for _, e := range ix.memoChildren(v, "enr:", "te_uid", store.KindEnrollment, tes) {
		enrs = append(enrs, e.UID)
	}
	ix.memoChildren(v, "ev:", "enrollment_uid", store.KindEvent, enrs)
	if len(rels) > 0 {
		ix.memoChildren(v, "rel:", "", store.KindRelationship, rels)
	}
}

// memoChildren loads the children of kind under these parents, by the parent's column
// (relationships: either end), into the request's lookups under prefix+parent, and
// returns them.
func (ix *Index) memoChildren(v *Viewer, prefix, col, kind string, parents []string) []*Obj {
	if v == nil || len(parents) == 0 {
		return nil
	}
	if v.related == nil {
		v.related = map[string][]*Obj{}
	}
	var need []string
	for _, p := range parents {
		if _, ok := v.related[prefix+p]; !ok {
			need = append(need, p)
		}
	}
	groups := map[string][]*Obj{}
	var all []*Obj
	const batch = 400
	for start := 0; start < len(need); start += batch {
		part := need[start:min(start+batch, len(need))]
		args := make([]any, 0, 2*len(part)+1)
		marks := "?" + strings.Repeat(",?", len(part)-1)
		for _, p := range part {
			args = append(args, p)
		}
		var objs []*Obj
		if col == "" {
			args = append(args, args...)
			objs = ix.objects(`SELECT `+store.ObjectCols("")+` FROM objects WHERE rel_from IN (`+marks+`) OR rel_to IN (`+marks+`)`, args...)
		} else {
			objs = ix.objects(`SELECT `+store.ObjectCols("")+` FROM objects WHERE `+col+` IN (`+marks+`) AND kind = ?`, append(args, kind)...)
		}
		for _, o := range objs {
			switch col {
			case "te_uid":
				groups[o.TE] = append(groups[o.TE], o)
			case "enrollment_uid":
				groups[o.Enrollment] = append(groups[o.Enrollment], o)
			default:
				from, to := RelRef(asM(o.Data["from"])).UID, RelRef(asM(o.Data["to"])).UID
				groups[from] = append(groups[from], o)
				if to != from {
					groups[to] = append(groups[to], o)
				}
			}
		}
		all = append(all, objs...)
	}
	for _, p := range need {
		v.related[prefix+p] = groups[p]
	}
	return all
}
