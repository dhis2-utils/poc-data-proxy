package tracker

import (
	"cmp"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
)

// Searches read the store's data file (docs/PERFORMANCE.md, "The query index in
// SQLite"). Each is a plan: SQL over the objects table carrying every condition that
// columns can decide exactly, and the checks only Go can make (visibility, dates in the
// payload, readable attributes). When the order asked for is the creation date, SQL
// returns the rows in that order and the search stops once the page is full, so a
// working list costs the same whatever the facility's size. Any other order is applied
// in Go, to everything that matched.

// plan is one search.
type plan struct {
	sel   string // SELECT … FROM … WHERE …, without ORDER BY
	args  []any
	order string // ORDER BY …, when the requested order is pushed down to SQL
	// keys: the columns order sorts by, each with its direction; keyRow: which row of
	// a pair carries them. A page reads on from a row's key (pagePositions).
	keys   []keyCol
	keyRow int
	pairs   bool // rows are a tracked entity and one of its enrollments in the program
	// needData: keep or sort read the objects' slim data, so each row is decoded.
	needData bool
	keep     func(o, enr *Obj) (bool, *APIError)
	// finish runs on everything kept, when the order is not pushed down: it sorts, and
	// may filter further.
	finish func([]*Obj) ([]*Obj, *APIError)
}

// keyCol is one column of a plan's sort key and whether it descends.
type keyCol struct {
	col  string
	desc bool
}

// where collects a plan's conditions.
type where struct {
	conds []string
	args  []any
}

func (w *where) add(cond string, args ...any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *where) in(col string, vals []string) {
	if len(vals) == 0 {
		return
	}
	args := make([]any, len(vals))
	for i, v := range vals {
		args[i] = v
	}
	w.add(col+" IN (?"+strings.Repeat(",?", len(vals)-1)+")", args...)
}

func (w *where) sql() string { return strings.Join(w.conds, " AND ") }

// createdOrder reports whether order sorts by creation date alone (the default when
// empty), and whether descending: the order SQL can give.
func createdOrder(order string) (ok, desc bool) {
	keys := splitList(order)
	if len(keys) == 0 {
		return true, false
	}
	if len(keys) > 1 {
		return false, false
	}
	f, dir, _ := strings.Cut(keys[0], ":")
	return f == "createdAt", strings.EqualFold(dir, "desc")
}

// keyOrder is the column a single-key order sorts by in SQL, whether descending, and
// whether SQL can give it: creation (also the default), and for events the event
// date (occurred_at, as working lists ask).
func keyOrder(order string, events bool) (col string, desc, ok bool) {
	if byCreated, d := createdOrder(order); byCreated {
		return "created_at", d, true
	}
	keys := splitList(order)
	if events && len(keys) == 1 {
		if f, dir, _ := strings.Cut(keys[0], ":"); f == "occurredAt" {
			return "occurred_at", strings.EqualFold(dir, "desc"), true
		}
	}
	return "", false, false
}

// updatedFloor is a lower bound for the update times of objects updated after the
// updatedAfter date: a day earlier, in the form stored times start with, so that a
// string comparison keeps a superset whatever the time zones (central's times carry
// none). updatedMatch decides exactly.
func updatedFloor(q url.Values) (string, *APIError) {
	s := q.Get("updatedAfter")
	if s == "" {
		return "", nil
	}
	t, ok := parseTime(s)
	if !ok {
		return "", badRequest("invalid date for updatedAfter: %s", s)
	}
	return t.Add(-24 * time.Hour).Format("2006-01-02T15:04:05"), nil
}

// updatedSince narrows a search over one kind to objects that may have changed since
// updatedAfter.
func updatedSince(w *where, alias string, q url.Values) *APIError {
	floor, err := updatedFloor(q)
	if floor != "" && hubClockBehind(q) {
		w.add("("+alias+".origin = 'local' OR "+alias+".updated_at >= ?)", floor)
	} else if floor != "" {
		w.add(alias+".updated_at >= ?", floor)
	}
	return err
}

