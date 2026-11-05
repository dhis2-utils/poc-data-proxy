package tracker

import (
	"net/url"
	"regexp"
	"slices"
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
	explain := func(name, q string, args ...any) string {
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
		return plan
	}
	c := store.ObjectCols("")
	explain("get", `SELECT `+c+` FROM objects WHERE uid = ?`, "x")
	explain("enrollmentsOf", `SELECT `+c+` FROM objects WHERE te_uid = ? AND kind = 'enrollment'`, "x")
	explain("eventsOf", `SELECT `+c+` FROM objects WHERE enrollment_uid = ? AND kind = 'event'`, "x")
	explain("eventsOfTE", `SELECT `+c+` FROM objects WHERE te_uid = ? AND kind = 'event'`, "x")
	explain("relsOf", `SELECT `+c+` FROM objects WHERE rel_from = ? OR rel_to = ?`, "x", "x")
	explain("te_created update", `UPDATE objects INDEXED BY objects_te SET te_created = ? WHERE te_uid = ? AND kind = 'enrollment' AND te_created <> ?`, "a", "b", "c")
	explain("insert sub", `SELECT coalesce((SELECT created_at FROM objects WHERE uid = ? AND kind = 'trackedEntity'), '')`, "a")
	explain("changed", `SELECT iif(kind = 'trackedEntity', uid, te_uid), updated_at FROM objects WHERE updated_at >= ?`, "x")
	explain("written on the hub", `SELECT iif(kind = 'trackedEntity', uid, te_uid), '' FROM objects WHERE origin = 'local'`)
	explain("hub-written children", sqlHubChildren, "x")
	explain("newest central child", sqlCentralChildrenLive, "x", "o")
	explain("newest central child, deleted too", sqlCentralChildrenAll, "x", "o")
	explain("object states", `SELECT o.uid, o.updated_at, o.slot, EXISTS (SELECT 1 FROM objects s INDEXED BY objects_stubs WHERE s.uid = o.uid AND s.stub = 1) FROM objects o INDEXED BY objects_state WHERE o.uid IN (?,?)`, "a", "b")
	explain("slot origins", `SELECT slot, origin, program FROM objects WHERE slot IN (?,?)`, 1, 2)
	explain("enrolled, owned", sqlEnrolledOwned, "p", "o", store.SyncSynced)
	explain("enrolled, owned elsewhere", sqlEnrolledElsewhere, "p", "o", store.SyncSynced)
	for _, cond := range []string{"a.value_lc LIKE ? ESCAPE '\\'", "a.value_lc = ?"} {
		if p := explain("search candidates", "SELECT a.te_uid FROM te_attrs a WHERE a.attr = ? AND "+cond+" LIMIT ?", "a", "b", 1001); !strings.Contains(p, "COVERING INDEX te_attrs_lc") {
			t.Errorf("search candidates read te_attrs rows: %s", p)
		}
	}
	explain("unique", `SELECT count(*) FROM te_attrs WHERE attr = ? AND value = ? AND te_uid <> ?`, "a", "b", "c")
	explain("old program events (pruning)", `SELECT uid FROM objects WHERE kind = ? AND program = ? AND owner_org_unit = ? AND te_uid = ''
 AND sync_state = ? AND ((occurred_at <> '' AND occurred_at < ?) OR (occurred_at = '' AND created_at < ?))
 AND NOT EXISTS (SELECT 1 FROM outbox o JOIN outbox_objects oo ON oo.seq = o.seq WHERE o.state = ? AND oo.uid = objects.uid)`,
		"event", "p", "o", "synced", "2025-01-01", "2025-01-01", "pending")
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
		base := explain(name, sql, p.args...)
		if len(p.keys) > 0 && !strings.Contains(base, "TEMP B-TREE FOR ORDER BY") {
			// Reading on after a page (pagePositions) must keep the index's order when
			// the first page does: sorting what is left would cost the whole rest of
			// the program per page.
			vals := make([]string, len(p.keys))
			for i := range vals {
				vals[i] = "d"
			}
			cond, condArgs := afterConds(p, vals)
			on := p.sel + " AND " + cond + " ORDER BY " + p.order
			if got := explain(name+", read on", on, append(slices.Clone(p.args), condArgs...)...); strings.Contains(got, "TEMP B-TREE FOR ORDER BY") {
				t.Errorf("%s, read on: sorts instead of reading the index in order: %s", name, got)
			}
		}
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
	// A program's events, in the orders Android's download (creation) and the working
	// lists (event date, newest first) ask for, are read in the index's order: with
	// 150 000 events a sort per page cost 0.5 to 1.6 s (docs/PERFORMANCE.md).
	for _, c := range []struct {
		name  string
		order string
	}{{"ev program, Android's paging", ""}, {"ev program, working list", "occurredAt:desc"}, {"ev program, oldest first", "occurredAt:asc"}, {"ev program, by creation", "createdAt:desc"}} {
		q := url.Values{"program": {"PrAaaaaaaa1"}, "orgUnit": {ou}}
		if c.order != "" {
			q.Set("order", c.order)
		}
		p, err := ix.searchEvents(v, q, "PrAaaaaaaa1")
		if err != nil || p.order == "" {
			t.Fatalf("%s: %v, order %q", c.name, err, p.order)
		}
		if got := explain(c.name, p.sel+" ORDER BY "+p.order, p.args...); strings.Contains(got, "TEMP B-TREE") {
			t.Errorf("%s sorts: %s", c.name, got)
		}
		plan(c.name, "ev", q) // and reading on keeps the order
	}
}
