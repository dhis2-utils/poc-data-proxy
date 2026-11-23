package e2e

import (
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/store"
)

// Changes central refuses because it has deleted the record (E1082, E1114: "already
// deleted") close on the hub: nobody can fix them, and DHIS2 cannot undo a deletion.
// Here an event is edited offline and deleted on central, as in physical session A,
// and a person is edited offline with a new event (the blocked child) and deleted on
// central. The hub keeps tombstones, so the records leave the offline lists and
// Android's next download marks them deleted. No issue stays, here or on central.
func TestMootRefusalClosesItself(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("MootTe00001", "MootEn00001", "Kept", "NAT-9001", map[string]string{"MootEv00001": fc.Stage1})
	e.central.Seed("MootTe00002", "MootEn00002", "Gone", "NAT-9002", nil)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	tablet := e.android("nurse_a")
	tablet.json("GET", "/api/me", nil, 200)
	e.sync()
	e.offline()
	ev := event("MootEv00001", "MootEn00001", fc.Stage1)
	ev["events"].([]any)[0].(map[string]any)["status"] = "COMPLETED"
	a.json("POST", postSync, ev, 200)
	a.json("POST", postSync, newPatient("MootTe00002", "MootEn00002", "Edited", "NAT-9002",
		map[string]string{"MootEv00003": fc.Stage1}), 200)
	e.central.SoftDelete("MootEv00001")
	e.central.SoftDelete("MootTe00002") // with its enrollment
	// Android's last download, before the push, and after central's deletions: central's
	// times are older than it. In the same millisecond the test passed without the fix
	// (the GitHub Windows runner, 23 November 2026, was slower and failed).
	time.Sleep(50 * time.Millisecond)
	mark := time.Now().UTC().Format("2006-01-02T15:04:05.000")
	time.Sleep(50 * time.Millisecond)
	e.online()
	e.sync()

	for _, uid := range []string{"MootEv00001", "MootTe00002", "MootEn00002", "MootEv00003"} {
		if o := e.app.Hub.Index.Get(uid); o == nil || !o.Deleted || o.SyncState != store.SyncSynced {
			t.Errorf("%s after the refusal: %+v, want a tombstone", uid, o)
		}
	}
	for _, uid := range []string{"MootTe00001", "MootEn00001"} {
		if o := e.app.Hub.Index.Get(uid); o == nil || o.Deleted {
			t.Errorf("%s, which central kept, after the refusal: %+v", uid, o)
		}
	}
	if n := e.app.Hub.Store.CountIssues(); n != 0 {
		t.Errorf("%d issues left on the hub", n)
	}
	if k := issueKeys(e); len(k) != 0 {
		t.Errorf("issues left in facility-hub-issues: %v", k)
	}
	if g := refusedGroups(e); len(g) != 0 {
		t.Errorf("the heartbeat still counts refusals: %v", g)
	}
	if n := outboxIn(e, store.OutboxDiscarded); n != 2 {
		t.Errorf("%d outbox entries kept as discarded, want 2", n)
	}
	before := e.central.RequestCount("POST /api/tracker")
	e.sync()
	if n := e.central.RequestCount("POST /api/tracker") - before; n != 0 {
		t.Errorf("the closed changes were sent again %d times", n)
	}
	if o := e.central.Object("MootTe00002"); o["deleted"] != true {
		t.Errorf("the person deleted on central is back: %v", o)
	}

	e.offline()
	list := a.json("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnits="+fc.Facility+"&fields=*", nil, 200)
	if got := uids(list["trackedEntities"], "trackedEntity"); contains(got, "MootTe00002") || !contains(got, "MootTe00001") {
		t.Errorf("offline list of persons: %v", got)
	}
	evs := a.json("GET", "/api/tracker/events?program="+fc.Program+"&orgUnit="+fc.Facility+"&fields=*", nil, 200)
	if got := uids(evs["events"], "event"); contains(got, "MootEv00001") || contains(got, "MootEv00003") {
		t.Errorf("offline list of events: %v", got)
	}
	deleted := func(list any, key string) map[string]bool {
		out := map[string]bool{}
		arr, _ := list.([]any)
		for _, x := range arr {
			m := x.(map[string]any)
			out[m[key].(string)] = m["deleted"] == true
		}
		return out
	}
	down := tablet.json("GET", "/api/tracker/trackedEntities?fields=*&includeDeleted=true&order=createdAt:desc&orgUnitMode=DESCENDANTS&orgUnits="+
		fc.Facility+"&page=1&pageSize=50&paging=true&program="+fc.Program+"&updatedAfter="+mark, nil, 200)
	if d := deleted(down["trackedEntities"], "trackedEntity"); !d["MootTe00002"] {
		t.Errorf("Android's download does not mark the person deleted: %v", d)
	}
	evDown := tablet.json("GET", "/api/tracker/events?fields=*&includeDeleted=true&orgUnit="+fc.Facility+
		"&orgUnitMode=DESCENDANTS&page=1&pageSize=50&paging=true&program="+fc.Program+"&updatedAfter="+mark, nil, 200)
	if d := deleted(evDown["events"], "event"); !d["MootEv00001"] || !d["MootEv00003"] {
		t.Errorf("Android's download does not mark the events deleted: %v", d)
	}
}