// hubClockBehind reports whether updatedAfter is more than a day ahead of the hub's
// clock, in any time zone (up to UTC+14). The hub's clock is then behind central's (a
// Pi without a clock battery started with no network, docs/DEPLOYMENT.md, "Clock"), its
// write times say nothing against the watermark, and everything written on the hub
// counts as updated (hubUpdated).
func hubClockBehind(q url.Values) bool {
	t, ok := parseTime(q.Get("updatedAfter"))
	return ok && t.After(time.Now().UTC().Add(38*time.Hour))
}

// changedTEs returns the tracked entities that may have changed after updatedAfter
// (Android's incremental sync), from the indexes alone: those with anything updated at
// or after floor, for objects written on the hub by the hub's write time, and with
// allHub everyone with anything written on the hub (hubUpdated). With exact (no
// updatedBefore), a person with nothing written on the hub is left out when the newest
// of all their rows is older than updatedAfter: updatedMatch could not count them,
// whichever children the user may read. During an outage the day's floor kept central's
// people of the last day as candidates, and checking each in updatedMatch cost two
// queries (docs/PERFORMANCE.md, "Android syncs on the Pi 4's card").
func (ix *Index) changedTEs(floor string, allHub bool, after time.Time, exact bool) []string {
	newest := map[string]string{}
	each := func(query string, arg []any, row func(uid, at string)) {
		rows, err := ix.store.DB().Query(query, arg...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var uid, at string
			if rows.Scan(&uid, &at) == nil && uid != "" {
				row(uid, at)
			}
		}
	}
	each(`SELECT iif(kind = 'trackedEntity', uid, te_uid), updated_at FROM objects WHERE updated_at >= ?`, []any{floor},
		func(uid, at string) {
			if prev, ok := newest[uid]; !ok || later(at, prev) {
				newest[uid] = at
			}
		})
	var out []string
	hub := map[string]bool{}
	each(`SELECT iif(kind = 'trackedEntity', uid, te_uid), '' FROM objects WHERE origin = 'local'`, nil,
		func(uid, _ string) {
			if _, ok := newest[uid]; (ok || allHub) && !hub[uid] {
				out = append(out, uid)
			}
			hub[uid] = true
		})
	for uid, at := range newest {
		if hub[uid] {
			continue
		}
		if t, ok := parseTime(at); !exact || ok && !t.Before(after) {
			out = append(out, uid)
		}
	}
	return out
}

// later reports whether stored time a is after b. Times of one fixed-width form without
// a zone compare as strings; others are parsed.
func later(a, b string) bool {
	if len(a) == len(b) && !strings.ContainsAny(a[min(len(a), 19):]+b[min(len(b), 19):], "Z+-") {
		return a > b
	}
	ta, oka := parseTime(a)
	tb, okb := parseTime(b)
	if oka && okb {
		return ta.After(tb)
	}
	return oka || a > b
}

// run returns what the plan keeps, in order. With want > 0 and the order pushed down,
// it stops after want and reports whether there are more, and last: the row that
// carries the creation key of the last object returned ("" when it did not stop).
func (ix *Index) run(v *Viewer, p *plan, want int) ([]*Obj, bool, string, *APIError) {
	if ix.store == nil || !ix.store.Unlocked() {
		return nil, false, "", nil
	}
	bm := ix.bitmapOf(v.UserID)
	seen := map[string]bool{}
	var out []*Obj
	early := want > 0 && p.order != ""
	query := p.sel
	if !p.needData {
		// Nothing reads the rows' slim data: leave it out (the page's objects are read
		// again in full by Search).
		query = slimCol.ReplaceAllString(query, "''")
	}
	if p.order != "" {
		query += " ORDER BY " + p.order
	}
	// Rows are read in chunks, growing, so a full page usually costs one small read,
	// and nothing holds a connection while the checks run their own queries.
	chunk := 0
	if early {
		chunk = want + 1 // a page and one more, to know whether more follow
	}
	last := ""
	ctx := v.ctx()
	for off := 0; ; {
		q, args := query, p.args
		if chunk > 0 {
			q += " LIMIT ? OFFSET ?"
			args = append(slices.Clone(args), chunk, off)
		}
		var rows [][2]*store.Object
		var err error
		if p.pairs {
			rows, err = ix.store.QueryObjectPairs(q, args...)
		} else {
			var single []*store.Object
			single, err = ix.store.QueryObjects(q, args...)
			for _, r := range single {
				rows = append(rows, [2]*store.Object{r, nil})
			}
		}
		if err != nil {
			return nil, false, "", &APIError{Status: 500, Message: "search: " + err.Error()}
		}
		for _, r := range rows {
			if ctx.Err() != nil {
				return nil, false, "", gone
			}
			if seen[r[0].UID] {
				continue
			}
			o := ix.row(r[0], p.needData)
			if !ix.visibleWith(v, bm, o) {
				continue
			}
			var enr *Obj
			if r[1] != nil {
				if enr = ix.row(r[1], p.needData); !ix.visibleWith(v, bm, enr) {
					continue
				}
			}
			if ok, err := p.keep(o, enr); err != nil {
				return nil, false, "", err
			} else if !ok {
				continue
			}
			seen[o.UID] = true
			out = append(out, o)
			if early && len(out) == want && r[p.keyRow] != nil {
				last = r[p.keyRow].UID
			}
			if early && len(out) > want {
				return out[:want], true, last, nil
			}
		}
		if chunk == 0 || len(rows) < chunk {
			break
		}
		off += chunk // where this read stopped, before the next read grows
		chunk *= 4
	}
	if p.order == "" && p.finish != nil {
		var err *APIError
		if out, err = p.finish(out); err != nil {
			return nil, false, "", err
		}
	}
	return out, false, last, nil
}

