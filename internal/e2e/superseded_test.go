package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/store"
)

// supersededRefusal is the nurse's case of review R-22: a correction of a person's
// identifier, entered offline, is refused (another person on central has it); then a
// visit to the person is recorded and accepted, which moves the person's updatedAt on
// central. The hub's copy follows central's, and the refusal stays open, superseded.
// It returns the refused group.
func supersededRefusal(t *testing.T, e *env) map[string]any {
	t.Helper()
	e.central.Seed("NurseTe0091", "NurseEn0091", "Asha", "NAT-9191", nil)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	e.offline()
	e.central.Seed("OtherTe0092", "OtherEn0092", "Other", "NAT-9292", nil) // the hub cannot know
	te := newPatient("NurseTe0091", "NurseEn0091", "Asha", "NAT-9292", nil)["trackedEntities"].([]any)[0].(map[string]any)
	delete(te, "enrollments")
	a.json("POST", postSync, map[string]any{"trackedEntities": []any{te}}, 200)
	e.online()
	e.sync()
	if o := e.app.Hub.Index.Get("NurseTe0091"); o == nil || o.SyncState != store.SyncRejected {
		t.Fatalf("the correction was not refused: %+v", o)
	}
	time.Sleep(1100 * time.Millisecond) // central's times are in whole seconds
	// The visit: central has the person as before, with a new updatedAt and the event.
	e.central.Seed("NurseTe0091", "NurseEn0091", "Asha", "NAT-9191", map[string]string{"VisitEv0091": fc.Stage1})
	e.sync()
	e.sync() // the issue is reported again with the push after the pull
	o := e.app.Hub.Index.Get("NurseTe0091")
	if o == nil || o.SyncState != store.SyncSuperseded {
		t.Fatalf("not superseded: %+v", o)
	}
	if full := fmt.Sprint(e.app.Hub.Index.Full(o)); !strings.Contains(full, "NAT-9191") || strings.Contains(full, "NAT-9292") {
		t.Fatalf("the hub's copy does not follow central's: %s", full)
	}
	if n := e.app.Hub.Store.CountIssues(); n != 1 {
		t.Fatalf("%d issues on the hub, want the refusal's 1", n)
	}
	keys := issueKeys(e)
	if len(keys) != 1 {
		t.Fatalf("issues on central: %v", keys)
	}
	if v := e.central.DataStoreValue("facility-hub-issues", keys[0]); v["superseded"] != true || v["objectUid"] != "NurseTe0091" {
		t.Fatalf("the issue on central does not say superseded: %v", v)
	}
	g := groupWith(e, "E1064")
	if g == nil || fmt.Sprint(g["superseded"]) != "1" {
		t.Fatalf("heartbeat group: %v", g)
	}
	if n := e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["superseded"]; fmt.Sprint(n) != "1" {
		t.Errorf("heartbeat superseded: %v", n)
	}
	return g
}

// Resend sends the refused change on top of central's current version: the visit
// stays, the correction arrives, and the refusal closes.
func TestSupersededRefusalResent(t *testing.T) {
	e := newEnv(t)
	g := supersededRefusal(t, e)
	e.central.Seed("OtherTe0092", "OtherEn0092", "Other", "NAT-0000", nil) // fixed on central
	retry(e, "r-sup", g)
	e.sync()
	c := e.central.Object("NurseTe0091")
	if !strings.Contains(fmt.Sprint(c), "NAT-9292") {
		t.Fatalf("the correction did not reach central: %v", c)
	}
	if e.central.Object("VisitEv0091") == nil {
		t.Fatal("the visit is gone from central")
	}
	e.sync()
	o := e.app.Hub.Index.Get("NurseTe0091")
	if o == nil || o.SyncState != store.SyncSynced || !strings.Contains(fmt.Sprint(e.app.Hub.Index.Full(o)), "NAT-9292") {
		t.Fatalf("the hub's copy after the resend: %+v", o)
	}
	if n := e.app.Hub.Store.CountIssues(); n != 0 || len(issueKeys(e)) != 0 {
		t.Errorf("issues left: %d on the hub, %v on central", n, issueKeys(e))
	}
}

// Discard closes the refusal and keeps central's version, which the hub shows already.
func TestSupersededRefusalDiscarded(t *testing.T) {
	e := newEnv(t)
	g := supersededRefusal(t, e)
	discardInstruction(e, "d-sup", g, "")
	e.sync()
	o := e.app.Hub.Index.Get("NurseTe0091")
	if o == nil || o.SyncState != store.SyncSynced || !strings.Contains(fmt.Sprint(e.app.Hub.Index.Full(o)), "NAT-9191") {
		t.Fatalf("the hub's copy after the discard: %+v", o)
	}
	if strings.Contains(fmt.Sprint(e.central.Object("NurseTe0091")), "NAT-9292") {
		t.Fatal("the discarded correction reached central")
	}
	if n := outboxIn(e, store.OutboxDiscarded); n != 1 {
		t.Errorf("%d discarded outbox entries, want 1", n)
	}
	if n := e.app.Hub.Store.CountIssues(); n != 0 || len(issueKeys(e)) != 0 {
		t.Errorf("issues left: %d on the hub, %v on central", n, issueKeys(e))
	}
}
