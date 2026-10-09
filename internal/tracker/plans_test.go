package tracker

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/store"
)

// The queries the index runs read through indexes, never a whole table: without
// statistics, SQLite's planner chose a list index for a person's enrollments and a
// table scan for an enrollment's events (a partial index it could not prove usable),
// which made storing and rendering cost the facility's size.
func TestQueryPlansUseIndexes(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	v := &Viewer{UserID: 1, Access: &access.List{}, Facility: "OuAaaaaaaa1", SearchScope: true}
	scan := regexp.MustCompile(`SCAN (objects|te_attrs|[a-z]+)( \||$)`)
	explain := func(name, q string, args ...any) {
		rows, err := s.DB().Query("EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var lines []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			rows.Scan(&id, &parent, &notused, &detail)
			lines = append(lines, detail)
		}
		rows.Close()
		plan := strings.Join(lines, " | ")
		if scan.MatchString(plan) {
			t.Errorf("%s scans a table: %s", name, plan)
		}
	}
	c := store.ObjectCols("")
	explain("get", `SELECT `+c+` FROM objects WHERE uid = ?`, "x")
	explain("enrollmentsOf", `SELECT `+c+` FROM objects WHERE te_uid = ? AND kind = 'enrollment'`, "x")
	explain("eventsOf", `SELECT `+c+` FROM objects WHERE enrollment_uid = ? AND kind = 'event'`, "x")
	explain("eventsOfTE", `SELECT `+c+` FROM objects WHERE te_uid = ? AND kind = 'event'`, "x")
	explain("relsOf", `SELECT `+c+` FROM objects WHERE rel_from = ? OR rel_to = ?`, "x", "x")
	explain("te_created update", `UPDATE objects INDEXED BY objects_te SET te_created = ? WHERE te_uid = ? AND kind = 'enrollment' AND te_created <> ?`, "a", "b", "c")
	explain("insert sub", `SELECT coalesce((SELECT created_at FROM objects WHERE uid = ? AND kind = 'trackedEntity'), '')`, "a")
	explain("changed", `SELECT iif(kind = 'trackedEntity', uid, te_uid) FROM objects WHERE updated_at >= ? UNION SELECT iif(kind = 'trackedEntity', uid, te_uid) FROM objects WHERE origin = 'local'`, "x")
	explain("object states", `SELECT uid, updated_at, slot FROM objects INDEXED BY objects_state WHERE uid IN (?,?)`, "a", "b")
	explain("slot origins", `SELECT slot, origin, program FROM objects WHERE slot IN (?,?)`, 1, 2)
	explain("unique", `SELECT count(*) FROM te_attrs WHERE attr = ? AND value = ? AND te_uid <> ?`, "a", "b", "c")
	explain("enrolledTEs", `SELECT DISTINCT te_uid FROM objects WHERE kind = 'enrollment' AND program = ? AND owner_org_unit = ? AND sync_state = ?`, "p", "o", "s")
	plan := func(name, coll string, q url.Values) {
		var p *plan
		var err *APIError
		switch coll {
		case "te":
			p, err = ix.searchTEs(v, q, q.Get("program"))
		case "enr":
			p, err = ix.searchEnrollments(v, q, q.Get("program"))
		case "ev":
			p, err = ix.searchEvents(v, q, q.Get("program"))
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		sql := p.sel
		if p.order != "" {
			sql += " ORDER BY " + p.order
		}
		explain(name, sql, p.args...)
	}
	ou := "OuAaaaaaaa1"
	plan("te list desc", "te", url.Values{"program": {"PrAaaaaaaa1"}, "orgUnits": {ou}, "order": {"createdAt:desc"}})
	plan("te list asc", "te", url.Values{"program": {"PrAaaaaaaa1"}, "orgUnits": {ou}})
	plan("te list status", "te", url.Values{"program": {"PrAaaaaaaa1"}, "orgUnits": {ou}, "enrollmentStatus": {"ACTIVE"}, "order": {"createdAt:desc"}})
	plan("te name", "te", url.Values{"program": {"PrAaaaaaaa1"}, "orgUnits": {ou}, "filter": {"AtAaaaaaaa1:like:am"}})
	plan("te eq", "te", url.Values{"program": {"PrAaaaaaaa1"}, "orgUnits": {ou}, "filter": {"AtAaaaaaaa1:eq:am"}})
	plan("te updated", "te", url.Values{"program": {"PrAaaaaaaa1"}, "orgUnits": {ou}, "updatedAfter": {"2024-01-01"}})
	plan("te tet", "te", url.Values{"trackedEntityType": {"T"}, "orgUnits": {ou}, "filter": {"AtAaaaaaaa1:eq:am"}})
	plan("te uids", "te", url.Values{"trackedEntities": {"TeAaaaaaaa1,TeAaaaaaaa2"}, "orgUnits": {ou}})
	plan("enr by te", "enr", url.Values{"trackedEntity": {"TeAaaaaaaa1"}})
	plan("enr by program", "enr", url.Values{"program": {"PrAaaaaaaa1"}, "orgUnits": {ou}})
	plan("ev by enrollments", "ev", url.Values{"enrollments": {"EnAaaaaaaa1"}})
	plan("ev by program", "ev", url.Values{"program": {"PrAaaaaaaa1"}, "orgUnits": {ou}, "order": {"createdAt:desc"}})
	plan("ev by stage+status", "ev", url.Values{"program": {"PrAaaaaaaa1"}, "programStage": {""}, "orgUnits": {ou}, "enrollmentStatus": {"ACTIVE"}})
}
