package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/hubaccount"
)

// approve runs what the national team's "hub approve" does, as the superuser.
func (e *env) approve() []string {
	e.t.Helper()
	done, err := hubaccount.Approve(context.Background(), hubaccount.Options{Central: e.cSrv.URL, Username: "hub_admin",
		Password: fc.Password, Group: "HubGrpAccts", Role: "HubRoleAcct"})
	if err != nil {
		e.t.Fatal(err)
	}
	return done
}

// DEPLOYMENT.md "The hub's own account": a hub asks for an account at set-up; someone
// allowed to create users approves it; at its next refresh the hub takes it up: its
// own token, the staff member's deleted. The account is scoped to the hub's facility.
func TestHubAccount(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	id := h.HubID()
	staff := h.Runtime().HubToken
	reg := e.central.DataStoreValue("facility-hub-registry", id)
	if reg["boxKey"] == nil || reg["accountRequested"] != true {
		t.Fatalf("registry entry without the account request: %v", reg)
	}
	if done := e.approve(); len(done) != 1 || done[0] != id {
		t.Fatalf("approved %v", done)
	}
	if done := e.approve(); len(done) != 0 {
		t.Fatalf("approved again: %v", done)
	}
	if u := e.central.Users[id]; u == nil || len(u.OrgUnits) != 1 || u.OrgUnits[0] != fc.Facility {
		t.Fatalf("the hub's account is not scoped to its facility: %+v", u)
	}
	if err := h.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	rt := h.Runtime()
	if rt.TokenOwner() != id || e.central.TokenOwner(rt.HubToken) != id {
		t.Fatalf("the hub did not take up its account: owner %q", rt.TokenOwner())
	}
	if e.central.TokenValid(staff) {
		t.Error("the staff member's token still works")
	}
	if reg := e.central.DataStoreValue("facility-hub-registry", id); reg["account"] != nil {
		t.Errorf("the account's credentials stay in the registry: %v", reg["account"])
	}
	e.sync()
	if hb := e.central.DataStoreValue("facility-hub-status", id); hb["hubTokenOwner"] != id {
		t.Errorf("heartbeat owner %v", hb["hubTokenOwner"])
	}
}

// typedMove posts a new central address on the hub's admin page, as nurse.
func typedMove(e *env, nurse *client, addr string) string {
	e.t.Helper()
	resp, err := nurse.postForm("/hub/ui/move", url.Values{"central_url": {addr}})
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("Location")
}

// DEPLOYMENT.md section 6, typed in: after an unplanned move, a staff member enters
// the new address on the hub's page; the hub checks it with its own account's token
// (no staff login) and switches only if that server returns the hub's move secret.
func TestTypedCentralMove(t *testing.T) {
	e, newURL, down, _ := moveEnv(t)
	e.app.Hub.Cfg.CentralMoveAfter = time.Hour // typed in: no waiting
	nurse := e.web("nurse_a")
	nurse.login()
	other := httptest.NewServer(fc.New()) // not this country's central
	t.Cleanup(other.Close)

	down.Store(true)
	e.cycleOffline()
	if loc := typedMove(e, nurse, newURL); !strings.Contains(loc, "own+account") || e.app.Hub.Central.BaseURL() != e.cSrv.URL {
		t.Fatalf("moved without the hub's own account: %s", loc)
	}
	down.Store(false)
	e.online()
	e.approve()
	e.app.Hub.RefreshCatalog(context.Background())
	if e.app.Hub.Runtime().TokenOwner() != e.app.Hub.HubID() {
		t.Fatal("no own account")
	}
	down.Store(true)
	e.cycleOffline()
	if typedMove(e, nurse, other.URL); e.app.Hub.Central.BaseURL() != e.cSrv.URL {
		t.Fatal("moved to a server without this hub's move secret")
	}
	liar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"moveSecret":"guessed"}`)) // answers anything, with a wrong secret
	}))
	t.Cleanup(liar.Close)
	if typedMove(e, nurse, liar.URL); e.app.Hub.Central.BaseURL() != e.cSrv.URL {
		t.Fatal("moved to a server with the wrong move secret")
	}
	typedMove(e, nurse, newURL)
	if got := e.app.Hub.Central.BaseURL(); got != newURL || !e.app.Hub.Online() {
		t.Fatalf("after the typed-in move: %s, online %v", got, e.app.Hub.Online())
	}
}

// An account handed over under another name than the hub's is never taken up.
func TestHubAccountRefusesAnotherName(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	id := h.HubID()
	reg := e.central.DataStoreValue("facility-hub-registry", id)
	pub, _ := base64.StdEncoding.DecodeString(reg["boxKey"].(string))
	plain, _ := json.Marshal(hubaccount.Credentials{Username: "hub_admin", Password: fc.Password, MoveSecret: "x"})
	box, _ := hubaccount.Seal(pub, plain, id)
	reg["account"] = map[string]string{"username": "hub_admin", "box": base64.StdEncoding.EncodeToString(box)}
	b, _ := json.Marshal(reg)
	e.central.DataStore["facility-hub-registry"][id] = b
	h.RefreshCatalog(context.Background())
	if h.Runtime().TokenOwner() == "hub_admin" && h.Runtime().MoveSecret == "x" {
		t.Fatal("took up an account named for someone else")
	}
}
