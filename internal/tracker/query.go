package tracker

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
)

// APIError is an error the hub returns in the target version's error format.
type APIError struct {
	Status  int
	Message string
	Code    string
	Param   string // set for unsupported parameters, so they can be logged (spec 10.1)
}

func (e *APIError) Error() string { return e.Message }

func NeedsConnection(msg string) *APIError {
	return &APIError{Status: 503, Message: "needs connection: " + msg}
}

func unsupported(p string) *APIError {
	return &APIError{Status: 400, Message: "not supported offline: " + p, Param: p}
}

func badRequest(format string, a ...any) *APIError {
	return &APIError{Status: 400, Message: fmt.Sprintf(format, a...)}
}

// Result is a page of rendered objects.
type Result struct {
	Collection string
	Items      []M
	Total      int
	Page       int
	PageSize   int
	Paging     bool
	TotalPages bool
}

// Parameters each collection accepts offline. Anything else returns 400 (spec 10.1).
var allowed = map[string][]string{
	"trackedEntities": {"orgUnits", "orgUnit", "orgUnitMode", "ouMode", "program", "trackedEntityType", "trackedEntity",
		"trackedEntities", "filter", "enrollmentStatus", "programStatus", "updatedAfter", "updatedBefore",
		"enrollmentEnrolledAfter", "enrollmentEnrolledBefore", "order", "page", "pageSize", "totalPages", "paging",
		"skipPaging", "fields", "includeDeleted"},
	"enrollments": {"orgUnits", "orgUnit", "orgUnitMode", "ouMode", "program", "trackedEntity", "enrollments",
		"status", "programStatus", "enrollmentStatus", "updatedAfter", "updatedBefore", "enrolledAfter",
		"enrolledBefore", "order", "page", "pageSize", "totalPages", "paging", "skipPaging", "fields", "includeDeleted"},
	"events": {"orgUnits", "orgUnit", "orgUnitMode", "ouMode", "program", "programStage", "trackedEntity",
		"enrollment", "enrollments", "events", "event", "filter", "status", "enrollmentStatus", "programStatus",
		"updatedAfter", "updatedBefore", "occurredAfter", "occurredBefore", "scheduledAfter", "scheduledBefore",
		"order", "page", "pageSize", "totalPages", "paging", "skipPaging", "fields", "includeDeleted"},
	"relationships": {"trackedEntity", "enrollment", "event", "order", "page", "pageSize", "totalPages", "paging",
		"skipPaging", "fields", "includeDeleted"},
	"single": {"fields", "program", "includeDeleted", "orgUnitMode", "ouMode"},
}