// after is p reading on after the row last (pagePositions): rows whose sort key comes
// after last's in p's order. ok is false when last is not in the store.
func (ix *Index) after(p *plan, last string) (*plan, bool) {
	col := func(c string) string { return c[strings.IndexByte(c, '.')+1:] }
	cols := make([]string, len(p.keys))
	for i, k := range p.keys {
		cols[i] = col(k.col)
	}
	ptrs := make([]any, len(cols))
	strs := make([]string, len(cols))
	for i := range cols {
		ptrs[i] = &strs[i]
	}
	if ix.store.DB().QueryRow(`SELECT `+strings.Join(cols, ", ")+` FROM objects WHERE uid = ?`, last).Scan(ptrs...) != nil {
		return nil, false
	}
	q := *p
	cond, args := afterConds(p, strs)
	q.sel = p.sel + " AND " + cond
	q.args = append(slices.Clone(p.args), args...)
	return &q, true
}

// afterConds is the range condition for the rows whose sort key comes after last's
// (vals) in p's order: k0 past last's, or k0 equal and k1 past last's, …
func afterConds(p *plan, vals []string) (string, []any) {
	var conds []string
	var args []any
	for i, k := range p.keys {
		cmp := ">"
		if k.desc {
			cmp = "<"
		}
		terms := make([]string, 0, i+1)
		for j := 0; j < i; j++ {
			terms = append(terms, p.keys[j].col+" = ?")
			args = append(args, vals[j])
		}
		terms = append(terms, k.col+" "+cmp+" ?")
		args = append(args, vals[i])
		conds = append(conds, "("+strings.Join(terms, " AND ")+")")
	}
	return "(" + strings.Join(conds, " OR ") + ")", args
}

// slimCol matches the slim column in store.ObjectCols.
var slimCol = regexp.MustCompile(`coalesce\(([a-z]+\.)?slim, ''\)`)

// row converts a row; without needData its slim data stays encoded until rendering
// asks for it (withData).
func (ix *Index) row(r *store.Object, needData bool) *Obj {
	if needData {
		if o, err := FromStore(r); err == nil {
			return o
		}
	}
	return &Obj{UID: r.UID, Kind: r.Kind, TE: r.TE, Enrollment: r.Enrollment, Program: r.Program, Stage: r.Stage,
		TET: r.TET, OwnerOrgUnit: r.OwnerOrgUnit, UpdatedAt: r.UpdatedAt, Origin: r.Origin, SyncState: r.SyncState,
		Slot: uint32(r.Slot), Deleted: r.Deleted, Stub: r.Stub, slim: true, raw: r.Slim}
}

