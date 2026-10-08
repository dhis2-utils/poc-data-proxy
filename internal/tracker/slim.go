package tracker

import (
	"encoding/json"
	"log/slog"

	"github.com/dhis2/facility-hub/internal/store"
)

// Objects in a loaded index are held slim: top-level scalar fields (identifiers,
// status, dates), a tracked entity's attribute values and a relationship's ends, which
// is what queries filter and sort on. Rendering and merging read the full payload
// from the store. A decoded event with its data values took about 6 KB in memory
// (docs/PERFORMANCE.md).

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
		slog.Warn("payload not readable; using the index copy", "uid", o.UID, "err", err)
		return o.Data
	}
	var d M
	if err := json.Unmarshal(so.Payload, &d); err != nil {
		return o.Data
	}
	return d
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
	for _, o := range objs {
		add([]*Obj{o})
		switch o.Kind {
		case store.KindTrackedEntity:
			// Enrollments always: a program-scoped answer merges their attributes.
			enrs := ix.EnrollmentsOf(o.UID)
			add(enrs)
			if ef := sub(f, "enrollments"); has(f, "enrollments") && has(ef, "events") {
				add(ix.EventsOfTE(o.UID))
			}
			if has(f, "relationships") {
				add(ix.RelationshipsOf(o.UID))
			}
		case store.KindEnrollment:
			if has(f, "events") {
				add(ix.EventsOf(o.UID))
			}
			if has(f, "relationships") {
				add(ix.RelationshipsOf(o.UID))
			}
		case store.KindEvent:
			if has(f, "relationships") {
				add(ix.RelationshipsOf(o.UID))
			}
		}
	}
	if len(uids) == 0 {
		return
	}
	raw, err := ix.store.Payloads(uids)
	if err != nil {
		slog.Warn("prefetching payloads", "err", err)
		return
	}
	if v.payloads == nil {
		v.payloads = make(map[string]M, len(raw))
	}
	for uid, b := range raw {
		var d M
		if json.Unmarshal(b, &d) == nil {
			v.payloads[uid] = d
		}
	}
}