func checkParams(collection string, q url.Values) *APIError {
	for k := range q {
		if !slices.Contains(allowed[collection], k) {
			return unsupported(k)
		}
	}
	// includeDeleted=true (Android's download) is accepted: deletes are refused offline,
	// so the hub never holds a tombstone to return.
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func listParam(q url.Values, names ...string) []string {
	var out []string
	for _, n := range names {
		for _, v := range q[n] {
			out = append(out, splitList(v)...)
		}
	}
	return out
}

// checkOrgUnits enforces "SELECTED only, facility only" (spec 8.2, 10.1).
func checkOrgUnits(v *Viewer, q url.Values, required bool) *APIError {
	mode := cmp.Or(q.Get("orgUnitMode"), q.Get("ouMode"))
	ous := listParam(q, "orgUnits", "orgUnit")
	if (mode == "ACCESSIBLE" || mode == "CAPTURE") && v.AccessibleAsFacility && len(ous) == 0 {
		return nil // offline the hub only holds facility data: search that
	}
	facilityOnly := len(ous) > 0
	for _, ou := range ous {
		facilityOnly = facilityOnly && ou == v.Facility
	}
	if (mode == "DESCENDANTS" || mode == "CHILDREN") && facilityOnly {
		return nil // a facility has no children in the hub's world: same as SELECTED
	}
	if (mode == "ACCESSIBLE" || mode == "CAPTURE") && v.AccessibleAsFacility && facilityOnly {
		return nil
	}
	if mode != "" && mode != "SELECTED" {
		return &APIError{Status: 503, Message: "needs connection: orgUnitMode " + mode + " is not supported offline", Param: "orgUnitMode=" + mode}
	}
	if len(ous) == 0 {
		if required {
			return NeedsConnection("searches without orgUnits are not supported offline")
		}
		return nil
	}
	for _, ou := range ous {
		if ou != v.Facility {
			return NeedsConnection("searches outside the facility are not supported offline")
		}
	}
	return nil
}

func paging(q url.Values) (page, size int, paged, totals bool, err *APIError) {
	page, size, paged = 1, 50, true
	if s := q.Get("page"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 {
			return 0, 0, false, false, badRequest("page must be greater than or equal to 1")
		}
		page = n
	}
	if s := q.Get("pageSize"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 {
			return 0, 0, false, false, badRequest("pageSize must be greater than or equal to 1")
		}
		size = n
	}
	if q.Get("paging") == "false" || q.Get("skipPaging") == "true" {
		paged = false
	}
	totals = q.Get("totalPages") == "true"
	return
}

// parseTime accepts the date forms DHIS2 accepts in query parameters.
func parseTime(s string) (time.Time, bool) {
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

type timeFilter struct {
	field         string
	after, before string
}

func (f timeFilter) match(data M, q url.Values) (bool, *APIError) {
	val, _ := parseTime(Str(data, f.field))
	if s := q.Get(f.after); s != "" {
		t, ok := parseTime(s)
		if !ok {
			return false, badRequest("invalid date for %s: %s", f.after, s)
		}
		if Str(data, f.field) == "" || val.Before(t) {
			return false, nil
		}
	}
	if s := q.Get(f.before); s != "" {
		t, ok := parseTime(s)
		if !ok {
			return false, badRequest("invalid date for %s: %s", f.before, s)
		}
		if Str(data, f.field) == "" || val.After(t) {
			return false, nil
		}
	}
	return true, nil
}

// valueFilter is one "uid:op:value" condition on attributes or data values.
type valueFilter struct{ uid, op, value string }

func parseFilters(q url.Values) ([]valueFilter, *APIError) {
	var out []valueFilter
	for _, raw := range q["filter"] {
		for _, f := range splitUnescaped(raw, ',') {
			parts := splitUnescaped(f, ':')
			if len(parts) == 1 {
				out = append(out, valueFilter{uid: parts[0]}) // "has a value" form is not supported; treat as unsupported below
				return nil, unsupported("filter=" + parts[0])
			}
			for i := 1; i+1 < len(parts); i += 2 {
				op := strings.ToLower(parts[i])
				if op != "eq" && op != "like" && op != "ilike" {
					return nil, unsupported("filter operator " + op)
				}
				out = append(out, valueFilter{uid: parts[0], op: op, value: parts[i+1]})
			}
			if len(parts)%2 == 0 {
				return nil, badRequest("invalid filter: %s", f)
			}
		}
	}
	return out, nil
}

// splitUnescaped splits on sep, honouring DHIS2's "/" escape character.
func splitUnescaped(s string, sep byte) []string {
	var out []string
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '/' && i+1 < len(s) && (s[i+1] == sep || s[i+1] == '/'):
			b.WriteByte(s[i+1])
			i++
		case s[i] == sep:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(s[i])
		}
	}
	return append(out, b.String())
}

func (f valueFilter) match(values map[string]string) bool {
	v, ok := values[f.uid]
	if !ok {
		return false
	}
	switch f.op {
	case "eq":
		return strings.EqualFold(v, f.value)
	default: // like, ilike: case-insensitive contains
		return strings.Contains(strings.ToLower(v), strings.ToLower(f.value))
	}
}

