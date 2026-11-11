package live

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// The retention table is read from central's dataStore at the catalog refresh.
func TestRetentionTableRead(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	e.PutSigned("facility-hub-config/retention", `{"defaultMonths": 24, "programs": {"X": 6}}`)
	if err := e.App.Hub.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := e.App.Hub.Retention()
	if r.Months("X") != 6 || r.Months("Y") != 24 {
		t.Fatalf("retention: %+v", r)
	}
}

// The catalog holds the facility's event programs and the option combinations of
// attribute category combinations.
func TestEventProgramsInCatalog(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	cat := e.App.Hub.Catalog()
	ev, donor := cat.Programs[fc.EventProgram], cat.Programs[fc.EventDonorProgram]
	if ev == nil || !ev.Event || ev.TET != "" || len(ev.Stages) != 1 || ev.AttributeCombo {
		t.Fatalf("event program: %+v", ev)
	}
	if donor == nil || !donor.AttributeCombo || donor.CategoryCombo != fc.AggDonorCC {
		t.Fatalf("donor event program: %+v", donor)
	}
	if opts := cat.AOCs[fc.AggDonorAOCA]; len(opts) != 1 || opts[0] != fc.AggDonorOptA {
		t.Fatalf("option combinations: %+v", cat.AOCs)
	}
	if tb := cat.Programs[fc.Program]; tb == nil || tb.Event {
		t.Fatalf("tracker program: %+v", tb)
	}
}

// The access refresh reads which category options of attribute category combinations
// each user may write.
func TestCategoryOptionWriteAccess(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	for _, c := range []struct {
		user string
		a, b bool
	}{{"nurse_a", true, true}, {"clerk_c", false, true}} {
		cl := e.Web(roleOf(e, c.user))
		cl.Login()
		u, _ := e.App.Hub.Store.UserByName(c.user)
		acc := e.App.Hub.Access(u.ID)
		if acc.CategoryOptions[fc.AggDonorOptA] != c.a || acc.CategoryOptions[fc.AggDonorOptB] != c.b {
			t.Errorf("%s: %+v", c.user, acc.CategoryOptions)
		}
	}
}

func roleOf(e *tk.Env, username string) string {
	for role, u := range e.M.Users {
		if u.Username == username {
			return role
		}
	}
	e.T.Fatalf("no role for %s", username)
	return ""
}

func eventPath(e *tk.Env, program string, extra url.Values) string {
	q := url.Values{"program": {program}, "orgUnit": {e.M.Facility.ID}, "fields": {"event,programStage,occurredAt,attributeOptionCombo,status"}}
	for k, v := range extra {
		q[k] = v
	}
	return "/api/tracker/events?" + q.Encode()
}

func eventUIDs(body map[string]any) []string {
	var out []string
	for _, x := range body["events"].([]any) {
		out = append(out, x.(map[string]any)["event"].(string))
	}
	return out
}

// Event programs are pulled and answered offline: working lists, single events and
// Android's download.
func TestEventProgramOffline(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.SeedProgramEvent("FakeEv00001", fc.EventProgram, fc.EventStage, "2026-10-01", fc.DefaultAOC)
	e.Setup()
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	e.Offline()
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventProgram, nil), nil, 200)); len(got) != 1 || got[0] != "FakeEv00001" {
		t.Fatalf("working list offline: %v", got)
	}
	android := url.Values{"orgUnitMode": {"DESCENDANTS"}, "includeDeleted": {"true"}, "paging": {"true"}, "page": {"1"}, "pageSize": {"50"}}
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventProgram, android), nil, 200)); len(got) != 1 {
		t.Fatalf("Android's download offline: %v", got)
	}
	nurse.JSON("GET", "/api/tracker/events/FakeEv00001?fields=event", nil, 200)
}

// Review focus 1: an event deleted on central disappears offline after the next
// incremental pull.
func TestEventProgramDeletedOnCentral(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.SeedProgramEvent("FakeEv00002", fc.EventProgram, fc.EventStage, "2026-10-01", fc.DefaultAOC)
	e.Setup()
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	e.Fake.DeleteProgramEvent("FakeEv00002")
	e.Sync()
	e.Offline()
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventProgram, nil), nil, 200)); len(got) != 0 {
		t.Fatalf("deleted on central, still offline: %v", got)
	}
}

