package live

import (
	"encoding/json"
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

// setRetention sets facility-hub-config/retention on the fake central and the hub's
// copy of it.
func setRetention(e *tk.Env, v string) {
	e.T.Helper()
	if e.Fake.DataStore["facility-hub-config"] == nil {
		e.Fake.DataStore["facility-hub-config"] = map[string]json.RawMessage{}
	}
	e.Fake.DataStore["facility-hub-config"]["retention"] = json.RawMessage(v)
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