func attrValues(m M, listKey, idKey string) map[string]string {
	out := map[string]string{}
	for _, a := range list(m, listKey) {
		out[Str(a, idKey)] = fmt.Sprint(a["value"])
	}
	return out
}

// sortItems applies "order=field:dir,field:dir" to objects.
func sortObjs(objs []*Obj, order string, attrKey func(*Obj, string) string) {
	type key struct {
		field string
		desc  bool
	}
	var keys []key
	for _, p := range splitList(order) {
		f, dir, _ := strings.Cut(p, ":")
		keys = append(keys, key{f, strings.EqualFold(dir, "desc")})
	}
	if len(keys) == 0 {
		keys = []key{{"createdAt", false}}
	}
	keys = append(keys, key{"uid", false}) // stable tie-breaker
	slices.SortStableFunc(objs, func(a, b *Obj) int {
		for _, k := range keys {
			var va, vb string
			switch {
			case k.field == "uid":
				va, vb = a.UID, b.UID
			case ValidUID(k.field) && attrKey != nil:
				va, vb = attrKey(a, k.field), attrKey(b, k.field)
			default:
				va, vb = Str(a.Data, k.field), Str(b.Data, k.field)
			}
			if c := strings.Compare(va, vb); c != 0 {
				if k.desc {
					return -c
				}
				return c
			}
		}
		return 0
	})
}

// --- rendering ---------------------------------------------------------------

