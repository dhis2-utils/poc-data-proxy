package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/store"
)

// discardInstruction writes a discard to facility-hub-admin/retries, as the Facility
// Hub app's Discard does: for a group (kind, programme, code), or, with issue, for one
// refused change.
func discardInstruction(e *env, id string, g map[string]any, issue string) {
	r := map[string]any{"id": id, "action": "discard", "kind": g["kind"], "program": g["program"], "code": g["code"],
		"at": time.Now().UTC().Format(time.RFC3339), "by": "hub_admin"}
	if issue != "" {
		r["issue"] = issue
	}
	b, _ := json.Marshal(map[string]any{"retries": []any{r}})
	if e.central.DataStore["facility-hub-admin"] == nil {
		e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{}
	}
	e.central.DataStore["facility-hub-admin"]["retries"] = b
	e.central.SetPublicAccess("facility-hub-admin", "retries", "r-------")
}

func groupWith(e *env, code string) map[string]any {
	for _, g := range refusedGroups(e) {
		if g["code"] == code {
			return g
		}
	}
	return nil
}

func outboxIn(e *env, state string) int {
	var n int
	e.app.Hub.Store.DB().QueryRow(`SELECT count(*) FROM outbox WHERE state = ?`, state).Scan(&n)
	return n
}

// issueKeys are this hub's keys in central's facility-hub-issues.
func issueKeys(e *env) []string {
	var out []string
	for _, k := range e.central.DataStoreKeys("facility-hub-issues") {
		if strings.HasPrefix(k, e.app.Hub.HubID()+"-") {
			out = append(out, k)
		}
	}
	return out
}

// A hub manager discards a group of refusals that no fix will pass. Here the
// duplicate national ID was entered offline on a person central has: the hub keeps
// central's version of the person and its enrollment, and a tombstone of the event the
// refusal blocked, which central never had. Nothing is sent, the issues close, and the
// entry stays 30 days as discarded.
func TestDiscardGroup(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("CentralTe83", "CentralEn83", "Kept", "NAT-8383", nil)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	e.offline()
	e.central.Seed("CentralTe84", "CentralEn84", "Other", "NAT-8484", nil) // the hub cannot know
	a.json("POST", postSync, newPatient("CentralTe83", "CentralEn83", "Kept", "NAT-8484",
		map[string]string{"DiscEv00001": fc.Stage1}), 200)
	e.online()
	e.sync()
	dup := groupWith(e, "E1064")
	if dup == nil {
		t.Fatalf("no E1064 group in %v", refusedGroups(e))
	}
	if n := e.app.Hub.Store.CountIssues(); n != 3 {
		t.Fatalf("%d issues, want 3: the person, and the enrollment and event it blocked", n)
	}
	// A discard of another group, or of another hub's issue, changes nothing.
	discardInstruction(e, "d-other", map[string]any{"kind": dup["kind"], "program": dup["program"], "code": "E9999"}, "")
	e.sync()
	discardInstruction(e, "d-elsewhere", dup, "other-hub-1")
	e.sync()
	if n := e.app.Hub.Store.CountIssues(); n != 3 {
		t.Fatalf("%d issues after discards that name nothing here", n)
	}
	before := e.central.RequestCount("POST /api/tracker")
	discardInstruction(e, "d-dup", dup, "")
	e.sync()
	e.sync()
	if n := e.central.RequestCount("POST /api/tracker") - before; n != 0 {
		t.Errorf("the discarded saves were sent %d times", n)
	}
	full := func(uid string) string {
		o := e.app.Hub.Index.Get(uid)
		if o == nil {
			t.Fatalf("%s is gone from the hub", uid)
		}
		if o.SyncState != store.SyncSynced {
			t.Errorf("%s is %s after the discard", uid, o.SyncState)
		}
		return fmt.Sprint(e.app.Hub.Index.Full(o))
	}
	if p := full("CentralTe83"); !strings.Contains(p, "NAT-8383") || strings.Contains(p, "NAT-8484") {
		t.Errorf("the hub did not keep central's version of the person: %s", p)
	}
	if o := e.app.Hub.Index.Get("CentralEn83"); full("CentralEn83") == "" || o.Deleted {
		t.Errorf("central's enrollment became a tombstone")
	}
	if o := e.app.Hub.Index.Get("DiscEv00001"); full("DiscEv00001") == "" || !o.Deleted {
		t.Errorf("the event central never had is not a tombstone")
	}
	if g := refusedGroups(e); len(g) != 0 {
		t.Errorf("refusals left after the discard: %v", g)
	}
	if n := e.app.Hub.Store.CountIssues(); n != 0 {
		t.Errorf("%d issues left on the hub", n)
	}
	if k := issueKeys(e); len(k) != 0 {
		t.Errorf("issues left on central: %v", k)
	}
	if n := outboxIn(e, store.OutboxDiscarded); n != 1 {
		t.Errorf("%d discarded outbox entries, want 1", n)
	}
	// Offline, the person reads as central has them.
	e.offline()
	te := a.json("GET", "/api/tracker/trackedEntities/CentralTe83?program="+fc.Program+"&fields=*", nil, 200)
	if b, _ := json.Marshal(te); !strings.Contains(string(b), "NAT-8383") {
		t.Errorf("offline read after the discard: %s", b)
	}
	e.online()
	// Kept 30 days after the discard, then pruned.
	old := time.Now().Add(-store.SentOutboxKept - time.Hour).UTC().Format(time.RFC3339Nano)
	e.app.Hub.Store.DB().Exec(`UPDATE outbox SET sent_at = ? WHERE state = ?`, old, store.OutboxDiscarded)
	e.sync()
	if n := outboxIn(e, store.OutboxDiscarded); n != 0 {
		t.Errorf("%d discarded entries left after %s", n, store.SentOutboxKept)
	}
}