// Review focus 5: where an attribute combination decides, each user sees only events
// under their options (listed per user).
func TestEventProgramPerUserListing(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.SeedProgramEvent("FakeEvD0001", fc.EventDonorProgram, fc.EventDonorStage, "2026-10-01", fc.AggDonorAOCA)
	e.Fake.SeedProgramEvent("FakeEvD0002", fc.EventDonorProgram, fc.EventDonorStage, "2026-10-01", fc.AggDonorAOCB)
	e.Setup()
	nurse, clerk := e.Android(tk.RoleNurse), e.Android(tk.RoleClerk)
	nurse.Do("GET", "/api/me?fields=id", nil)
	clerk.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	e.Offline()
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventDonorProgram, nil), nil, 200)); len(got) != 2 {
		t.Errorf("nurse (options A and B): %v", got)
	}
	if got := eventUIDs(clerk.JSON("GET", eventPath(e, fc.EventDonorProgram, nil), nil, 200)); len(got) != 1 || got[0] != "FakeEvD0002" {
		t.Errorf("clerk (option B only): %v", got)
	}
}

// Events older than their program's retention are not pulled, and are pruned once they
// fall out of it.
func TestEventRetention(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	old := e.App.Hub.CentralNow().AddDate(-2, 0, 0).Format(time.DateOnly)
	recent := e.App.Hub.CentralNow().AddDate(0, -1, 0).Format(time.DateOnly)
	e.Fake.SeedProgramEvent("FakeEvOld01", fc.EventProgram, fc.EventStage, old, fc.DefaultAOC)
	e.Fake.SeedProgramEvent("FakeEvNew01", fc.EventProgram, fc.EventStage, recent, fc.DefaultAOC)
	e.Setup() // keeps everything: no retention yet
	if e.App.Hub.Index.Get("FakeEvOld01") == nil {
		t.Fatal("without retention the old event is held")
	}
	e.PutSigned("facility-hub-config/retention", `{"programs": {"`+fc.EventProgram+`": 12}}`)
	e.App.Hub.RefreshCatalog(context.Background())
	e.Sync()
	if e.App.Hub.Index.Get("FakeEvOld01") != nil || e.App.Hub.Index.Get("FakeEvNew01") == nil {
		t.Fatal("pruning: the old event must go, the recent one stay")
	}
}

// Review focus 2: an event with pending edits is kept past its retention.
func TestEventRetentionKeepsPending(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	old := e.App.Hub.CentralNow().AddDate(-2, 0, 0).Format(time.DateOnly)
	e.Fake.SeedProgramEvent("FakeEvOld02", fc.EventProgram, fc.EventStage, old, fc.DefaultAOC)
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	e.Offline()
	nurse.JSON("POST", tk.SyncImport, map[string]any{"events": []any{map[string]any{"event": "FakeEvOld02",
		"program": fc.EventProgram, "programStage": fc.EventStage, "orgUnit": e.M.Facility.ID, "occurredAt": old,
		"status": "COMPLETED"}}}, 200)
	e.App.Hub.Store.SetState("retention", []byte(`{"defaultMonths": 12}`)) // as a refresh would store it
	n, err := e.App.Hub.PruneEvents()
	if err != nil || n != 0 || e.App.Hub.Index.Get("FakeEvOld02") == nil {
		t.Fatalf("pruned %d (%v) with an edit pending", n, err)
	}
}

// Events of event programs are captured offline and pushed as their author's.
func TestEventProgramWriteOffline(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	e.Offline()
	ev := tk.UID()
	nurse.JSON("POST", tk.SyncImport, map[string]any{"events": []any{map[string]any{"event": ev,
		"program": fc.EventProgram, "programStage": fc.EventStage, "orgUnit": e.M.Facility.ID,
		"occurredAt": e.App.Hub.CentralNow().Format(time.DateOnly), "status": "ACTIVE"}}}, 200)
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventProgram, nil), nil, 200)); !slices.Contains(got, ev) {
		t.Fatalf("offline list after capture: %v", got)
	}
	e.Online()
	e.Sync()
	if e.CentralObject("events", ev) == nil {
		t.Fatal("not on central after the push")
	}
}

