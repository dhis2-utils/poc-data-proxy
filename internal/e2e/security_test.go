// Regression tests for the findings in docs/REVIEW.md.
package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/store"
)

// M2: a write accepted while an upgrade copies the store would be lost with the
// store the upgrade replaces. The hub refuses it instead.
func TestUpgradeFreezesLocalWrites(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	h := e.app.Hub

	h.FreezeLocalWrites()
	a.json("POST", postSync, newPatient("FrozTe00001", "FrozEn00001", "Frozen", "", nil), 503)
	if n := h.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("%d outbox entries written during the upgrade", n)
	}
	if st, body := a.do("GET", "/api/trackedEntityAttributes/"+fc.AttrTBID+"/generate", nil); st != 503 {
		t.Fatalf("reserved value handed out during the upgrade: %d %s", st, body)
	}

	h.ThawLocalWrites() // the upgrade failed and the hub carries on
	a.json("POST", postSync, newPatient("FrozTe00001", "FrozEn00001", "Frozen", "", nil), 200)
	if n := h.Store.CountOutbox(store.OutboxPending); n != 1 {
		t.Fatalf("outbox entries after thaw: %d", n)
	}
}

// M5: the hub-level token is renewed before it expires, and the old one revoked.
func TestHubTokenRenewal(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	old := h.Runtime().HubToken
	e.sync()
	if h.Runtime().HubToken != old {
		t.Fatal("a fresh hub token was renewed")
	}
	h.UpdateRuntime(func(rt *core.Runtime) { rt.HubTokenExp = time.Now().Add(48 * time.Hour).Format(time.RFC3339) })
	e.sync()
	rt := h.Runtime()
	if rt.HubToken == old || !e.central.TokenValid(rt.HubToken) {
		t.Fatal("hub token not renewed")
	}
	if e.central.TokenValid(old) {
		t.Fatal("old hub token not revoked")
	}
	if exp, _ := time.Parse(time.RFC3339, rt.HubTokenExp); time.Until(exp) < 80*24*time.Hour {
		t.Fatalf("new expiry %s", rt.HubTokenExp)
	}
}

// M1: the hub refuses Host names it does not answer to, and a response cached for one
// Host is never replayed to another client with that Host baked in.
func TestHostHeader(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.app.Hub.Cfg.HubHosts = []string{"alias.test"}
	a := e.web("nurse_a")
	a.login()
	b := e.web("nurse_b")
	b.login()

	if st, _ := a.doHost("evil.example", "GET", "/dhis-web-capture/index.html", nil); st != 421 {
		t.Fatalf("foreign Host: got %d, want 421", st)
	}
	st, body := a.doHost("alias.test", "GET", "/dhis-web-capture/index.html", nil)
	if st != 200 || !strings.Contains(string(body), `content="http://alias.test"`) {
		t.Fatalf("online, base URL not pointed at the hub: %d %s", st, body)
	}
	e.offline()
	st, body = b.do("GET", "/dhis-web-capture/index.html", nil) // served from nurse_a's cache entry
	if st != 200 || strings.Contains(string(body), "alias.test") || !strings.Contains(string(body), e.srv.URL) {
		t.Fatalf("offline, cached page carries another client's Host: %d %s", st, body)
	}
}

// M4: sessions end after inactivity.
func TestSessionIdleExpiry(t *testing.T) {
	e := newEnv(t)
	e.setup()
	search := "/api/tracker/trackedEntities?orgUnits=" + fc.Facility + "&program=" + fc.Program
	e.app.Hub.Sessions.Idle = 100 * time.Millisecond
	a := e.web("nurse_a")
	a.login()
	a.json("GET", search, nil, 200)
	time.Sleep(200 * time.Millisecond)
	if st, _ := a.do("GET", "/api/me", nil); st != 401 {
		t.Fatalf("idle session still accepted: %d", st)
	}
}

// M4: an account disabled on central stops working through the hub, online on the
// next credential check and offline from then on, until an online login succeeds.
func TestDisabledUserLosesAccess(t *testing.T) {
	old := core.BasicRecheck
	core.BasicRecheck = 0
	t.Cleanup(func() { core.BasicRecheck = old })
	e := newEnv(t)
	e.setup()
	search := "/api/tracker/trackedEntities?orgUnits=" + fc.Facility + "&program=" + fc.Program
	tablet := e.android("nurse_a")
	tablet.json("GET", search, nil, 200)
	web := e.web("nurse_a")
	web.login()

	e.central.SetDisabled("nurse_a", true)
	if st, _ := tablet.do("GET", search, nil); st != 401 {
		t.Fatalf("disabled user's Basic credential still accepted online: %d", st)
	}
	e.offline()
	if st, _ := tablet.do("GET", search, nil); st != 401 {
		t.Fatalf("disabled user's Basic credential accepted offline: %d", st)
	}
	if st, _ := web.do("GET", search, nil); st != 401 {
		t.Fatalf("disabled user's web session survived: %d", st)
	}
	if st, _ := e.web("nurse_a").do("POST", "/api/auth/login", map[string]string{"username": "nurse_a", "password": fc.Password}); st != 401 {
		t.Fatalf("disabled user logged in offline: %d", st)
	}

	e.central.SetDisabled("nurse_a", false)
	e.online()
	tablet.json("GET", search, nil, 200)
	e.offline()
	tablet.json("GET", search, nil, 200)
}