// A refused registration that a hub manager discards becomes a tombstone on the hub,
// with its enrollment and events: it leaves the offline lists, and Android's next
// download marks it deleted. The author can then register the person again.
func TestDiscardRefusedRegistration(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	tablet := e.android("nurse_a")
	tablet.json("GET", "/api/me", nil, 200)
	e.offline()
	e.central.Seed("CentralTe85", "CentralEn85", "Other", "NAT-8585", nil)
	a.json("POST", postSync, newPatient("DupTe000085", "DupEn000085", "Dup", "NAT-8585",
		map[string]string{"DupEv000085": fc.Stage1}), 200)
	e.online()
	e.sync()
	issues, _ := e.app.Hub.Store.UnresolvedIssues(10)
	key := e.app.Hub.HubID() + "-" + issueID(issues, "DupTe000085")
	if issueID(issues, "DupTe000085") == "" {
		t.Fatal("the registration was not refused")
	}
	// Android's last download, before the discard.
	mark := time.Now().UTC().Format("2006-01-02T15:04:05.000")
	time.Sleep(50 * time.Millisecond)
	discardInstruction(e, "d-one", map[string]any{"kind": "trackedEntity"}, key)
	e.sync()
	for _, uid := range []string{"DupTe000085", "DupEn000085", "DupEv000085"} {
		o := e.app.Hub.Index.Get(uid)
		if o == nil || !o.Deleted || o.SyncState != store.SyncSynced {
			t.Errorf("%s after the discard: %+v, want a tombstone", uid, o)
		}
	}
	if n := e.app.Hub.Store.CountIssues(); n != 0 {
		t.Errorf("%d issues left on the hub", n)
	}
	if k := issueKeys(e); len(k) != 0 {
		t.Errorf("issues left on central: %v", k)
	}
	if n := outboxIn(e, store.OutboxDiscarded); n != 1 {
		t.Errorf("%d discarded outbox entries, want 1", n)
	}
	e.offline()
	list := a.json("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnits="+fc.Facility+"&fields=*", nil, 200)
	if got := uids(list["trackedEntities"], "trackedEntity"); contains(got, "DupTe000085") {
		t.Errorf("the discarded person is still listed offline: %v", got)
	}
	down := tablet.json("GET", "/api/tracker/trackedEntities?fields=*&includeDeleted=true&order=createdAt:desc&orgUnitMode=DESCENDANTS&orgUnits="+
		fc.Facility+"&page=1&pageSize=50&paging=true&program="+fc.Program+"&updatedAfter="+mark, nil, 200)
	deleted := false
	for _, x := range down["trackedEntities"].([]any) {
		if m := x.(map[string]any); m["trackedEntity"] == "DupTe000085" {
			deleted = m["deleted"] == true
		}
	}
	if !deleted {
		t.Errorf("Android's download after the discard does not mark the person deleted: %v", uids(down["trackedEntities"], "trackedEntity"))
	}
	// The author registers the person again, under a new UID, with the right ID.
	a.json("POST", postSync, newPatient("NewTe000085", "NewEn000085", "Dup", "NAT-8686", nil), 200)
	e.online()
	e.sync()
	if e.central.Object("NewTe000085") == nil {
		t.Error("the person registered again did not reach central")
	}
}

// A discard keeps a tombstone only of a record central never had. Here central
// answers 404 for a person the hub got from central (deleted, or no longer shown to
// the author: central answers alike, review R-47): the person and their enrollment
// stay refused, with the reason, for every user and tablet; only the event entered
// offline, which central never accepted, becomes a tombstone.
func TestDiscardKeepsWhatCentralHides(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("CentralTe87", "CentralEn87", "Kept", "NAT-8787", nil)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	e.offline()
	e.central.Seed("CentralTe88", "CentralEn88", "Other", "NAT-8888", nil)
	a.json("POST", postSync, newPatient("CentralTe87", "CentralEn87", "Kept", "NAT-8888",
		map[string]string{"HideEv00001": fc.Stage1}), 200)
	e.online()
	e.sync()
	dup := groupWith(e, "E1064")
	if dup == nil {
		t.Fatalf("no E1064 group in %v", refusedGroups(e))
	}
	e.central.Hide("CentralTe87")
	discardInstruction(e, "d-hidden", dup, "")
	e.sync()
	for _, uid := range []string{"CentralTe87", "CentralEn87"} {
		if o := e.app.Hub.Index.Get(uid); o == nil || o.Deleted || o.SyncState != store.SyncRejected {
			t.Errorf("%s after the discard: %+v, want kept refused", uid, o)
		}
	}
	if o := e.app.Hub.Index.Get("HideEv00001"); o == nil || !o.Deleted {
		t.Errorf("the event central never had is not a tombstone: %+v", o)
	}
	var why map[string]string
	json.Unmarshal([]byte(e.app.Hub.Store.Meta("retries_not_applied")), &why)
	if !strings.Contains(why["d-hidden"], "central does not show this record to nurse_a: deleted, or no access") {
		t.Errorf("reason given for the kept saves: %q", why["d-hidden"])
	}
	if g := groupWith(e, "E1064"); g == nil {
		t.Errorf("the refusal is gone: %v", refusedGroups(e))
	}
}