// Review focus 3: an attribute option combination is written offline only by users who
// may write each of its options.
func TestAttributeOptionWriteOffline(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	nurse, clerk := e.Web(tk.RoleNurse), e.Web(tk.RoleClerk)
	nurse.Login()
	clerk.Login()
	e.Sync()
	e.Offline()
	event := func(aoc string) map[string]any {
		return map[string]any{"events": []any{map[string]any{"event": tk.UID(), "program": fc.EventDonorProgram,
			"programStage": fc.EventDonorStage, "orgUnit": e.M.Facility.ID, "attributeOptionCombo": aoc,
			"occurredAt": e.App.Hub.CentralNow().Format(time.DateOnly), "status": "ACTIVE"}}}
	}
	nurse.JSON("POST", tk.SyncImport, event(fc.AggDonorAOCA), 200)
	if st, b := clerk.Do("POST", tk.SyncImport, event(fc.AggDonorAOCA)); st != 409 || !strings.Contains(string(b), "E1099") {
		t.Fatalf("clerk with option A offline: %d %s", st, b)
	}
	clerk.JSON("POST", tk.SyncImport, event(fc.AggDonorAOCB), 200)
	if st, b := clerk.Do("POST", tk.SyncImport, event("NoSuchAoc01")); st != 409 || !strings.Contains(string(b), "E1055") {
		t.Fatalf("an option combination of another combination: %d %s", st, b)
	}
	e.Online()
	e.Sync()
	if n := e.App.Hub.Store.CountIssues(); n != 0 {
		t.Fatalf("%d issues: central refused what the hub accepted", n)
	}
}

// Review fix: an event written under an attribute option combination, offline or online
// through the hub, is not shown to a user who may not read its option (clerk_c reads
// donor option B only).
func TestAttributeOptionReadOffline(t *testing.T) {
	for _, online := range []bool{false, true} {
		e := tk.Start(t)
		e.NeedFake()
		e.Setup()
		nurse, clerk := e.Web(tk.RoleNurse), e.Web(tk.RoleClerk)
		nurse.Login()
		clerk.Login()
		e.Sync()
		if !online {
			e.Offline()
		}
		ev := tk.UID()
		nurse.JSON("POST", tk.SyncImport, map[string]any{"events": []any{map[string]any{"event": ev,
			"program": fc.EventDonorProgram, "programStage": fc.EventDonorStage, "orgUnit": e.M.Facility.ID,
			"attributeOptionCombo": fc.AggDonorAOCA, "occurredAt": e.App.Hub.CentralNow().Format(time.DateOnly),
			"status": "ACTIVE"}}}, 200)
		if online {
			e.Sync()
			e.Offline()
		}
		if got := eventUIDs(clerk.JSON("GET", eventPath(e, fc.EventDonorProgram, nil), nil, 200)); slices.Contains(got, ev) {
			t.Fatalf("online=%v: the clerk sees an event under an option she may not read", online)
		}
		if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventDonorProgram, nil), nil, 200)); !slices.Contains(got, ev) {
			t.Fatalf("online=%v: the nurse does not see her own event", online)
		}
	}
}

// Review fix: with retention set, a full pull does not hide an old event with a pending
// edit from its author.
func TestEventRetentionPendingVisibleToAuthor(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	old := e.App.Hub.CentralNow().AddDate(-2, 0, 0).Format(time.DateOnly)
	e.Fake.SeedProgramEvent("FakeEvOld03", fc.EventProgram, fc.EventStage, old, fc.DefaultAOC)
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	e.Offline()
	nurse.JSON("POST", tk.SyncImport, map[string]any{"events": []any{map[string]any{"event": "FakeEvOld03",
		"program": fc.EventProgram, "programStage": fc.EventStage, "orgUnit": e.M.Facility.ID, "occurredAt": old,
		"status": "COMPLETED"}}}, 200)
	e.Online()
	e.Fake.AggFail = map[string]int{"/api/tracker": 503} // the push fails; pulls go on
	e.App.Hub.Store.SetState("retention", []byte(`{"defaultMonths": 12}`))
	e.App.Hub.Cfg.FullPullInterval = time.Nanosecond // a full pull now
	e.App.Worker.Cycle(context.Background())
	e.Fake.AggFail = nil
	e.App.Hub.Cfg.FullPullInterval = 7 * 24 * time.Hour
	e.App.Hub.Store.SetState("retention", []byte(`{}`))
	e.Offline()
	if got := eventUIDs(nurse.JSON("GET", eventPath(e, fc.EventProgram, nil), nil, 200)); !slices.Contains(got, "FakeEvOld03") {
		t.Fatalf("the author no longer sees her pending edit: %v", got)
	}
}

