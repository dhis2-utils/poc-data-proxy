package live

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The retention table is read from central's dataStore at the catalog refresh.
func TestRetentionTableRead(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"retention": json.RawMessage(`{"defaultMonths": 24, "programs": {"X": 6}}`)}
	e.Setup()
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
	e.Fake.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"retention": json.RawMessage(`{"programs": {"` + fc.EventProgram + `": 12}}`)}
	e.App.Hub.RefreshCatalog(context.Background())
	e.Sync()
	if e.App.Hub.Index.Get("FakeEvOld01") != nil || e.App.Hub.Index.Get("FakeEvNew01") == nil {
		t.Fatal("pruning: the old event must go, the recent one stay")
	}
}

// Review focus 2: an event with pending edits is kept past its retention.
func TestEventRetentionKeepsPending(t *testing.T) {
	t.Skip("needs offline writes in event programs (Task 7)")
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