// withData returns o with its slim data decoded: o itself, or a decoded copy (o may be
// shared with other readers).
func withData(o *Obj) *Obj {
	if o.Data != nil {
		return o
	}
	n := *o
	n.Data = M{}
	if len(o.raw) > 0 {
		json.Unmarshal(o.raw, &n.Data)
	}
	return &n
}

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
	v.IncludeDeleted = q.Get("includeDeleted") == "true"
	pg, size, paged, totals, perr := paging(q)
	if perr != nil {
		return nil, perr
	}
	program := q.Get("program")
	if err := ix.programCheck(v, program); err != nil {
		return nil, err
	}
	fields := FieldsOrDefault(collection, q.Get("fields"))
	var p *plan
	var objs []*Obj
	var render func(*Obj) M
	var err *APIError
	switch collection {
	case "trackedEntities":
		p, err = ix.searchTEs(v, q, program)
		render = func(o *Obj) M { return ix.RenderTE(v, o, fields, program) }
	case "enrollments":
		p, err = ix.searchEnrollments(v, q, program)
		render = func(o *Obj) M { return ix.RenderEnrollment(v, o, fields) }
	case "events":
		p, err = ix.searchEvents(v, q, program)
		render = func(o *Obj) M { return ix.RenderEvent(v, o, fields) }
	case "relationships":
		objs, err = ix.searchRelationships(v, q)
		render = func(o *Obj) M { return shallow(ix.fullFor(v, o)) }
	default:
		return nil, NeedsConnection(collection)
	}
	if err != nil {
		return nil, err
	}
	more, readOn := false, false
	var shown []*Obj
	if p != nil {
		// Read up to the end of the asked page and stop there, unless the total is
		// asked for (or the page is beyond any real size). Android asks for pages 1 to
		// 10 of 50; reading the whole program for every page after the first cost
		// about a second at 100 000 people (docs/PERFORMANCE.md).
		want := 0
		early := paged && !totals && pg <= 1_000_000/size && p.order != ""
		if early {
			want = pg * size
		}
		key := fmt.Sprintf("%d|%s|%s", v.UserID, collection, withoutPage(q))
		last, ok := ix.pages.get(key, pg)
		var on *plan
		if early && ok && len(p.keys) > 0 {
			on, ok = ix.after(p, last)
		}
		if early && ok && on != nil {
			// The page before was just served: read on after its last row.
			if shown, more, last, err = ix.run(v, on, size); err != nil {
				return nil, err
			}
			if last != "" {
				ix.pages.set(key, pg, last)
			}
			readOn = true
			objs = make([]*Obj, (pg-1)*size, (pg-1)*size+len(shown)) // stands in for the pages before, for the count
			objs = append(objs, shown...)
		} else {
			if objs, more, last, err = ix.run(v, p, want); err != nil {
				return nil, err
			}
			if early && last != "" && len(p.keys) > 0 {
				ix.pages.set(key, pg, last)
			}
		}
	}
	res := &Result{Collection: collection, Total: len(objs), Page: pg, PageSize: size, Paging: paged, TotalPages: totals,
		Items: []M{}}
	if more {
		res.Total++ // the page is full and more follow; the exact total was not asked for
	}
	if !readOn {
		shown = page(objs, pg, size, paged)
	}
	shown = slices.Clone(shown)
	var bare []string
	for _, o := range shown {
		if o.Data == nil && len(o.raw) == 0 {
			bare = append(bare, o.UID)
		}
	}
	got := ix.GetMany(bare) // rows read without their slim data
	for i, o := range shown {
		if n := got[o.UID]; n != nil {
			shown[i] = n
		} else {
			shown[i] = withData(o)
		}
	}
	ix.prefetch(v, shown, fields)
	for _, o := range shown {
		res.Items = append(res.Items, fields.Project(render(o)).(M))
	}
	return res, nil
}

// searchCandidatesMax is how many people an attribute filter may match for its
// matches to drive a search. Above it the search walks the program in its order, as
// a common name fills the first page within a few rows.
const searchCandidatesMax = 1000

