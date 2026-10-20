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
	// includeDeleted=true (Android's download) also returns tombstones, what central
	// deleted, marked deleted (Viewer.IncludeDeleted).
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
	// DHIS2's own form first: stored values use it.
	for _, l := range []string{DHIS2Layout, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02"} {
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
	if q.Get(f.after) == "" && q.Get(f.before) == "" {
		return true, nil // no bound: nothing to parse (this ran for every object of every search)
	}
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
	// Each object's sort values once, not on every comparison (map lookups in the
	// comparator were a seventh of a search's cost; docs/PERFORMANCE.md).
	type row struct {
		o    *Obj
		vals []string
	}
	rows := make([]row, len(objs))
	for i, o := range objs {
		vals := make([]string, len(keys))
		for j, k := range keys {
			switch {
			case k.field == "uid":
				vals[j] = o.UID
			case ValidUID(k.field) && attrKey != nil:
				vals[j] = attrKey(o, k.field)
			default:
				vals[j] = Str(o.Data, k.field)
			}
		}
		rows[i] = row{o, vals}
	}
	slices.SortStableFunc(rows, func(a, b row) int {
		for j, k := range keys {
			if c := strings.Compare(a.vals[j], b.vals[j]); c != 0 {
				if k.desc {
					return -c
				}
				return c
			}
		}
		return 0
	})
	for i, r := range rows {
		objs[i] = r.o
	}
}

// --- rendering ---------------------------------------------------------------

func (ix *Index) visible(v *Viewer, objs []*Obj) []*Obj {
	bm := ix.bitmapOf(v.UserID) // once, not a lock per object
	var out []*Obj
	for _, o := range objs {
		if ix.visibleWith(v, bm, o) {
			out = append(out, o)
		}
	}
	return out
}

// visibleWith is CanRead with the user's bitmap already in hand.
func (ix *Index) visibleWith(v *Viewer, bm bitmap, o *Obj) bool {
	if o.Origin == store.OriginCentral {
		return v.Access != nil && o.OwnerOrgUnit == v.Facility && (!o.Deleted || v.IncludeDeleted) && !o.Stub &&
			bm.has(o.Slot) && accessAllows(v, o)
	}
	return ix.CanRead(v, o)
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
	d := shallow(ix.fullFor(v, o))
	if program != "" {
		// Central lists the program's attributes on the tracked entity when program is
		// given; clients often send them under the enrollment only.
		for _, e := range ix.enrollmentsFor(v, o.UID) {
			if e.Program == program && ix.CanRead(v, e) {
				d["attributes"] = mergeList(d["attributes"], ix.fullFor(v, e)["attributes"], "attribute")
			}
		}
	}
	if _, ok := d["attributes"]; ok {
		d["attributes"] = scopeAttributes(v, o.TET, program, anyList(d["attributes"]))
	}
	enrs := ix.visible(v, ix.enrollmentsFor(v, o.UID))
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
	d := shallow(ix.fullFor(v, o))
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
		evs := ix.visible(v, ix.eventsFor(v, o.UID))
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
	d := shallow(ix.fullFor(v, o))
	if f.Has("relationships") {
		d["relationships"] = ix.renderRels(v, o.UID, f.Sub("relationships"))
	}
	return d
}

func (ix *Index) renderRels(v *Viewer, uid string, _ *Fields) []any {
	rels := ix.visible(v, ix.relationshipsFor(v, uid))
	sortObjs(rels, "createdAt", nil)
	out := []any{}
	for _, r := range rels {
		out = append(out, shallow(ix.fullFor(v, r)))
	}
	return out
}

// GetOne renders a single object for /api/tracker/<collection>/<uid>.
func (ix *Index) GetOne(v *Viewer, collection, uid string, q url.Values) (M, *APIError) {
	if err := checkParams("single", q); err != nil {
		return nil, err
	}
	kind := map[string]string{"trackedEntities": store.KindTrackedEntity, "enrollments": store.KindEnrollment,
		"events": store.KindEvent, "relationships": store.KindRelationship}[collection]
	o := ix.Get(uid)
	v.IncludeDeleted = q.Get("includeDeleted") == "true"
	if o != nil && o.Kind == kind && o.Deleted && !v.IncludeDeleted {
		// Deleted on central: not found, as central answers, for whoever could read it.
		v.IncludeDeleted = true
		readable := ix.CanRead(v, o)
		v.IncludeDeleted = false
		if readable {
			return nil, &APIError{Status: 404, Message: strings.ToUpper(kind[:1]) + kind[1:] + " with id " + uid + " could not be found."}
		}
	}
	if o == nil || o.Kind != kind || !ix.CanRead(v, o) {
		// Same answer for "not here" and "not visible", so nothing leaks (spec 8.2).
		return nil, NeedsConnection(strings.TrimSuffix(kind, "") + " " + uid + " is not available offline")
	}
	fields := ParseFields(cmp.Or(q.Get("fields"), "*"))
	if q.Get("fields") == "" {
		fields = FieldsOrDefault(collection, "")
	}
	ix.prefetch(v, []*Obj{o}, fields)
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
	return shallow(ix.fullFor(v, o)), nil
}