func (ix *Index) visible(v *Viewer, objs []*Obj) []*Obj {
	bm := ix.bitmapOf(v.UserID) // once, not a lock per object
	var out []*Obj
	for _, o := range objs {
		ok := false
		if o.Origin == store.OriginCentral {
			ok = v.Access != nil && o.OwnerOrgUnit == v.Facility && bm.has(o.Slot) && accessAllows(v, o)
		} else {
			ok = ix.CanRead(v, o)
		}
		if ok {
			out = append(out, o)
		}
	}
	return out
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

func filterAttributes(v *Viewer, tet string, attrs any) []any {
	arr, _ := attrs.([]any)
	out := []any{}
	for _, a := range arr {
		m, _ := a.(M)
		if v.Catalog != nil && v.Catalog.ReadableAttribute(v.Access, tet, Str(m, "attribute")) {
			out = append(out, a)
		}
	}
	return out
}

// scopeAttributes keeps the attributes central shows on a tracked entity: those of
// every tracked entity type, plus, with a program, that program's own
// (TrackedEntityAggregate.getAllowedAttributeUids in 2.42). Attributes of the user's
// other programs stay out.
func scopeAttributes(v *Viewer, tet, program string, attrs []any) []any {
	if v.Catalog == nil {
		return []any{}
	}
	keep := map[string]bool{}
	for id := range v.Catalog.TypeAttributes {
		keep[id] = true
	}
	for _, a := range v.Catalog.TETAttributes[tet] {
		keep[a.ID] = true
	}
	if p := v.Catalog.Programs[program]; p != nil && v.Access.Program(program).Read {
		for _, a := range p.Attributes {
			keep[a.ID] = true
		}
	}
	out := []any{}
	for _, a := range attrs {
		if m, _ := a.(M); keep[Str(m, "attribute")] {
			out = append(out, a)
		}
	}
	return out
}

func (ix *Index) RenderTE(v *Viewer, o *Obj, f *Fields, program string) M {
	d := clone(ix.Full(o))
	if program != "" {
		// Central lists the program's attributes on the tracked entity when program is
		// given; clients often send them under the enrollment only.
		for _, e := range ix.EnrollmentsOf(o.UID) {
			if e.Program == program && ix.CanRead(v, e) {
				d["attributes"] = mergeList(d["attributes"], ix.Full(e)["attributes"], "attribute")
			}
		}
	}
	if _, ok := d["attributes"]; ok {
		d["attributes"] = scopeAttributes(v, o.TET, program, anyList(d["attributes"]))
	}
	enrs := ix.visible(v, ix.EnrollmentsOf(o.UID))
	if program != "" {
		// Like central: a program-scoped answer carries that program's enrollments only
		// (the differential oracle found the hub listing all readable ones).
		enrs = slices.DeleteFunc(enrs, func(e *Obj) bool { return e.Program != program })
	}
	sortObjs(enrs, "enrolledAt", nil)
	if _, ok := d["programOwners"]; !ok || o.Origin == store.OriginLocal {
		owners := []any{}
		for _, e := range enrs {
			owners = append(owners, M{"orgUnit": o.OwnerOrgUnit, "trackedEntity": o.UID, "program": e.Program})
		}
		d["programOwners"] = owners
	}
	if !f.Has("programOwners") {
		delete(d, "programOwners")
	}
	if f.Has("enrollments") {
		sub := f.Sub("enrollments")
		out := []any{}
		for _, e := range enrs {
			out = append(out, ix.RenderEnrollment(v, e, sub))
		}
		d["enrollments"] = out
	}
	if f.Has("relationships") {
		d["relationships"] = ix.renderRels(v, o.UID, f.Sub("relationships"))
	}
	return d
}

func (ix *Index) RenderEnrollment(v *Viewer, o *Obj, f *Fields) M {
	d := clone(ix.Full(o))
	if f.Has("attributes") {
		tet := ""
		if te := ix.Get(o.TE); te != nil {
			tet = te.TET
		}
		d["attributes"] = filterAttributes(v, tet, d["attributes"])
	} else {
		delete(d, "attributes")
	}
	if f.Has("events") {
		evs := ix.visible(v, ix.EventsOf(o.UID))
		sortObjs(evs, "occurredAt,scheduledAt", nil)
		sub := f.Sub("events")
		out := []any{}
		for _, e := range evs {
			out = append(out, ix.RenderEvent(v, e, sub))
		}
		d["events"] = out
	}
	if f.Has("relationships") {
		d["relationships"] = ix.renderRels(v, o.UID, f.Sub("relationships"))
	}
	return d
}

func (ix *Index) RenderEvent(v *Viewer, o *Obj, f *Fields) M {
	d := clone(ix.Full(o))
	if f.Has("relationships") {
		d["relationships"] = ix.renderRels(v, o.UID, f.Sub("relationships"))
	}
	return d
}

func (ix *Index) renderRels(v *Viewer, uid string, _ *Fields) []any {
	rels := ix.visible(v, ix.RelationshipsOf(uid))
	sortObjs(rels, "createdAt", nil)
	out := []any{}
	for _, r := range rels {
		out = append(out, clone(ix.Full(r)))
	}
	return out
}

// --- queries -------------------------------------------------------------------

func page(objs []*Obj, pg, size int, paged bool) []*Obj {
	if !paged {
		return objs
	}
	if pg-1 >= (len(objs)+size-1)/size { // compared before multiplying: page and size are unbounded
		return nil
	}
	start := (pg - 1) * size
	return objs[start:min(start+size, len(objs))]
}

func (ix *Index) programCheck(v *Viewer, program string) *APIError {
	if program == "" {
		return nil
	}
	if v.Catalog == nil || v.Catalog.Programs[program] == nil {
		return NeedsConnection("program " + program + " is not cached on this hub")
	}
	if !v.Access.Program(program).Read {
		return &APIError{Status: 403, Message: "User has no access to program: " + program}
	}
	return nil
}

// Search runs a collection query (spec 10.1).
func (ix *Index) Search(v *Viewer, collection string, q url.Values) (*Result, *APIError) {
	if err := checkParams(collection, q); err != nil {
		return nil, err
	}
	pg, size, paged, totals, perr := paging(q)
	if perr != nil {
		return nil, perr
	}
	program := q.Get("program")
	if err := ix.programCheck(v, program); err != nil {
		return nil, err
	}
	fields := FieldsOrDefault(collection, q.Get("fields"))
	var objs []*Obj
	var render func(*Obj) M
	var err *APIError
	switch collection {
	case "trackedEntities":
		objs, err = ix.searchTEs(v, q, program)
		render = func(o *Obj) M { return ix.RenderTE(v, o, fields, program) }
	case "enrollments":
		objs, err = ix.searchEnrollments(v, q, program)
		render = func(o *Obj) M { return ix.RenderEnrollment(v, o, fields) }
	case "events":
		objs, err = ix.searchEvents(v, q, program)
		render = func(o *Obj) M { return ix.RenderEvent(v, o, fields) }
	case "relationships":
		objs, err = ix.searchRelationships(v, q)
		render = func(o *Obj) M { return clone(ix.Full(o)) }
	default:
		return nil, NeedsConnection(collection)
	}
	if err != nil {
		return nil, err
	}
	res := &Result{Collection: collection, Total: len(objs), Page: pg, PageSize: size, Paging: paged, TotalPages: totals,
		Items: []M{}}
	for _, o := range page(objs, pg, size, paged) {
		res.Items = append(res.Items, fields.Project(render(o)).(M))
	}
	return res, nil
}

func (ix *Index) searchTEs(v *Viewer, q url.Values, program string) ([]*Obj, *APIError) {
	if err := checkOrgUnits(v, q, len(listParam(q, "trackedEntities", "trackedEntity")) == 0); err != nil {
		return nil, err
	}
	tet := q.Get("trackedEntityType")
	if program == "" && tet == "" && len(listParam(q, "trackedEntities", "trackedEntity")) == 0 {
		return nil, badRequest("Either `program`, `trackedEntityType` or `trackedEntities` should be specified")
	}
	filters, err := parseFilters(q)
	if err != nil {
		return nil, err
	}
	uids := listParam(q, "trackedEntities", "trackedEntity")
	status := cmp.Or(q.Get("enrollmentStatus"), q.Get("programStatus"))
	if status != "" && program == "" {
		return nil, badRequest("`program` must be defined when `enrollmentStatus` is specified")
	}
	upd := timeFilter{"updatedAt", "updatedAfter", "updatedBefore"}
	enrolled := timeFilter{"enrolledAt", "enrollmentEnrolledAfter", "enrollmentEnrolledBefore"}
	var cands []*Obj
	switch {
	case len(uids) > 0:
		cands = ix.byUIDs(store.KindTrackedEntity, uids)
	case program != "":
		seen := map[string]bool{}
		for _, e := range ix.related("penr:" + program) {
			if !seen[e.TE] {
				seen[e.TE] = true
				if o := ix.Get(e.TE); o != nil {
					cands = append(cands, o)
				}
			}
		}
	case tet != "":
		cands = ix.related("tet:" + tet)
	default:
		cands = ix.All(store.KindTrackedEntity)
	}
	var out []*Obj
	for _, o := range ix.visible(v, cands) {
		if tet != "" && o.TET != tet {
			continue
		}
		if len(uids) > 0 && !slices.Contains(uids, o.UID) {
			continue
		}
		if ok, err := ix.updatedMatch(v, o, upd, q); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		if program != "" {
			found := false
			for _, e := range ix.visible(v, ix.EnrollmentsOf(o.UID)) {
				if e.Program != program || (status != "" && Str(e.Data, "status") != status) {
					continue
				}
				ok, err := enrolled.match(e.Data, q)
				if err != nil {
					return nil, err
				}
				if ok {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if len(filters) > 0 {
			vals := attrValues(M{"attributes": filterAttributes(v, o.TET, o.Data["attributes"])}, "attributes", "attribute")
			match := true
			for _, f := range filters {
				if !f.match(vals) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, o)
	}
	sortObjs(out, q.Get("order"), func(o *Obj, attr string) string {
		return attrValues(o.Data, "attributes", "attribute")[attr]
	})
	return out, nil
}

func (ix *Index) searchEnrollments(v *Viewer, q url.Values, program string) ([]*Obj, *APIError) {
	te := q.Get("trackedEntity")
	uids := listParam(q, "enrollments")
	if err := checkOrgUnits(v, q, te == "" && len(uids) == 0); err != nil {
		return nil, err
	}
	status := cmp.Or(q.Get("status"), q.Get("enrollmentStatus"), q.Get("programStatus"))
	tfs := []timeFilter{{"updatedAt", "updatedAfter", "updatedBefore"}, {"enrolledAt", "enrolledAfter", "enrolledBefore"}}
	var cands []*Obj
	switch {
	case len(uids) > 0:
		cands = ix.byUIDs(store.KindEnrollment, uids)
	case te != "":
		cands = ix.EnrollmentsOf(te)
	case program != "":
		cands = ix.related("penr:" + program)
	default:
		cands = ix.All(store.KindEnrollment)
	}
	var out []*Obj
	for _, o := range ix.visible(v, cands) {
		if (program != "" && o.Program != program) || (te != "" && o.TE != te) ||
			(len(uids) > 0 && !slices.Contains(uids, o.UID)) || (status != "" && Str(o.Data, "status") != status) {
			continue
		}
		ok, err := matchAll(o.Data, q, tfs[1:])
		if ok && err == nil {
			ok, err = ix.updatedMatch(v, o, tfs[0], q)
		}
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, o)
		}
	}
	sortObjs(out, q.Get("order"), nil)
	return out, nil
}

// updatedMatch applies updatedAfter/updatedBefore. Objects written on the hub always
// match: their timestamps come from the hub's clock, while clients compare against a
// watermark from central's clock, and re-sending them is harmless. A tracked entity
// counts as updated when anything under it was (central bumps it too).
func (ix *Index) updatedMatch(v *Viewer, o *Obj, tf timeFilter, q url.Values) (bool, *APIError) {
	if q.Get(tf.after) == "" && q.Get(tf.before) == "" {
		return true, nil
	}
	objs := []*Obj{o}
	if o.Kind == store.KindTrackedEntity {
		objs = append(objs, ix.visible(v, ix.EnrollmentsOf(o.UID))...)
		objs = append(objs, ix.visible(v, ix.EventsOfTE(o.UID))...)
	}
	newest := M{}
	for _, x := range objs {
		if x.Origin == store.OriginLocal {
			return true, nil
		}
		if u := Str(x.Data, "updatedAt"); u > Str(newest, "updatedAt") {
			newest = M{"updatedAt": u}
		}
	}
	return tf.match(newest, q)
}

func matchAll(d M, q url.Values, tfs []timeFilter) (bool, *APIError) {
	for _, tf := range tfs {
		ok, err := tf.match(d, q)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (ix *Index) searchEvents(v *Viewer, q url.Values, program string) ([]*Obj, *APIError) {
	te := q.Get("trackedEntity")
	enrs := listParam(q, "enrollment", "enrollments")
	uids := listParam(q, "events", "event")
	if err := checkOrgUnits(v, q, te == "" && len(enrs) == 0 && len(uids) == 0); err != nil {
		return nil, err
	}
	stage := q.Get("programStage")
	if stage != "" && !v.Access.Stage(stage).Read {
		return nil, &APIError{Status: 403, Code: "E1006", Message: "User has no access to program stage: " + stage}
	}
	status := q.Get("status")
	enrStatus := cmp.Or(q.Get("enrollmentStatus"), q.Get("programStatus"))
	filters, err := parseFilters(q)
	if err != nil {
		return nil, err
	}
	tfs := []timeFilter{{"updatedAt", "updatedAfter", "updatedBefore"}, {"occurredAt", "occurredAfter", "occurredBefore"},
		{"scheduledAt", "scheduledAfter", "scheduledBefore"}}
	var cands []*Obj
	switch {
	case len(uids) > 0:
		cands = ix.byUIDs(store.KindEvent, uids)
	case len(enrs) > 0:
		for _, e := range enrs {
			cands = append(cands, ix.EventsOf(e)...)
		}
	case te != "":
		cands = ix.EventsOfTE(te)
	case program != "":
		cands = ix.related("pev:" + program)
	default:
		cands = ix.All(store.KindEvent)
	}
	var out []*Obj
	for _, o := range ix.visible(v, cands) {
		if (program != "" && o.Program != program) || (stage != "" && o.Stage != stage) || (te != "" && o.TE != te) ||
			(len(enrs) > 0 && !slices.Contains(enrs, o.Enrollment)) || (len(uids) > 0 && !slices.Contains(uids, o.UID)) ||
			(status != "" && Str(o.Data, "status") != status) {
			continue
		}
		if enrStatus != "" {
			if e := ix.Get(o.Enrollment); e == nil || Str(e.Data, "status") != enrStatus {
				continue
			}
		}
		ok, err := matchAll(o.Data, q, tfs[1:])
		if ok && err == nil {
			ok, err = ix.updatedMatch(v, o, tfs[0], q)
		}
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if len(filters) > 0 {
			vals := attrValues(ix.Full(o), "dataValues", "dataElement")
			match := true
			for _, f := range filters {
				if !f.match(vals) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		out = append(out, o)
	}
	sortObjs(out, q.Get("order"), func(o *Obj, de string) string {
		return attrValues(ix.Full(o), "dataValues", "dataElement")[de]
	})
	return out, nil
}

func (ix *Index) searchRelationships(v *Viewer, q url.Values) ([]*Obj, *APIError) {
	var uid string
	n := 0
	for _, k := range []string{"trackedEntity", "enrollment", "event"} {
		if s := q.Get(k); s != "" {
			uid = s
			n++
		}
	}
	if n != 1 {
		return nil, badRequest("Only one parameter of 'trackedEntity', 'enrollment' or 'event' must be specified. Got: %d", n)
	}
	if o := ix.Get(uid); !ix.CanRead(v, o) {
		return nil, NeedsConnection(uid + " is not available offline")
	}
	out := ix.visible(v, ix.RelationshipsOf(uid))
	sortObjs(out, q.Get("order"), nil)
	return out, nil
}

// GetOne renders a single object for /api/tracker/<collection>/<uid>.
func (ix *Index) GetOne(v *Viewer, collection, uid string, q url.Values) (M, *APIError) {
	if err := checkParams("single", q); err != nil {
		return nil, err
	}
	kind := map[string]string{"trackedEntities": store.KindTrackedEntity, "enrollments": store.KindEnrollment,
		"events": store.KindEvent, "relationships": store.KindRelationship}[collection]
	o := ix.Get(uid)
	if o == nil || o.Kind != kind || !ix.CanRead(v, o) {
		// Same answer for "not here" and "not visible", so nothing leaks (spec 8.2).
		return nil, NeedsConnection(strings.TrimSuffix(kind, "") + " " + uid + " is not available offline")
	}
	fields := ParseFields(cmp.Or(q.Get("fields"), "*"))
	if q.Get("fields") == "" {
		fields = FieldsOrDefault(collection, "")
	}
	switch kind {
	case store.KindTrackedEntity:
		if p := q.Get("program"); p != "" {
			if err := ix.programCheck(v, p); err != nil {
				return nil, err
			}
			// Owned by another facility in this program: central decides whether this
			// user may open it (ownership, temporary ownership), so offline it may not.
			if owner := ProgramOwner(o, p); (owner != "" && owner != v.Facility) || ix.ownedElsewhere(o.UID, p, v.Facility) {
				return nil, NeedsConnection("tracked entity " + uid + " is owned by another org unit in program " + p)
			}
		}
		return fields.Project(ix.RenderTE(v, o, fields, q.Get("program"))).(M), nil
	case store.KindEnrollment:
		return fields.Project(ix.RenderEnrollment(v, o, fields)).(M), nil
	case store.KindEvent:
		return fields.Project(ix.RenderEvent(v, o, fields)).(M), nil
	}
	return clone(ix.Full(o)), nil
}