// searchCandidates returns, as a JSON list, the people whose attribute matches cond,
// and whether they are few enough to drive the search. It reads only the te_attrs_lc
// index. A filter that matches few people otherwise read every enrollment of the
// program (151 ms at 100 000 people; docs/PERFORMANCE.md, "Name searches").
func (ix *Index) searchCandidates(attr, cond string, args []any) (string, bool) {
	if ix.store == nil || !ix.store.Unlocked() {
		return "", false
	}
	rows, err := ix.store.DB().Query("SELECT a.te_uid FROM te_attrs a WHERE a.attr = ? AND "+cond+" LIMIT ?",
		append(append([]any{attr}, args...), searchCandidatesMax+1)...)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	var uids []string
	for rows.Next() {
		var uid string
		if rows.Scan(&uid) == nil {
			uids = append(uids, uid)
		}
	}
	if rows.Err() != nil || len(uids) > searchCandidatesMax {
		return "", false
	}
	b, _ := json.Marshal(uids)
	return string(b), true
}

func (ix *Index) searchTEs(v *Viewer, q url.Values, program string) (*plan, *APIError) {
	if err := checkOrgUnits(v, q, len(listParam(q, "trackedEntities", "trackedEntity")) == 0); err != nil {
		return nil, err
	}
	tet := q.Get("trackedEntityType")
	uids := listParam(q, "trackedEntities", "trackedEntity")
	if program == "" && tet == "" && len(uids) == 0 {
		return nil, badRequest("Either `program`, `trackedEntityType` or `trackedEntities` should be specified")
	}
	filters, err := parseFilters(q)
	if err != nil {
		return nil, err
	}
	status := cmp.Or(q.Get("enrollmentStatus"), q.Get("programStatus"))
	if status != "" && program == "" {
		return nil, badRequest("`program` must be defined when `enrollmentStatus` is specified")
	}
	upd := timeFilter{"updatedAt", "updatedAfter", "updatedBefore"}
	enrolled := timeFilter{"enrolledAt", "enrollmentEnrolledAfter", "enrollmentEnrolledBefore"}
	p := &plan{}
	w := &where{}
	if program != "" {
		// With a program: the people with a visible enrollment in it that passes the
		// status and enrolment-date filters, one row per such enrollment.
		p.pairs = true
		p.sel = "SELECT " + store.ObjectCols("t") + ", " + store.ObjectCols("e") +
			" FROM objects e JOIN objects t ON t.uid = e.te_uid AND t.kind = 'trackedEntity' WHERE "
		w.add("e.kind = 'enrollment' AND e.program = ? AND e.owner_org_unit = ?", program, v.Facility)
		if status != "" {
			w.add("e.status = ?", status)
		}
	} else {
		p.sel = "SELECT " + store.ObjectCols("t") + " FROM objects t WHERE "
		w.add("t.kind = 'trackedEntity'")
	}
	w.add("t.owner_org_unit = ?", v.Facility)
	if !v.IncludeDeleted {
		w.add("t.deleted = 0")
		if p.pairs {
			w.add("e.deleted = 0")
		}
	}
	if p.pairs {
		w.add("e.stub = 0") // stubs of pruned enrollments are never read
	}
	if tet != "" {
		w.add("t.tet = ?", tet)
	}
	w.in("t.uid", uids)
	// With a program the filter refers to the enrollment's person, so SQLite applies it
	// before looking the person up.
	person := "t.uid"
	if program != "" {
		person = "e.te_uid"
	}
	if err := ix.attrSQL(v, w, filters, person); err != nil {
		return nil, err
	}
	// Changed people are found once and checked in SQL, on the person's identifier in
	// the index the search walks, so the rows of people who did not change are never
	// read: on a hard disk an incremental sync read every enrollment of the program
	// (docs/INCREMENTAL-SYNC-FIX.md). One JSON list, whatever its size.
	floor, apiErr := updatedFloor(q)
	if apiErr != nil {
		return nil, apiErr
	}
	if floor != "" && ix.store != nil && ix.store.Unlocked() {
		after, _ := parseTime(q.Get(upd.after))
		list, _ := json.Marshal(ix.changedTEs(floor, hubClockBehind(q), after, q.Get(upd.before) == ""))
		w.add(person+" IN (SELECT value FROM json_each(?))", string(list))
	}
	p.sel += w.sql()
	p.args = w.args
	byCreated, desc := createdOrder(q.Get("order"))
	if byCreated {
		dir := ""
		if desc {
			dir = " DESC"
		}
		if program != "" {
			p.order = "e.te_created" + dir + ", e.te_uid DESC"
			p.keys, p.keyRow = []keyCol{{"e.te_created", desc}, {"e.te_uid", true}}, 1
		} else {
			p.order = "t.created_at" + dir + ", t.uid DESC"
			p.keys = []keyCol{{"t.created_at", desc}, {"t.uid", true}}
		}
	}
	timed := func(tf timeFilter) bool { return q.Get(tf.after) != "" || q.Get(tf.before) != "" }
	p.needData = timed(enrolled) || !byCreated
	p.keep = func(o, enr *Obj) (bool, *APIError) {
		if enr != nil {
			if ok, err := enrolled.match(enr.Data, q); err != nil || !ok {
				return false, err
			}
		}
		// The values matched in SQL; what this user may read of them is checked here.
		for _, f := range filters {
			if v.Catalog == nil || !v.Catalog.ReadableAttribute(v.Access, o.TET, f.uid) {
				return false, nil
			}
		}
		return ix.updatedMatch(v, o, upd, q)
	}
	p.finish = func(out []*Obj) ([]*Obj, *APIError) {
		sortObjs(out, q.Get("order"), func(o *Obj, attr string) string {
			return attrValues(o.Data, "attributes", "attribute")[attr]
		})
		return out, nil
	}
	return p, nil
}