// Review fix: an old event edited offline is pruned once its edit is pushed (it is a
// local object then, not central's).
func TestEventRetentionPrunesPushedEdits(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	old := e.App.Hub.CentralNow().AddDate(-2, 0, 0).Format(time.DateOnly)
	e.Fake.SeedProgramEvent("FakeEvOld04", fc.EventProgram, fc.EventStage, old, fc.DefaultAOC)
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	e.Offline()
	nurse.JSON("POST", tk.SyncImport, map[string]any{"events": []any{map[string]any{"event": "FakeEvOld04",
		"program": fc.EventProgram, "programStage": fc.EventStage, "orgUnit": e.M.Facility.ID, "occurredAt": old,
		"status": "COMPLETED"}}}, 200)
	e.PutSigned("facility-hub-config/retention", `{"defaultMonths": 12}`) // the pull no longer lists it
	e.Online()
	e.App.Hub.RefreshCatalog(context.Background())
	e.Sync() // pushed (and pruned at the cycle's end)
	if _, err := e.App.Hub.PruneEvents(); err != nil || e.App.Hub.Index.Get("FakeEvOld04") != nil {
		t.Fatalf("an old event edited offline is still held after its push (%v)", err)
	}
}

// Deletions on central are kept as tombstones: offline, lists and single reads leave
// them out, and Android's download (includeDeleted=true) gets them, deleted, so a
// device drops what it downloaded before. Editing one is refused with central's code.
// For an event program's event and a tracker event, deleted on central.
func TestTombstonesOffline(t *testing.T) {
	e := tk.Start(t)
	if e.Fake != nil {
		e.Fake.SeedProgramEvent("FakeEvDel01", fc.EventProgram, fc.EventStage, "2026-10-01", fc.DefaultAOC)
	}
	e.Setup()
	if len(e.M.EventPrograms) == 0 {
		t.Skip("no event program at the facility")
	}
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	// One event of each kind the nurse sees, read online from central.
	pick := func(program string) map[string]any {
		t.Helper()
		body := nurse.JSON("GET", eventPath(e, program, url.Values{"fields": {"event,programStage"},
			"pageSize": {"1"}, "order": {"createdAt:asc"}}), nil, 200)
		evs := body["events"].([]any)
		if len(evs) == 0 {
			t.Skipf("no event of %s at the facility", program)
		}
		return evs[0].(map[string]any)
	}
	evProgram, trProgram := e.M.EventPrograms[0], e.M.Programs[0]
	single := pick(evProgram.ID)
	var tracked map[string]any // a tracker event, with its person
	tes := nurse.JSON("GET", "/api/tracker/trackedEntities?program="+trProgram.ID+"&orgUnits="+e.M.Facility.ID+
		"&fields=trackedEntity,enrollments[events[event,programStage]]&pageSize=20", nil, 200)
	for _, x := range tes["trackedEntities"].([]any) {
		te := x.(map[string]any)
		for _, en := range asList(te["enrollments"]) {
			for _, ev := range asList(en.(map[string]any)["events"]) {
				if tracked == nil {
					tracked = ev.(map[string]any)
					tracked["trackedEntity"] = te["trackedEntity"]
				}
			}
		}
	}
	if tracked == nil {
		t.Skipf("no event of %s at the facility", trProgram.ID)
	}
	e.Sync()
	e.CentralDelete("events", single["event"].(string))
	e.CentralDelete("events", tracked["event"].(string))
	e.Sync()
	e.Offline()
	withDeleted := url.Values{"includeDeleted": {"true"}, "fields": {"event,deleted"}, "paging": {"false"}}
	check := func(program, ev string) {
		t.Helper()
		if got := eventUIDs(nurse.JSON("GET", eventPath(e, program, url.Values{"paging": {"false"}}), nil, 200)); slices.Contains(got, ev) {
			t.Errorf("%s: a deleted event in a list", program)
		}
		body := nurse.JSON("GET", eventPath(e, program, withDeleted), nil, 200)
		found := false
		for _, x := range body["events"].([]any) {
			m := x.(map[string]any)
			if m["event"] == ev {
				found = m["deleted"] == true
			}
		}
		if !found {
			t.Errorf("%s: includeDeleted=true does not return %s as deleted", program, ev)
		}
		if st, _ := nurse.Do("GET", "/api/tracker/events/"+ev+"?fields=event", nil); st != 404 {
			t.Errorf("%s: a deleted event read singly: %d", program, st)
		}
	}
	check(evProgram.ID, single["event"].(string))
	check(trProgram.ID, tracked["event"].(string))
	te := nurse.JSON("GET", "/api/tracker/trackedEntities/"+tracked["trackedEntity"].(string)+"?program="+trProgram.ID+
		"&fields=enrollments[events[event]]", nil, 200)
	if b, _ := json.Marshal(te); strings.Contains(string(b), tracked["event"].(string)) {
		t.Errorf("a deleted event among a person's events: %s", b)
	}
	// Editing what central deleted is refused with central's code.
	ev := map[string]any{"event": single["event"], "program": evProgram.ID, "programStage": single["programStage"],
		"orgUnit": e.M.Facility.ID, "occurredAt": "2026-10-02", "status": "ACTIVE", "dataValues": []any{}}
	st, body := nurse.Do("POST", "/api/tracker?async=false", map[string]any{"events": []any{ev}})
	if st != 409 || !strings.Contains(string(body), "E1082") {
		t.Errorf("editing a deleted event offline: %d %s", st, body)
	}
}

