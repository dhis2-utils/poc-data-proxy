package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/hubaccount"
)

func (e *env) approveOpts() hubaccount.Options {
	return hubaccount.Options{Central: e.cSrv.URL, Username: "hub_admin", Password: fc.Password, Group: "HubGrpAccts", Role: "HubRoleAcct"}
}

// approve runs what the national team's "hub approve <hub ID>" does for this hub, as
// the superuser.
func (e *env) approve() []string {
	e.t.Helper()
	done, err := hubaccount.Approve(context.Background(), e.approveOpts(), []string{e.app.Hub.HubID()})
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
	if done, err := hubaccount.Approve(context.Background(), e.approveOpts(), []string{id}); len(done) != 0 || err == nil {
		t.Fatalf("approved again: %v %v", done, err)
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

// typedMove posts a new central address on the hub's admin page, as nurse, who
// confirms with their password.
func typedMove(e *env, nurse *client, addr, password string) string {
	e.t.Helper()
	resp, err := nurse.postForm("/hub/ui/move", url.Values{"central_url": {addr}, "password": {password}})
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("Location")
}

// ownAccountMoveEnv: a hub with its own account, a logged-in nurse, and central's old
// address down for longer than central_move_after.
func ownAccountMoveEnv(t *testing.T) (e *env, nurse *client, newURL string, down *atomic.Bool) {
	t.Helper()
	e, newURL, down, _ = moveEnv(t)
	e.app.Hub.Cfg.CentralMoveAfter = time.Hour
	nurse = e.web("nurse_a")
	nurse.login()
	e.approve()
	e.app.Hub.RefreshCatalog(context.Background())
	if e.app.Hub.Runtime().TokenOwner() != e.app.Hub.HubID() {
		t.Fatal("no own account")
	}
	down.Store(true)
	e.cycleOffline()
	downFor(e, 2*time.Hour)
	return e, nurse, newURL, down
}

// downFor backdates when central stopped answering.
func downFor(e *env, d time.Duration) {
	e.app.Hub.Store.SetMeta("offline_since", time.Now().Add(-d).UTC().Format(time.RFC3339Nano))
}

// trap is a server that answers anything with a wrong move secret, and records the
// credentials it was sent.
func trap(t *testing.T) (*httptest.Server, *atomic.Value) {
	got := &atomic.Value{}
	got.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			got.Store(strings.TrimPrefix(a, "ApiToken "))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"moveSecret":"guessed"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// DEPLOYMENT.md section 6, typed in: after an unplanned move, a staff member enters
// the new address on the hub's page, with their password; the hub checks it with its
// own account's token and switches only if that server returns the hub's move secret.
func TestTypedCentralMove(t *testing.T) {
	e, newURL, down, _ := moveEnv(t)
	e.app.Hub.Cfg.CentralMoveAfter = time.Hour
	nurse := e.web("nurse_a")
	nurse.login()
	down.Store(true)
	e.cycleOffline()
	downFor(e, 2*time.Hour)
	if loc := typedMove(e, nurse, newURL, fc.Password); !strings.Contains(loc, "own+account") || e.app.Hub.Central.BaseURL() != e.cSrv.URL {
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
	downFor(e, 2*time.Hour)
	typedMove(e, nurse, newURL, fc.Password)
	if got := e.app.Hub.Central.BaseURL(); got != newURL || !e.app.Hub.Online() {
		t.Fatalf("after the typed-in move: %s, online %v", got, e.app.Hub.Online())
	}
}

// Review C2: the typed-in check sends the hub's token, so it runs only when central
// has really stopped answering, for central_move_after, and the staff member confirms
// with their own password. "Simulate uplink cut" does not count.
func TestTypedMoveNeedsARealLongOutage(t *testing.T) {
	e, nurse, _, down := ownAccountMoveEnv(t)
	srv, got := trap(t)
	if typedMove(e, nurse, srv.URL, "wrong"); got.Load() != "" {
		t.Fatal("the hub's token went out without the staff member's password")
	}
	downFor(e, time.Minute)
	if typedMove(e, nurse, srv.URL, fc.Password); got.Load() != "" {
		t.Fatal("the hub's token went out before central_move_after")
	}
	down.Store(false)
	e.online()
	downFor(e, 2*time.Hour)
	off := false
	e.app.Hub.Monitor.Force(&off)
	if typedMove(e, nurse, srv.URL, fc.Password); got.Load() != "" {
		t.Fatal("the hub's token went out while central still answers (uplink switched off)")
	}
}

// Review C2: a server that receives the hub's token and fails the check could read
// the move secret on central with it. Typed-in moves stop until the hub has reached
// central again and replaced its password, token and move secret. A server that
// cannot be reached received nothing, and locks nothing.
func TestTypedMoveLocksAfterAFailedCheck(t *testing.T) {
	e, nurse, newURL, down := ownAccountMoveEnv(t)
	h := e.app.Hub
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	typedMove(e, nurse, gone.URL, fc.Password) // a typo: nothing answers
	srv, got := trap(t)
	typedMove(e, nurse, srv.URL, fc.Password)
	leaked := got.Load().(string)
	if leaked == "" || h.Central.BaseURL() != e.cSrv.URL {
		t.Fatalf("the trap: token %q, central %s", leaked, h.Central.BaseURL())
	}
	if loc := typedMove(e, nurse, newURL, fc.Password); h.Central.BaseURL() != e.cSrv.URL {
		t.Fatalf("moved after a failed check, before the hub's credentials were replaced: %s", loc)
	}
	oldSecret := h.Runtime().MoveSecret
	down.Store(false)
	e.online()
	h.RefreshCatalog(context.Background())
	if e.central.TokenValid(leaked) {
		t.Error("the token sent to the failed server still works")
	}
	sec := e.central.DataStoreValue("facility-hub-accounts", h.HubID())
	if sec["moveSecret"] == oldSecret || sec["moveSecret"] != h.Runtime().MoveSecret {
		t.Errorf("move secret not replaced: central %v, hub %q, old %q", sec["moveSecret"], h.Runtime().MoveSecret, oldSecret)
	}
	down.Store(true)
	e.cycleOffline()
	downFor(e, 2*time.Hour)
	if typedMove(e, nurse, newURL, fc.Password); h.Central.BaseURL() != newURL {
		t.Fatal("typed-in moves still locked after the credentials were replaced")
	}
}

// Review C2: "Simulate uplink cut" is for demonstrations, off unless hub.yaml turns it on.
func TestUplinkSwitchOffByDefault(t *testing.T) {
	e := newEnv(t)
	e.setup()
	admin := e.web("nurse_a")
	admin.login()
	resp, err := admin.postForm("/hub/ui/uplink", url.Values{"state": {"off"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if e.app.Hub.Monitor.ForcedOffline() {
		t.Fatal("the uplink switch works without uplink_switch in hub.yaml")
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
	if rt := h.Runtime(); rt.HubTokenOwner != "" || rt.HubPassword != "" || rt.MoveSecret == "x" {
		t.Fatal("took up an account named for someone else")
	}
}

// plant writes a registry entry as user, through central's API (so central records
// who created it).
func plant(e *env, user, id string, v map[string]any) {
	e.t.Helper()
	b, _ := json.Marshal(v)
	req, _ := http.NewRequest("POST", e.cSrv.URL+"/api/dataStore/facility-hub-registry/"+id, strings.NewReader(string(b)))
	req.SetBasicAuth(user, fc.Password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode >= 300 {
		e.t.Fatalf("planting %s: %v %v", id, resp.StatusCode, err)
	}
	resp.Body.Close()
}

// Review C1: anyone who can write the registry can add an entry. "hub approve" lists
// the hubs waiting for an account and hands one only to a hub named on its command
// line whose entry the staff member who set it up created, and who is assigned to its
// facility. One refused hub does not stop the others.
func TestApproveChecksTheEntry(t *testing.T) {
	e := newEnv(t)
	e.setup()
	id := e.app.Hub.HubID()
	pub, _, _ := hubaccount.NewBoxKey()
	key := base64.StdEncoding.EncodeToString(pub)
	entry := func(facility, setupBy string) map[string]any {
		return map[string]any{"hubId": "x", "facility": facility, "setupBy": setupBy, "status": "active",
			"accountRequested": true, "boxKey": key}
	}
	plant(e, "nurse_a", "hub-claimed", entry(fc.OtherFacility, "outsider_d")) // created by someone else than setupBy
	plant(e, "nurse_a", "hub-elsewhere", entry(fc.OtherFacility, "nurse_a"))  // nurse_a is not assigned there
	plant(e, "nurse_a", "hub-nowhere", entry("", "nurse_a"))                  // no facility
	pending, err := hubaccount.Pending(context.Background(), e.approveOpts())
	if err != nil {
		t.Fatal(err)
	}
	problems := map[string]string{}
	for _, p := range pending {
		problems[p.HubID] = p.Problem
	}
	if len(problems) != 4 || problems[id] != "" || problems["hub-claimed"] == "" || problems["hub-elsewhere"] == "" || problems["hub-nowhere"] == "" {
		t.Fatalf("pending: %+v", pending)
	}
	if done, err := hubaccount.Approve(context.Background(), e.approveOpts(), nil); len(done) != 0 || err != nil {
		t.Fatalf("approved without naming a hub: %v %v", done, err)
	}
	done, err := hubaccount.Approve(context.Background(), e.approveOpts(), []string{"hub-claimed", "hub-elsewhere", "hub-nowhere", id})
	if len(done) != 1 || done[0] != id || err == nil {
		t.Fatalf("approved %v, err %v", done, err)
	}
	for _, other := range []string{"hub-claimed", "hub-elsewhere", "hub-nowhere"} {
		if e.central.Users[other] != nil || e.central.DataStoreValue("facility-hub-registry", other)["account"] != nil {
			t.Errorf("an account for %s", other)
		}
	}
}

// Review C1, I2, I5: someone replaces the box key in a hub's entry before approval.
// The hub sees its key is not the one published, publishes it again and asks again;
// approving again resets the existing account's password and move secret, so what
// the first box carried no longer works.
func TestApproveAgainAfterAReplacedKey(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	id := h.HubID()
	reg := e.central.DataStoreValue("facility-hub-registry", id)
	attacker, priv, _ := hubaccount.NewBoxKey()
	reg["boxKey"] = base64.StdEncoding.EncodeToString(attacker)
	b, _ := json.Marshal(reg)
	e.central.DataStore["facility-hub-registry"][id] = b
	e.approve()
	box, _ := base64.StdEncoding.DecodeString(e.central.DataStoreValue("facility-hub-registry", id)["account"].(map[string]any)["box"].(string))
	plain, err := hubaccount.Open(priv, box, id)
	var stolen hubaccount.Credentials
	if err != nil || json.Unmarshal(plain, &stolen) != nil {
		t.Fatalf("the attacker's box: %v", err)
	}
	h.RefreshCatalog(context.Background())
	if h.Runtime().TokenOwner() == id {
		t.Fatal("took up an account sealed to another key")
	}
	reg = e.central.DataStoreValue("facility-hub-registry", id)
	if reg["boxKey"] == base64.StdEncoding.EncodeToString(attacker) || reg["accountRequested"] != true || reg["account"] != nil {
		t.Fatalf("the hub did not publish its key again: %v", reg)
	}
	e.approve()
	h.RefreshCatalog(context.Background())
	if h.Runtime().TokenOwner() != id {
		t.Fatal("the hub did not take up its account after the second approval")
	}
	if e.central.PasswordOf(id) == stolen.Password {
		t.Error("the stolen password still works")
	}
	if e.central.DataStoreValue("facility-hub-accounts", id)["moveSecret"] == stolen.MoveSecret {
		t.Error("the stolen move secret is still central's")
	}
}

// Review C1: the hub replaces what came in the box (password, token, move secret) as
// soon as it takes the account up, and deletes the account's other tokens.
func TestHubAccountReplacesHandedOverCredentials(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	id := h.HubID()
	e.approve()
	box, _ := base64.StdEncoding.DecodeString(e.central.DataStoreValue("facility-hub-registry", id)["account"].(map[string]any)["box"].(string))
	plain, _ := hubaccount.Open(h.Runtime().BoxKey, box, id)
	var creds hubaccount.Credentials
	json.Unmarshal(plain, &creds)
	// Someone who saw the credentials made a token of their own with them.
	other := e.central.CreateToken(id)
	h.RefreshCatalog(context.Background())
	rt := h.Runtime()
	if rt.TokenOwner() != id {
		t.Fatal("not taken up")
	}
	if e.central.PasswordOf(id) == creds.Password || rt.HubPassword != e.central.PasswordOf(id) {
		t.Error("the password from the box still works, or the hub does not hold the new one")
	}
	if e.central.TokenValid(other) {
		t.Error("another token of the account still works")
	}
	if sec := e.central.DataStoreValue("facility-hub-accounts", id)["moveSecret"]; sec == creds.MoveSecret || sec != rt.MoveSecret {
		t.Errorf("move secret: central %v, hub %q, box %q", sec, rt.MoveSecret, creds.MoveSecret)
	}
	if rt.BoxKey != nil {
		t.Error("the box key is kept after the account was taken up")
	}
}

// Review I1: the hub takes up an account only if it sees the facility only and has no
// authorities: anything central shares publicly is then limited to the facility.
func TestHubAccountRefusesWiderAccounts(t *testing.T) {
	for name, widen := range map[string]func(u *fc.User){
		"authorities": func(u *fc.User) { u.Authorities = []string{"ALL"} },
		"data view":   func(u *fc.User) { u.DataViewOrgUnits = []string{fc.District} },
		"search":      func(u *fc.User) { u.SearchOrgUnits = []string{fc.Facility, fc.OtherFacility} },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.setup()
			e.approve()
			widen(e.central.Users[e.app.Hub.HubID()])
			e.app.Hub.RefreshCatalog(context.Background())
			if e.app.Hub.Runtime().TokenOwner() == e.app.Hub.HubID() {
				t.Fatal("took up an account wider than the facility")
			}
		})
	}
}

// Review I4: a staff member who takes the hub's housekeeping over sets its own account
// aside: the hub keeps no password or move secret it no longer uses (typed-in moves
// need the own account again), and asks for an account again, which the national team
// approves as before.
func TestTakeOverSetsTheOwnAccountAside(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	e.approve()
	h.RefreshCatalog(context.Background())
	if h.Runtime().TokenOwner() != h.HubID() {
		t.Fatal("no own account")
	}
	if err := h.TakeOverHubToken(context.Background(), "nurse_a", fc.Password); err != nil {
		t.Fatal(err)
	}
	rt := h.Runtime()
	if rt.TokenOwner() != "nurse_a" || rt.HubPassword != "" || rt.MoveSecret != "" {
		t.Fatalf("after the take-over: owner %q, password kept %v, move secret kept %v", rt.TokenOwner(), rt.HubPassword != "", rt.MoveSecret != "")
	}
	h.RefreshCatalog(context.Background())
	if reg := e.central.DataStoreValue("facility-hub-registry", h.HubID()); reg["accountRequested"] != true {
		t.Errorf("the hub does not ask for an account again: %v", reg)
	}
}