func (ix *Index) searchEnrollments(v *Viewer, q url.Values, program string) (*plan, *APIError) {
	te := q.Get("trackedEntity")
	uids := listParam(q, "enrollments")
	if err := checkOrgUnits(v, q, te == "" && len(uids) == 0); err != nil {
		return nil, err
	}
	status := cmp.Or(q.Get("status"), q.Get("enrollmentStatus"), q.Get("programStatus"))
	upd := timeFilter{"updatedAt", "updatedAfter", "updatedBefore"}
	enrolled := timeFilter{"enrolledAt", "enrolledAfter", "enrolledBefore"}
	w := &where{}
	w.add("e.kind = 'enrollment' AND e.owner_org_unit = ?", v.Facility)
	if !v.IncludeDeleted {
		w.add("e.deleted = 0")
	}
	w.add("e.stub = 0") // stubs of pruned enrollments are never read
	w.in("e.uid", uids)
	if te != "" {
		w.add("e.te_uid = ?", te)
	}
	if program != "" {
		w.add("e.program = ?", program)
	}
	if status != "" {
		w.add("e.status = ?", status)
	}
	if err := updatedSince(w, "e", q); err != nil {
		return nil, err
	}
	return ix.simplePlan(v, w, q, "e", []timeFilter{enrolled}, upd, nil), nil
}

// simplePlan is a search over one kind with the conditions in w, dates checked in Go
// and createdAt order pushed down.
func (ix *Index) simplePlan(v *Viewer, w *where, q url.Values, alias string, tfs []timeFilter, upd timeFilter,
	finish func([]*Obj) ([]*Obj, *APIError)) *plan {
	p := &plan{sel: "SELECT " + store.ObjectCols(alias) + " FROM objects " + alias + " WHERE " + w.sql(), args: w.args}
	col, desc, ok := keyOrder(q.Get("order"), alias == "ev")
	if ok && finish == nil {
		p.order = alias + "." + col
		if desc {
			p.order += " DESC"
		}
		if col == "created_at" {
			p.order += ", " + alias + ".uid DESC"
			p.keys = []keyCol{{alias + "." + col, desc}, {alias + ".uid", true}}
		} else {
			// Events by event date: central breaks ties on the date by creation time,
			// newest first, then by UID (observed on 2.42), both descending.
			p.order += ", " + alias + ".created_at DESC, " + alias + ".uid DESC"
			p.keys = []keyCol{{alias + "." + col, desc}, {alias + ".created_at", true}, {alias + ".uid", true}}
		}
	}
	p.needData = p.order == ""
	for _, tf := range tfs {
		p.needData = p.needData || q.Get(tf.after) != "" || q.Get(tf.before) != ""
	}
	p.keep = func(o, _ *Obj) (bool, *APIError) {
		if ok, err := matchAll(o.Data, q, tfs); err != nil || !ok {
			return false, err
		}
		return ix.updatedMatch(v, o, upd, q)
	}
	p.finish = finish
	if p.finish == nil {
		p.finish = func(out []*Obj) ([]*Obj, *APIError) {
			sortObjs(out, q.Get("order"), nil)
			return out, nil
		}
	}
	return p
}

