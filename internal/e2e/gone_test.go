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
// nurse_b has logged in online, so the hub holds a token for them.
func goneAuthor(t *testing.T, te, en, nat string) *env {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.web("nurse_b").login()
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

func retryAs(e *env, id string, g map[string]any, user string) {
	r := groupRetry(e, id, g)
	r["user"] = user
	putRetries(e, true, r)
}

// A user disabled (or deleted) on central never logs in online again: after 7 days
// their waiting saves become refusals, kept and reported like any other, and a hub
// manager can resend them as another user of the hub.
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

	// Resent as nurse_b: on central, nurse_b created the person.
	retryAs(e, "r-as-b", noLoginGroup(e), "nurse_b")
	e.sync()
	o := e.central.Object("GoneTe00001")
	if o == nil {
		t.Fatal("the resent person did not reach central")
	}
	if by, _ := o["createdBy"].(map[string]any); by["username"] != "nurse_b" {
		t.Fatalf("created by %v, want nurse_b", o["createdBy"])
	}
	if e.central.Object("GoneEn00001") == nil {
		t.Fatal("the resent enrollment did not reach central")
	}
	if g := refusedGroups(e); len(g) != 0 {
		t.Fatalf("refusals left after the resend: %v", g)
	}
}

// A resend naming a user the hub does not know, or one it cannot push for, changes
// nothing, and the hub says why in its heartbeat.
func TestResendAsUnknownUserAppliesNothing(t *testing.T) {
	e := goneAuthor(t, "GoneTe00002", "GoneEn00002", "NAT-9102")
	h := e.app.Hub
	needsLoginFor(e, "nurse_a", 8*24*time.Hour)
	e.sync()
	g := noLoginGroup(e)
	if g == nil {
		t.Fatalf("no NO_LOGIN refusal: %v", refusedGroups(e))
	}
	issues := h.Store.CountIssues()
	for _, user := range []string{"nobody_here", "nurse_a"} {
		before := e.central.RequestCount("POST /api/tracker")
		retryAs(e, "r-"+user, g, user)
		e.sync()
		if e.central.Object("GoneTe00002") != nil || e.central.RequestCount("POST /api/tracker") != before {
			t.Fatalf("a resend as %s sent the save", user)
		}
		if noLoginGroup(e) == nil || h.Store.CountIssues() != issues {
			t.Fatalf("resend as %s: refusals %v, %d issues", user, refusedGroups(e), h.Store.CountIssues())
		}
		hb, _ := json.Marshal(e.central.DataStoreValue("facility-hub-status", h.HubID())["retriesNotApplied"])
		if !bytes.Contains(hb, []byte(`"id":"r-`+user+`"`)) || !bytes.Contains(hb, []byte(user)) {
			t.Fatalf("heartbeat does not say why the resend as %s was not applied: %s", user, hb)
		}
	}
}