// The pull of people enrolled in nothing (by tracked entity type) leaves out
// includeDeleted: on 2.42 central answers that combination with broken SQL (E7145), and
// such people were missing offline.
func TestTypePullWithoutIncludeDeleted(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.SeedPerson("FakeTeNoEnr", "Unenrolled")
	e.Setup()
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	e.Offline()
	body := nurse.JSON("GET", "/api/tracker/trackedEntities?trackedEntityType="+fc.TET+"&orgUnits="+e.M.Facility.ID+
		"&fields=trackedEntity", nil, 200)
	if b, _ := json.Marshal(body); !strings.Contains(string(b), "FakeTeNoEnr") {
		t.Errorf("a person enrolled in nothing is missing offline: %s", b)
	}
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// Review minor: a relationship with an event-program event that retention prunes goes
// with it.
func TestEventRetentionRemovesRelationships(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	old := e.App.Hub.CentralNow().AddDate(-2, 0, 0).Format(time.DateOnly)
	e.Fake.SeedProgramEvent("FakeEvOld04", fc.EventProgram, fc.EventStage, old, fc.DefaultAOC)
	e.Setup()
	ev := e.App.Hub.Index.Get("FakeEvOld04")
	if ev == nil {
		t.Fatal("the old event is not held")
	}
	rel := &tracker.Obj{UID: "FakeRel0004", Kind: "relationship", OwnerOrgUnit: ev.OwnerOrgUnit, Origin: ev.Origin,
		SyncState: ev.SyncState, Slot: ev.Slot + 100000, UpdatedAt: ev.UpdatedAt, Data: tracker.M{"relationship": "FakeRel0004",
			"from": tracker.M{"event": tracker.M{"event": "FakeEvOld04"}}, "to": tracker.M{"trackedEntity": tracker.M{"trackedEntity": "SeedTe00001"}}}}
	if err := e.App.Hub.Store.PutObject(e.App.Hub.Store.DB(), rel.ToStore()); err != nil {
		t.Fatal(err)
	}
	e.App.Hub.Store.SetState("retention", []byte(`{"programs": {"`+fc.EventProgram+`": 12}}`))
	if _, err := e.App.Hub.PruneEvents(); err != nil {
		t.Fatal(err)
	}
	e.App.Hub.Index.Forget()
	if e.App.Hub.Index.Get("FakeEvOld04") != nil || e.App.Hub.Index.Get("FakeRel0004") != nil {
		t.Fatal("the pruned event or its relationship stays")
	}
}