// updatedMatch applies updatedAfter/updatedBefore. A tracked entity counts as updated
// when anything under it was (central bumps it too). It reads the stored update times
// only (central's updatedAt, kept in its column): an incremental sync may check every
// person of a program, and decoding their records cost memory in proportion
// (docs/PERFORMANCE.md).
func (ix *Index) updatedMatch(v *Viewer, o *Obj, tf timeFilter, q url.Values) (bool, *APIError) {
	if q.Get(tf.after) == "" && q.Get(tf.before) == "" {
		return true, nil
	}
	floor, apiErr := updatedFloor(q)
	if apiErr != nil {
		return false, apiErr
	}
	if hubClockBehind(q) {
		floor = ""
	}
	newest := ""
	if o.Origin == store.OriginLocal {
		if hubUpdated(o.UpdatedAt, floor) {
			return true, nil
		}
	} else {
		newest = o.UpdatedAt
	}
	if o.Kind == store.KindTrackedEntity {
		local, hub, t, err := ix.newestChild(v, o.UID)
		if err != nil {
			return false, err
		}
		if local && hubUpdated(hub, floor) {
			return true, nil
		}
		newest = max(newest, t)
	}
	return tf.match(M{"updatedAt": newest}, q)
}

// hubUpdated reports whether an object written on the hub at t (the hub's clock) counts
// as updated after updatedAfter: when it was written at or after floor, a day before
// it. Clients compare against a watermark from central's clock, which the hub's may
// differ from by some drift, so the hub errs towards re-sending, but a sync during a
// long outage gets the last day's work, not the whole outage's (docs/PERFORMANCE.md, "A
// week offline at HC IV size"). With no updatedAfter, or the hub's clock behind
// (hubClockBehind), everything.
func hubUpdated(t, floor string) bool {
	return floor == "" || t >= floor
}

// The children newestChild reads: those written on the hub, and central's, with the
// conditions of visibleWith that SQL can apply, with and without the deleted ones.
// Built once: building them per person cost more than reading the rows.
var (
	sqlHubChildren = `SELECT ` + slimCol.ReplaceAllString(store.ObjectCols(""), "''") +
		` FROM objects INDEXED BY objects_te WHERE te_uid = ? AND kind IN ('enrollment', 'event') AND origin <> 'central'`
	sqlCentralChildren = `SELECT coalesce(slot, 0), kind, program, stage, updated_at FROM objects INDEXED BY objects_te
 WHERE te_uid = ? AND kind IN ('enrollment', 'event') AND origin = 'central' AND owner_org_unit = ? AND stub = 0`
	sqlCentralChildrenLive = sqlCentralChildren + ` AND deleted = 0 ORDER BY updated_at DESC`
	sqlCentralChildrenAll  = sqlCentralChildren + ` ORDER BY updated_at DESC`
)

