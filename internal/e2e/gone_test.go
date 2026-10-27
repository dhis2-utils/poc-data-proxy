package e2e

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
)

// goneAuthor has nurse_a save a person offline and central disable nurse_a before the
// hub pushes it: the hub marks nurse_a as needing an online login, and the save waits.
func goneAuthor(t *testing.T, te, en, nat string) *env {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient(te, en, "Gone", nat, nil), 200)
	e.central.SetDisabled("nurse_a", true)
	e.online()
	e.sync()
	if u, _ := e.app.Hub.Store.UserByName("nurse_a"); !u.NeedsLogin {
		t.Fatal("nurse_a is not marked as needing an online login")
	}
	return e
}

// needsLoginFor backdates when the hub marked the user as needing an online login.
func needsLoginFor(e *env, username string, d time.Duration) {
	at := time.Now().Add(-d).UTC().Format(time.RFC3339Nano)
	if _, err := e.app.Hub.Store.Keys.Exec(`UPDATE key_slots SET needs_login_at=? WHERE username=?`, at, username); err != nil {
		e.t.Fatal(err)
	}
}

func noLoginGroup(e *env) map[string]any {
	for _, g := range refusedGroups(e) {
		if g["code"] == "NO_LOGIN" {
			return g
		}
	}
	return nil
}

// A user disabled (or deleted) on central never logs in online again: after 7 days
// their waiting saves become refusals, kept and reported like any other.
func TestAuthorGoneRefusedAfter7Days(t *testing.T) {
	e := goneAuthor(t, "GoneTe00001", "GoneEn00001", "NAT-9101")
	h := e.app.Hub
	needsLoginFor(e, "nurse_a", 6*24*time.Hour)
	e.sync()
	if n := h.Store.CountOutbox(store.OutboxPending); n != 1 || h.Store.CountIssues() != 0 {
		t.Fatalf("after 6 days: %d pending, %d issues; want the save waiting", n, h.Store.CountIssues())
	}

	needsLoginFor(e, "nurse_a", 7*24*time.Hour+time.Minute)
	e.sync()
	if n := h.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("after 7 days: %d entries still pending", n)
	}
	if noLoginGroup(e) == nil {
		t.Fatalf("no NO_LOGIN refusal in the heartbeat: %v", refusedGroups(e))
	}
	keys := e.central.DataStoreKeys("facility-hub-issues")
	if len(keys) == 0 {
		t.Fatal("the refusals were not reported to central")
	}
	is, _ := json.Marshal(e.central.DataStoreValue("facility-hub-issues", keys[0]))
	if !bytes.Contains(is, []byte("could not log in online for 7 days")) {
		t.Fatalf("issue on central lacks the reason: %s", is)
	}
	if e.central.Object("GoneTe00001") != nil {
		t.Fatal("the save reached central")
	}
}
