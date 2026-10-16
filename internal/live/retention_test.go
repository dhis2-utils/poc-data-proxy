package live

import (
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/tracker"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The catalog records which programs allow one enrollment ever.
func TestCatalogOnlyEnrollOnce(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.OnlyEnrollOnce = true
	e.Setup()
	if pm := e.App.Hub.Catalog().Programs[fc.Program]; pm == nil || !pm.OnlyEnrollOnce {
		t.Fatalf("onlyEnrollOnce not in the catalog: %+v", pm)
	}
}

// stubEnrollment turns a stored enrollment into a stub, as pruning does.
func stubEnrollment(t *testing.T, e *tk.Env, uid string) {
	t.Helper()
	ix := e.App.Hub.Index
	o := ix.Get(uid)
	if o == nil {
		t.Fatalf("%s not held", uid)
	}
	n := &tracker.Obj{UID: o.UID, Kind: o.Kind, TE: o.TE, Program: o.Program, OwnerOrgUnit: o.OwnerOrgUnit,
		UpdatedAt: o.UpdatedAt, Origin: o.Origin, SyncState: o.SyncState, Slot: o.Slot, Data: tracker.StubOf(ix.Full(o))}
	if err := e.App.Hub.Store.PutObject(e.App.Hub.Store.DB(), n.ToStore()); err != nil {
		t.Fatal(err)
	}
	ix.Forget()
}

// listed returns the values of key in body[coll].
func listed(body map[string]any, coll, key string) []string {
	var out []string
	for _, x := range asList(body[coll]) {
		if m, ok := x.(map[string]any); ok {
			out = append(out, tracker.Str(m, key))
		}
	}
	return out
}

// A stub is never read: not in lists, not under its person, not singly (needs
// connection, since central has it), not even with includeDeleted.
func TestStubHiddenFromReads(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Seed("RetTe000001", "RetEn000001", "Old", "NAT-9001", map[string]string{"RetEv000001": fc.Stage1})
	e.Setup()
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	stubEnrollment(t, e, "RetEn000001")
	e.Offline()
	for _, extra := range []string{"", "&includeDeleted=true"} {
		ens := nurse.JSON("GET", "/api/tracker/enrollments?program="+fc.Program+"&orgUnit="+fc.Facility+"&fields=enrollment"+extra, nil, 200)
		if slices.Contains(listed(ens, "enrollments", "enrollment"), "RetEn000001") {
			t.Errorf("a stub listed%s", extra)
		}
		tes := nurse.JSON("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnits="+fc.Facility+"&fields=trackedEntity"+extra, nil, 200)
		if slices.Contains(listed(tes, "trackedEntities", "trackedEntity"), "RetTe000001") {
			t.Errorf("a person listed in a program where only a stub is held%s", extra)
		}
	}
	te := nurse.JSON("GET", "/api/tracker/trackedEntities/RetTe000001?fields=enrollments[enrollment]", nil, 200)
	if slices.Contains(listed(te, "enrollments", "enrollment"), "RetEn000001") {
		t.Error("a stub under its person")
	}
	if st, body := nurse.Do("GET", "/api/tracker/enrollments/RetEn000001", nil); st != 503 {
		t.Errorf("a stub read singly: %d %s", st, body)
	}
}

// newEnrollment posts a new ACTIVE enrollment for te in the TB program.
func newEnrollment(c *tk.Client, uid, te string) (int, string) {
	st, b := c.Do("POST", "/api/tracker?async=false", map[string]any{"enrollments": []any{map[string]any{
		"enrollment": uid, "trackedEntity": te, "program": fc.Program, "orgUnit": fc.Facility,
		"status": "ACTIVE", "enrolledAt": "2026-10-16", "occurredAt": "2026-10-16"}}})
	return st, string(b)
}

// Offline, a second active enrollment in a program is refused (E1015), and so is any
// second enrollment in a program that allows one ever (E1016), even when the first is
// held only as a stub.
func TestEnrollmentRulesOffline(t *testing.T) {
	setup := func(t *testing.T, once bool, status string) (*tk.Env, *tk.Client) {
		e := tk.Start(t)
		e.NeedFake()
		e.Fake.OnlyEnrollOnce = once
		e.Fake.Seed("RetTe000002", "RetEn000002", "Active", "NAT-9002", nil)
		e.Fake.SetStatus("RetEn000002", status)
		e.Setup()
		nurse := e.Android(tk.RoleNurse)
		nurse.Do("GET", "/api/me?fields=id", nil)
		e.Sync()
		e.Offline()
		return e, nurse
	}
	t.Run("E1015", func(t *testing.T) {
		e, nurse := setup(t, false, "ACTIVE")
		if st, b := newEnrollment(nurse, "RetEn000003", "RetTe000002"); st != 409 || !strings.Contains(b, "E1015") {
			t.Errorf("a second active enrollment offline: %d %s", st, b)
		}
		// Online, central refuses it with the same code.
		e.Online()
		if st, b := newEnrollment(nurse, "RetEn000004", "RetTe000002"); st != 409 || !strings.Contains(b, "E1015") {
			t.Errorf("a second active enrollment online: %d %s", st, b)
		}
	})
	t.Run("E1016", func(t *testing.T) {
		e, nurse := setup(t, true, "COMPLETED")
		if st, b := newEnrollment(nurse, "RetEn000003", "RetTe000002"); st != 409 || !strings.Contains(b, "E1016") {
			t.Errorf("enrolling twice where once is allowed: %d %s", st, b)
		}
		stubEnrollment(t, e, "RetEn000002")
		if st, b := newEnrollment(nurse, "RetEn000003", "RetTe000002"); st != 409 || !strings.Contains(b, "E1016") {
			t.Errorf("enrolling twice where once is allowed, the first a stub: %d %s", st, b)
		}
	})
	t.Run("as central", func(t *testing.T) { enrollmentRulesAsCentral(t, false) })
	t.Run("as central, once", func(t *testing.T) { enrollmentRulesAsCentral(t, true) })
	t.Run("completed then new", func(t *testing.T) {
		e, nurse := setup(t, false, "COMPLETED")
		if st, b := newEnrollment(nurse, "RetEn000003", "RetTe000002"); st != 200 {
			t.Fatalf("a new enrollment after a completed one: %d %s", st, b)
		}
		e.Online()
		e.Sync()
		if e.CentralObject("enrollments", "RetEn000003") == nil {
			t.Error("the new enrollment did not reach central")
		}
	})
}

// setRetention sets facility-hub-config/retention on central and the hub's copy of it.
func setRetention(e *tk.Env, v string) {
	e.T.Helper()
	e.CentralSetRetention(v)
	if err := e.App.Hub.Store.SetState("retention", []byte(v)); err != nil && e.App.Hub.Store.Unlocked() {
		e.T.Fatal(err)
	}
}

// seedRetention seeds people in the TB program, each with one enrollment and event:
//   - RetTe000010: active, last changed two years ago (kept in full);
//   - RetTe000011: completed two years ago (old);
//   - RetTe000012: completed last month (kept in full);
//   - RetTe000013: completed two years ago, the person changed last month (listed by
//     the window's second listing, with the old enrollment under them).
func seedRetention(e *tk.Env) {
	now := e.App.Hub.CentralNow()
	ts := func(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000") }
	old, recent := ts(now.AddDate(-2, 0, 0)), ts(now.AddDate(0, -1, 0))
	for i, st := range []string{"ACTIVE", "COMPLETED", "COMPLETED", "COMPLETED"} {
		n := strconv.Itoa(10 + i)
		te, en, ev := "RetTe0000"+n, "RetEn0000"+n, "RetEv0000"+n
		e.Fake.Seed(te, en, "Person"+n, "NAT-90"+n, map[string]string{ev: fc.Stage1})
		e.Fake.SetStatus(en, st)
		e.Fake.Backdate(te, old)
	}
	e.Fake.Backdate("RetTe000012", recent)
	e.Fake.Backdate("RetTe000013", recent)
	e.Fake.Backdate("RetEn000013", old)
}

// heldAs is "full", "stub" or "" (not held).
func heldAs(e *tk.Env, uid string) string {
	o := e.App.Hub.Index.Get(uid)
	switch {
	case o == nil:
		return ""
	case o.Stub:
		return "stub"
	}
	return "full"
}

func expectHeld(t *testing.T, e *tk.Env, want map[string][]string) {
	t.Helper()
	for uid, ok := range want {
		if got := heldAs(e, uid); !slices.Contains(ok, got) {
			t.Errorf("%s held as %q, want one of %q", uid, got, ok)
		}
	}
}

var (
	full     = []string{"full"}
	stubOrNo = []string{"stub", ""}
	none     = []string{""}
)

// retentionEnv starts a hub with the retention seeds and window, synced for a nurse.
func retentionEnv(t *testing.T, window string) (*tk.Env, *tk.Client) {
	e := tk.Start(t)
	e.NeedFake()
	seedRetention(e)
	if window != "" {
		setRetention(e, window)
	}
	e.Setup()
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	return e, nurse
}

const window12 = `{"programs": {"` + fc.Program + `": 12}}`

// fullPull runs a sync cycle with a full pull.
func fullPull(e *tk.Env) {
	e.App.Hub.Cfg.FullPullInterval = time.Nanosecond
	e.Sync()
	e.App.Hub.Cfg.FullPullInterval = 7 * 24 * time.Hour
}

// With a window, the first pull leaves old closed enrollments and their events out, or
// keeps them as stubs; people stay.
func TestRetentionPullWindow(t *testing.T) {
	e, _ := retentionEnv(t, window12)
	expectHeld(t, e, map[string][]string{
		"RetEn000010": full, "RetEv000010": full,
		"RetEn000012": full, "RetEv000012": full,
		"RetTe000011": full, "RetEn000011": stubOrNo, "RetEv000011": none,
		"RetTe000013": full, "RetEn000013": {"stub"}, "RetEv000013": none,
	})
}

// A full pull after pruning fetches no one again for what it holds as stubs.
func TestNoRefetchAfterPrune(t *testing.T) {
	e, _ := retentionEnv(t, window12)
	before := map[string]int{}
	for _, te := range []string{"RetTe000011", "RetTe000013"} {
		before[te] = e.Fake.FetchCount(te)
	}
	fullPull(e)
	for te, n := range before {
		if got := e.Fake.FetchCount(te); got != n {
			t.Errorf("%s fetched again at a full pull: %d -> %d", te, n, got)
		}
	}
	expectHeld(t, e, map[string][]string{"RetEn000013": {"stub"}, "RetEv000013": none})
}

// A person whose only enrollment in a program is a stub is edited offline: the edit
// reaches central, and the stub stays a stub (never merged into a full enrollment).
func TestStubNeverMerges(t *testing.T) {
	e, nurse := retentionEnv(t, window12)
	e.Offline()
	nurse.JSON("POST", "/api/tracker?async=false", map[string]any{"trackedEntities": []any{map[string]any{
		"trackedEntity": "RetTe000013", "trackedEntityType": fc.TET, "orgUnit": fc.Facility,
		"attributes": []any{map[string]any{"attribute": fc.AttrFirstName, "value": "Renamed"}}}}}, 200)
	e.Online()
	e.Sync()
	e.Sync()
	b, _ := json.Marshal(e.CentralObject("trackedEntities", "RetTe000013"))
	if !strings.Contains(string(b), "Renamed") {
		t.Errorf("the edit did not reach central: %s", b)
	}
	expectHeld(t, e, map[string][]string{"RetEn000013": {"stub"}, "RetEv000013": none})
	if d := e.App.Hub.Index.Full(e.App.Hub.Index.Get("RetEn000013")); d["hubStub"] != true || d["events"] != nil {
		t.Errorf("the stub was merged: %v", d)
	}
}

// An enrollment reopened on central after it was stubbed comes back in full, with its
// events, at the next pull.
func TestReopenedEnrollmentReplacesStub(t *testing.T) {
	e, _ := retentionEnv(t, window12)
	expectHeld(t, e, map[string][]string{"RetEn000013": {"stub"}})
	e.Fake.SetStatus("RetEn000013", "ACTIVE")
	e.Fake.Backdate("RetEn000013", e.App.Hub.CentralNow().UTC().Format("2006-01-02T15:04:05.000"))
	e.Sync()
	expectHeld(t, e, map[string][]string{"RetEn000013": full, "RetEv000013": full})
}

// Without retention settings nothing changes: everything is held in full, and no
// listing asks for enrollment status.
func TestNoRetentionUnchanged(t *testing.T) {
	e, _ := retentionEnv(t, "")
	fullPull(e)
	for _, n := range []string{"10", "11", "12", "13"} {
		expectHeld(t, e, map[string][]string{"RetTe0000" + n: full, "RetEn0000" + n: full, "RetEv0000" + n: full})
	}
	for _, q := range e.Fake.TEListQueries() {
		if strings.Contains(q, "enrollmentStatus") {
			t.Errorf("a listing with enrollmentStatus: %s", q)
		}
	}
}

// prune runs tracker pruning and fails the test on error.
func prune(t *testing.T, e *tk.Env) (stubbed, deleted int) {
	t.Helper()
	s, d, err := e.App.Hub.PruneTracker()
	if err != nil {
		t.Fatal(err)
	}
	return s, d
}

// Pruning turns closed enrollments whose last change fell out of the window into
// stubs and deletes their events; a second run changes nothing.
func TestTrackerRetentionPrunes(t *testing.T) {
	e, _ := retentionEnv(t, "")
	setRetention(e, window12)
	if s, d := prune(t, e); s != 2 || d != 2 {
		t.Errorf("pruned: %d stubbed, %d deleted; want 2 and 2", s, d)
	}
	expectHeld(t, e, map[string][]string{
		"RetTe000010": full, "RetEn000010": full, "RetEv000010": full,
		"RetTe000011": full, "RetEn000011": {"stub"}, "RetEv000011": none,
		"RetTe000012": full, "RetEn000012": full, "RetEv000012": full,
		"RetTe000013": full, "RetEn000013": {"stub"}, "RetEv000013": none,
	})
	if d := e.App.Hub.Index.Full(e.App.Hub.Index.Get("RetEn000011")); d["hubStub"] != true || d["status"] != "COMPLETED" {
		t.Errorf("stub payload: %v", d)
	}
	if s, d := prune(t, e); s != 0 || d != 0 {
		t.Errorf("a second run pruned: %d stubbed, %d deleted", s, d)
	}
}

// With a window on the tracked entity type, people whose last change is old and who
// have nothing left in full go, with their stubs.
func TestTypeRetentionRemovesEntities(t *testing.T) {
	e, _ := retentionEnv(t, "")
	setRetention(e, `{"programs": {"`+fc.Program+`": 12}, "trackedEntityTypes": {"`+fc.TET+`": 12}}`)
	prune(t, e)
	expectHeld(t, e, map[string][]string{
		"RetTe000010": full, "RetEn000010": full, // an active enrollment
		"RetTe000011": none, "RetEn000011": none, "RetEv000011": none,
		"RetTe000012": full,                          // a recent enrollment
		"RetTe000013": full, "RetEn000013": {"stub"}, // changed last month
	})
}

// An edit waiting to be pushed keeps what it touches, and what that refers to, out of
// pruning.
func TestPruneKeepsReferenced(t *testing.T) {
	e, nurse := retentionEnv(t, "")
	e.Offline()
	nurse.JSON("POST", "/api/tracker?async=false", map[string]any{"events": []any{map[string]any{
		"event": "RetEv000011", "enrollment": "RetEn000011", "trackedEntity": "RetTe000011", "program": fc.Program,
		"programStage": fc.Stage1, "orgUnit": fc.Facility, "status": "ACTIVE", "occurredAt": "2024-10-01", "dataValues": []any{}}}}, 200)
	setRetention(e, `{"programs": {"`+fc.Program+`": 12}, "trackedEntityTypes": {"`+fc.TET+`": 12}}`)
	prune(t, e)
	expectHeld(t, e, map[string][]string{"RetTe000011": full, "RetEn000011": full, "RetEv000011": full})
}

// errorReports returns the error reports of an import answer, as "code: message".
func errorReports(body []byte) []string {
	var r struct {
		ValidationReport struct {
			ErrorReports []struct{ Message, ErrorCode string } `json:"errorReports"`
		} `json:"validationReport"`
	}
	json.Unmarshal(body, &r)
	var out []string
	for _, x := range r.ValidationReport.ErrorReports {
		out = append(out, x.ErrorCode+": "+x.Message)
	}
	return out
}

// enrollmentRulesAsCentral checks that the hub refuses offline what central refuses,
// with the same codes and messages: a second active enrollment in a program (E1015);
// with once, in a program that allows one enrollment only, a second one completed
// (E1016 alone) and a second one active (both rules apply: which code central gives).
// Live, the primary program's onlyEnrollOnce is set as the case needs for the test,
// and restored after.
func enrollmentRulesAsCentral(t *testing.T, once bool) {
	e := tk.Start(t)
	program := e.M.Primary().ID
	if e.Fake != nil {
		e.Fake.OnlyEnrollOnce = once
		e.Fake.Seed("RetTe000005", "RetEn000005", "Active", "NAT-9005", nil)
	} else {
		var cur struct{ OnlyEnrollOnce bool }
		if st, b, err := e.Central.Do("GET", "/api/programs/"+program+"?fields=onlyEnrollOnce", nil); err != nil || st != 200 {
			t.Fatalf("reading %s: %d %v %.300s", program, st, err, b)
		} else {
			json.Unmarshal(b, &cur)
		}
		patch := func(v bool) {
			st, b, err := e.Central.Do("PATCH", "/api/programs/"+program,
				[]any{map[string]any{"op": "replace", "path": "/onlyEnrollOnce", "value": v}})
			if err != nil || st >= 300 {
				t.Fatalf("setting onlyEnrollOnce=%v on %s: %d %v %.500s", v, program, st, err, b)
			}
		}
		if cur.OnlyEnrollOnce != once {
			patch(once)
			t.Cleanup(func() { patch(cur.OnlyEnrollOnce) })
		}
	}
	e.Setup()
	if pm := e.App.Hub.Catalog().Programs[program]; pm == nil || pm.OnlyEnrollOnce != once {
		t.Fatalf("catalog: %s onlyEnrollOnce %+v, want %v", program, pm, once)
	}
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	body := nurse.JSON("GET", "/api/tracker/trackedEntities?program="+program+"&orgUnits="+e.M.Facility.ID+
		"&enrollmentStatus=ACTIVE&pageSize=1&fields=trackedEntity,attributes[attribute,value]", nil, 200)
	tes := asList(body["trackedEntities"])
	if len(tes) == 0 {
		t.Skipf("no one with an active enrollment in %s at the facility", program)
	}
	te := tes[0].(map[string]any)
	statuses := []string{"ACTIVE"}
	if once {
		statuses = []string{"COMPLETED", "ACTIVE"}
	}
	var payloads []map[string]any
	for _, st := range statuses {
		payloads = append(payloads, map[string]any{"enrollments": []any{map[string]any{
			"enrollment": tk.UID(), "trackedEntity": te["trackedEntity"], "program": program, "orgUnit": e.M.Facility.ID,
			"status": st, "enrolledAt": "2026-10-16", "occurredAt": "2026-10-16", "attributes": orEmptyList(te["attributes"])}}})
	}
	e.Sync()
	e.Offline()
	offline := make([][]string, len(payloads))
	for i, p := range payloads {
		st, b := nurse.Do("POST", "/api/tracker?async=false", p)
		if offline[i] = errorReports(b); st != 409 || len(offline[i]) == 0 {
			t.Errorf("a second %s enrollment offline: %d %.800s", statuses[i], st, b)
		}
	}
	e.Online()
	for i, p := range payloads {
		st, b := nurse.Do("POST", "/api/tracker?async=false", p)
		if st == 200 {
			e.CentralDelete("enrollments", p["enrollments"].([]any)[0].(map[string]any)["enrollment"].(string))
		}
		// Both codes apply at times; central's order of them varies between runs.
		got := errorReports(b)
		slices.Sort(got)
		slices.Sort(offline[i])
		t.Logf("central, a second %s enrollment (once=%v): %d %q", statuses[i], once, st, got)
		if st != 409 || !slices.Equal(got, offline[i]) {
			t.Errorf("a second %s enrollment: the hub says %q, central %d %q", statuses[i], offline[i], st, got)
		}
	}
}

func orEmptyList(v any) []any {
	if l := asList(v); l != nil {
		return l
	}
	return []any{}
}

// With a window on the primary program, the people the hub lists offline for it are
// those central lists for the same user with an enrollment in the program that is
// active or changed since the cut-off; central accepts both of the full pull's
// listings, and the active one's time is recorded.
func TestRetentionWindowLive(t *testing.T) {
	e := tk.Start(t)
	months := 1
	if e.Fake != nil {
		seedRetention(e)
		months = 12
	}
	program := e.M.Primary().ID
	e.CentralSetRetention(`{"programs": {"` + program + `": ` + strconv.Itoa(months) + `}}`)
	e.Setup()
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	cutoff, ok := e.App.Hub.Retention().Cutoff(program, e.App.Hub.CentralNow())
	if !ok {
		t.Fatal("the hub did not read the retention setting")
	}
	// Central as the nurse, directly (reads through the hub would be stored).
	c := tk.NewCentral(e.M.CentralURL, e.M.Users[tk.RoleNurse].Username, e.M.Password)
	api := e.App.Hub.API()
	want := map[string]bool{}
	for i, path := range []string{api.ActivePath(e.M.Facility.ID, program, 1, 10000), api.ListPath(e.M.Facility.ID, program, cutoff, 1, 10000)} {
		// The pull's own listings, with the fields this check reads.
		u, _ := url.Parse(path)
		q := u.Query()
		q.Set("fields", "trackedEntity,enrollments[program,status,updatedAt,deleted]")
		u.RawQuery = q.Encode()
		path = u.String()
		start := time.Now()
		st, b, err := c.Do("GET", path, nil)
		if err != nil || st != 200 {
			t.Fatalf("central refused a listing: %s: %d %v %.500s", path, st, err, b)
		}
		if i == 0 {
			t.Logf("active-enrollment listing on central: %v, %d bytes", time.Since(start), len(b))
		}
		var body map[string]any
		json.Unmarshal(b, &body)
		for _, x := range asList(body["trackedEntities"]) {
			te := x.(map[string]any)
			for _, en := range asList(te["enrollments"]) {
				m := en.(map[string]any)
				if m["program"] == program && m["deleted"] != true &&
					(m["status"] == "ACTIVE" || tracker.Str(m, "updatedAt") >= cutoff) {
					want[tracker.Str(te, "trackedEntity")] = true
				}
			}
		}
	}
	e.Offline()
	q := url.Values{"program": {program}, "orgUnits": {e.M.Facility.ID}, "fields": {"trackedEntity"}, "paging": {"false"}}
	got := listed(nurse.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, 200), "trackedEntities", "trackedEntity")
	for _, te := range got {
		if !want[te] {
			t.Errorf("listed offline, not by central's window: %s", te)
		}
		delete(want, te)
	}
	for te := range want {
		t.Errorf("listed by central's window, not offline: %s", te)
	}
	t.Logf("%d people in %s with a %d-month window", len(got), program, months)
}