// newestChild reports whether the user may read an enrollment or event of te written
// on the hub, and the newest hub write time among those, and the newest update time
// among those of central they may read. Each child is checked with visibleWith, as when all were read. Central's are
// read newest first, with only the columns the check needs after the facility,
// deleted and stub conditions in SQL, and reading stops at the first the user may
// read: an incremental sync checks every changed person, and reading all their
// children ran a small PC out of memory (docs/INCREMENTAL-SYNC-FIX.md).
func (ix *Index) newestChild(v *Viewer, te string) (local bool, hub, newest string, apiErr *APIError) {
	if ix.store == nil || !ix.store.Unlocked() {
		return false, "", "", nil
	}
	bm := ix.bitmapOf(v.UserID)
	rows, err := ix.store.QueryObjects(sqlHubChildren, te)
	if err != nil {
		slog.Warn("index query", "err", err)
	}
	for _, r := range rows {
		// Without data: optionsReadable reads the object when it needs its combination.
		if o := ix.row(r, false); (!local || o.UpdatedAt > hub) && ix.visibleWith(v, bm, o) {
			local, hub = true, max(hub, o.UpdatedAt)
		}
	}
	if v.Access == nil {
		return local, hub, "", nil
	}
	query := sqlCentralChildrenLive
	if v.IncludeDeleted {
		query = sqlCentralChildrenAll
	}
	c := &Obj{Origin: store.OriginCentral, OwnerOrgUnit: v.Facility}
	err = ix.store.Each(v.ctx(), query, []any{te, v.Facility}, func(scan func(...any) error) (bool, error) {
		if err := scan(&c.Slot, &c.Kind, &c.Program, &c.Stage, &c.UpdatedAt); err != nil {
			return false, err
		}
		if ix.visibleWith(v, bm, c) {
			newest = c.UpdatedAt
			return false, nil
		}
		return true, nil
	})
	if v.ctx().Err() != nil {
		return false, "", "", gone
	}
	if err != nil {
		slog.Warn("index query", "err", err)
	}
	return local, hub, newest, nil
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

func (ix *Index) searchEvents(v *Viewer, q url.Values, program string) (*plan, *APIError) {
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
	if len(filters) > 0 && len(uids) > 0 {
		return nil, &APIError{Status: 400, Code: "E1003", Message: "Event UIDs and filters can not be specified at the same time"}
	}
	kinds := make([]valueKind, len(filters))
	for i, f := range filters {
		vt := v.valueType(f.uid)
		kinds[i] = v.filterKind(f, vt, true)
		if err := f.check(kinds[i], vt, "data element"); err != nil {
			return nil, err
		}
	}
	upd := timeFilter{"updatedAt", "updatedAfter", "updatedBefore"}
	tfs := []timeFilter{{"occurredAt", "occurredAfter", "occurredBefore"}, {"scheduledAt", "scheduledAfter", "scheduledBefore"}}
	w := &where{}
	w.add("ev.kind = 'event' AND ev.owner_org_unit = ?", v.Facility)
	if !v.IncludeDeleted {
		w.add("ev.deleted = 0")
	}
	w.in("ev.uid", uids)
	w.in("ev.enrollment_uid", enrs)
	if te != "" {
		w.add("ev.te_uid = ?", te)
	}
	if program != "" {
		w.add("ev.program = ?", program)
	}
	if stage != "" {
		w.add("ev.stage = ?", stage)
	}
	if status != "" {
		w.add("ev.status = ?", status)
	}
	if enrStatus != "" {
		w.add("EXISTS (SELECT 1 FROM objects x WHERE x.uid = ev.enrollment_uid AND x.status = ?)", enrStatus)
	}
	if err := updatedSince(w, "ev", q); err != nil {
		return nil, err
	}
	// Filters on the person's attributes (Android's filterAttributes): events of
	// people who match, as person searches apply them.
	attrFilters, err := parseFilterParam(q, "filterAttributes")
	if err != nil {
		return nil, err
	}
	if err := ix.attrSQL(v, w, attrFilters, "ev.te_uid"); err != nil {
		return nil, err
	}
	byValue := len(filters) > 0
	for _, p := range splitList(q.Get("order")) {
		f, _, _ := strings.Cut(p, ":")
		byValue = byValue || ValidUID(f)
	}
	var finish func([]*Obj) ([]*Obj, *APIError)
	if byValue {
		// Data value filters and ordering read the full payloads: of everything else
		// that matched, in one go.
		finish = func(cands []*Obj) ([]*Obj, *APIError) {
			ix.prefetch(v, cands, ParseFields("dataValues"))
			var out []*Obj
			for _, o := range cands {
				vals := attrValues(ix.fullFor(v, o), "dataValues", "dataElement")
				match := true
				for i, f := range filters {
					if !f.matchDataValue(vals, kinds[i]) {
						match = false
						break
					}
				}
				if match {
					out = append(out, o)
				}
			}
			sortObjs(out, q.Get("order"), func(o *Obj, de string) string {
				return attrValues(ix.fullFor(v, o), "dataValues", "dataElement")[de]
			})
			return out, nil
		}
	}
	return ix.simplePlan(v, w, q, "ev", tfs, upd, finish), nil
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
	out := ix.visible(v, ix.relationshipsFor(v, uid))
	sortObjs(out, q.Get("order"), nil)
	return out, nil
}

// withoutPage is the query as a key for pagePositions: every parameter but the page.
func withoutPage(q url.Values) string {
	c := url.Values{}
	for k, v := range q {
		if k != "page" {
			c[k] = v
		}
	}
	return c.Encode()
}
