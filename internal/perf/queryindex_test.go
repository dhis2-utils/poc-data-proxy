package perf

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// TestQueryIndex is the prototype of the SQLite query index (docs/PERFORMANCE.md, "The
// query index in SQLite"). It fills a hub like TestPull, builds indexed query tables in
// the hub's own encrypted data file, and times the queries that matter both ways:
// today's in-memory search, end to end over HTTP, and the SQLite selection of the same
// page, checked to pick the same records. Rendering a page reads payloads from the
// store in both designs, so it is timed once, on its own. Visibility stays in memory
// (the bitmaps), checked per row as the rows stream in order.
func TestQueryIndex(t *testing.T) {
	s, users := scale(t)
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Generate(s)
	e.Setup()
	clients := make([]*tk.Client, users)
	for i := range clients {
		clients[i] = e.AndroidUser(fc.GenUser(i))
		if st, b := clients[i].Do("GET", "/api/me?fields=id", nil); st != 200 {
			t.Fatalf("%s: %d %s", fc.GenUser(i), st, b)
		}
	}
	e.Sync()
	e.Offline()
	ix, st := e.App.Hub.Index, e.App.Hub.Store
	u, err := st.UserByName(fc.GenUser(0))
	if err != nil || u == nil {
		t.Fatalf("user: %v", err)
	}
	report := []string{fmt.Sprintf("scale: %d TEs, %d enrollments, %d events, %d users; %d objects in the index",
		s.TEs, s.Enrollments, s.Events, users, ix.Len())}

	sizeBefore := dirMB(e.Dir)
	start := time.Now()
	if err := buildQueryIndex(st.DB(), ix); err != nil {
		t.Fatal(err)
	}
	report = append(report, fmt.Sprintf("query tables built in %s; data file %.0f → %.0f MB",
		time.Since(start).Round(time.Millisecond), sizeBefore, dirMB(e.Dir)))

	visible := func(slot int64) bool { return ix.VisibleTo(&tracker.Obj{Slot: uint32(slot)}, u.ID) }
	sq := &sqlQueries{db: st.DB(), visible: visible}
	c := clients[0]
	prog, ou := fc.GenProgram(0), fc.Facility
	const runs = 15

	// Each case: the hub's request, and the SQLite selection of the same page.
	type tcase struct {
		name string
		path string
		sel  func() ([]string, int, error)
	}
	list := "/api/tracker/trackedEntities?program=" + prog + "&orgUnits=" + ou
	// The deep download page: halfway through the program's people.
	var people int
	if err := st.DB().QueryRow(`SELECT count(DISTINCT te_uid) FROM qi_enr WHERE program=? AND owner=?`, prog, ou).Scan(&people); err != nil {
		t.Fatal(err)
	}
	deep := people/50/2 + 1
	cases := []tcase{
		{"working list, page 1 of 15",
			list + "&page=1&pageSize=15&order=createdAt:desc&fields=trackedEntity",
			func() ([]string, int, error) { return sq.list(prog, ou, "", true, 0, 15, false) }},
		{"working list, page 1 of 15, with total",
			list + "&page=1&pageSize=15&order=createdAt:desc&totalPages=true&fields=trackedEntity",
			func() ([]string, int, error) { return sq.list(prog, ou, "", true, 0, 15, true) }},
		{"working list, active only",
			list + "&enrollmentStatus=ACTIVE&page=1&pageSize=15&order=createdAt:desc&fields=trackedEntity",
			func() ([]string, int, error) { return sq.list(prog, ou, "ACTIVE", true, 0, 15, false) }},
		{"name search, common name",
			list + "&filter=" + fc.AttrFirstName + ":like:Amina&pageSize=15&fields=trackedEntity",
			func() ([]string, int, error) {
				return sq.search(prog, ou, []attrLike{{fc.AttrFirstName, "amina"}}, 15)
			}},
		{"name search, first and last name",
			list + "&filter=" + fc.AttrFirstName + ":like:Amina&filter=" + fc.AttrLastName + ":like:Phiri&pageSize=15&fields=trackedEntity",
			func() ([]string, int, error) {
				return sq.search(prog, ou, []attrLike{{fc.AttrFirstName, "amina"}, {fc.AttrLastName, "phiri"}}, 15)
			}},
		{"name search, nobody (full scan)",
			list + "&filter=" + fc.AttrFirstName + ":like:Zelda&pageSize=15&fields=trackedEntity",
			func() ([]string, int, error) {
				return sq.search(prog, ou, []attrLike{{fc.AttrFirstName, "zelda"}}, 15)
			}},
		{"Android download, page 1 of 50",
			list + "&page=1&pageSize=50&fields=trackedEntity",
			func() ([]string, int, error) { return sq.list(prog, ou, "", false, 0, 50, false) }},
		{fmt.Sprintf("Android download, page %d of 50", deep),
			list + "&page=" + fmt.Sprint(deep) + "&pageSize=50&fields=trackedEntity",
			func() ([]string, int, error) { return sq.list(prog, ou, "", false, (deep-1)*50, 50, false) }},
	}
	report = append(report, fmt.Sprintf("%-40s %22s %22s  %s", "query: median (first) of 15", "in memory", "SQLite", "same page"))
	for _, tc := range cases {
		var mem []string
		memT := median(runs, func() {
			// Pages after the first come from the result cache after the first run, as
			// when a client pages through: the report shows the first run as well.
			st, b := c.Do("GET", tc.path, nil)
			if st != 200 {
				t.Fatalf("%s: %d %s", tc.name, st, b)
			}
			mem = teUIDs(t, b)
		})
		var got []string
		var total int
		sqlT := median(runs, func() {
			var err error
			if got, total, err = tc.sel(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		})
		same := "yes"
		if !slices.Equal(mem, got) {
			same = fmt.Sprintf("NO: hub %d %v…, SQLite %d %v…", len(mem), head(mem), len(got), head(got))
			t.Errorf("%s: pages differ", tc.name)
		}
		if strings.Contains(tc.name, "total") {
			same += fmt.Sprintf(" (total %d)", total)
		}
		report = append(report, fmt.Sprintf("%-40s %22s %22s  %s", tc.name, memT, sqlT, same))
	}

	// Rendering 15 tracked entities as a working list shows them, by uid: the same in
	// both designs.
	page, _, _ := sq.list(prog, ou, "", true, 0, 15, false)
	render := median(runs, func() {
		c.Do("GET", "/api/tracker/trackedEntities?program="+prog+"&orgUnits="+ou+"&trackedEntities="+strings.Join(page, ",")+
			"&fields=trackedEntity,attributes[attribute,value],enrollments[enrollment,status,enrolledAt]", nil)
	})
	report = append(report, fmt.Sprintf("%-40s %22s", "rendering those 15 (both designs)", render))

	// Memory: the index's objects are what the SQLite design no longer holds.
	withIndex := heapMB()
	ix.Clear()
	report = append(report, fmt.Sprintf("heap with the in-memory index: %.0f MB; without its objects: %.0f MB", withIndex, heapMB()))

	out := strings.Join(report, "\n")
	t.Log("\n" + out)
	if p := os.Getenv("HUB_PERF_OUT"); p != "" {
		os.WriteFile(p, []byte(out+"\n"), 0o644)
	}
}

type timing struct{ first, median time.Duration }

func (t timing) String() string {
	r := func(d time.Duration) time.Duration { return d.Round(10 * time.Microsecond) }
	return fmt.Sprintf("%s (%s)", r(t.median), r(t.first))
}

// median runs fn n times: the median, and the first run.
func median(n int, fn func()) timing {
	d := make([]time.Duration, n)
	for i := range d {
		start := time.Now()
		fn()
		d[i] = time.Since(start)
	}
	first := d[0]
	slices.Sort(d)
	return timing{first, d[n/2]}
}

func teUIDs(t *testing.T, b []byte) []string {
	var res struct {
		TEs []struct {
			TE string `json:"trackedEntity"`
		} `json:"trackedEntities"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("%v: %.200s", err, b)
	}
	out := []string{}
	for _, x := range res.TEs {
		out = append(out, x.TE)
	}
	return out
}

func head(s []string) []string { return s[:min(3, len(s))] }

// buildQueryIndex writes the query tables from the index's objects: one row per
// enrollment with what a program's list filters and sorts on (the person's creation
// date copied in, so the list reads in index order without a join), and one row per
// attribute value, lowercased.
func buildQueryIndex(db *sql.DB, ix *tracker.Index) error {
	for _, s := range []string{
		`DROP TABLE IF EXISTS qi_enr`, `DROP TABLE IF EXISTS qi_attr`,
		`CREATE TABLE qi_enr(program TEXT, owner TEXT, status TEXT, te_created TEXT, te_uid TEXT, te_slot INTEGER, enr_slot INTEGER)`,
		`CREATE TABLE qi_attr(te_uid TEXT, attr TEXT, value TEXT)`,
	} {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	enr, err := tx.Prepare(`INSERT INTO qi_enr VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	attr, err := tx.Prepare(`INSERT INTO qi_attr VALUES(?,?,?)`)
	if err != nil {
		return err
	}
	for _, e := range ix.All(store.KindEnrollment) {
		te := ix.Get(e.TE)
		if te == nil {
			continue
		}
		if _, err := enr.Exec(e.Program, e.OwnerOrgUnit, tracker.Str(e.Data, "status"), tracker.Str(te.Data, "createdAt"),
			te.UID, te.Slot, e.Slot); err != nil {
			return err
		}
	}
	for _, te := range ix.All(store.KindTrackedEntity) {
		as, _ := te.Data["attributes"].([]any)
		for _, a := range as {
			m, _ := a.(tracker.M)
			if _, err := attr.Exec(te.UID, tracker.Str(m, "attribute"), strings.ToLower(fmt.Sprint(m["value"]))); err != nil {
				return err
			}
		}
	}
	for _, s := range []string{
		// Both directions: the tie-breaker is ascending either way, so one index cannot
		// serve both.
		`CREATE INDEX qi_enr_list ON qi_enr(program, owner, te_created, te_uid)`,
		`CREATE INDEX qi_enr_list_desc ON qi_enr(program, owner, te_created DESC, te_uid)`,
		`CREATE INDEX qi_attr_te ON qi_attr(te_uid, attr)`,
	} {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type sqlQueries struct {
	db      *sql.DB
	visible func(slot int64) bool
}

type attrLike struct{ attr, value string }

// list reads a program's people in creation order (newest first when desc), with
// visibility checked per row as the rows stream, and stops once the page is full
// unless the total is wanted.
func (q *sqlQueries) list(program, owner, status string, desc bool, offset, size int, total bool) ([]string, int, error) {
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	where, args := "program=? AND owner=?", []any{program, owner}
	if status != "" {
		where, args = where+" AND status=?", append(args, status)
	}
	// Creation dates tie in the generated data: uid breaks ties ascending, as the hub does.
	rows, err := q.db.Query(`SELECT te_uid, te_slot, enr_slot FROM qi_enr WHERE `+where+
		` ORDER BY te_created `+dir+`, te_uid ASC`, args...)
	if err != nil {
		return nil, 0, err
	}
	return q.collect(rows, offset, size, total)
}

// search is list with attribute filters, in the default order (creation, oldest first).
func (q *sqlQueries) search(program, owner string, filters []attrLike, size int) ([]string, int, error) {
	from, joinArgs := "qi_enr e", []any{}
	where, whereArgs := "e.program=? AND e.owner=?", []any{program, owner}
	for i, f := range filters {
		from += fmt.Sprintf(" JOIN qi_attr a%d ON a%d.te_uid=e.te_uid AND a%d.attr=?", i, i, i)
		joinArgs = append(joinArgs, f.attr)
		where += fmt.Sprintf(" AND a%d.value LIKE ?", i)
		whereArgs = append(whereArgs, "%"+f.value+"%")
	}
	rows, err := q.db.Query(`SELECT e.te_uid, e.te_slot, e.enr_slot FROM `+from+` WHERE `+where+
		` ORDER BY e.te_created ASC, e.te_uid ASC`, append(joinArgs, whereArgs...)...)
	if err != nil {
		return nil, 0, err
	}
	return q.collect(rows, 0, size, false)
}

func (q *sqlQueries) collect(rows *sql.Rows, offset, size int, total bool) ([]string, int, error) {
	defer rows.Close()
	out := []string{}
	seen := map[string]bool{}
	n := 0
	for rows.Next() {
		var uid string
		var teSlot, enrSlot int64
		if err := rows.Scan(&uid, &teSlot, &enrSlot); err != nil {
			return nil, 0, err
		}
		if seen[uid] || !q.visible(teSlot) || !q.visible(enrSlot) {
			continue
		}
		seen[uid] = true
		if n >= offset && len(out) < size {
			out = append(out, uid)
		}
		n++
		if !total && len(out) == size {
			break
		}
	}
	return out, n, rows.Err()
}
