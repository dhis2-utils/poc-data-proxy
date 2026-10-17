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

	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/hubaccount"
)

func (e *env) approveOpts() hubaccount.Options {
	return hubaccount.Options{Central: e.cSrv.URL, Username: "hub_admin", Password: fc.Password, Group: "HubGrpAccts", Role: "HubRoleAcct"}
}

// approveWith does what the Facility Hub app does when the national team approves
// this hub with a code the facility read to them.
func (e *env) approveWith(code string) ([]string, error) {
	return hubaccount.Approve(context.Background(), e.approveOpts(), []hubaccount.Request{{HubID: e.app.Hub.HubID(), Code: code}})
}

// approve approves this hub with the code on its admin page.
func (e *env) approve() []string {
	e.t.Helper()
	done, err := e.approveWith(e.app.Hub.AccountCode())
	if err != nil {
		e.t.Fatal(err)
	}
	return done
}

// adopt approves the hub and lets it take the account up.
func (e *env) adopt() {
	e.t.Helper()
	e.approve()
	e.app.Hub.RefreshCatalog(context.Background())
	if e.app.Hub.Runtime().TokenOwner() != e.app.Hub.HubID() {
		e.t.Fatal("the hub did not take up its account")
	}
}

// moveCode is what the Facility Hub app gives the national team for this hub and addr.
func (e *env) moveCode(addr string) string {
	e.t.Helper()
	v, _ := e.central.DataStoreValue("facility-hub-keys", e.app.Hub.HubID())["moveKey"].(string)
	key, err := base64.StdEncoding.DecodeString(v)
	if err != nil || v == "" {
		e.t.Fatal("no move key on central")
	}
	return hubaccount.MoveCode(key, addr)
}

// DEPLOYMENT.md "The hub's own account": a hub asks for an account at set-up and shows
// a code on its admin page; the national team approves it in the Facility Hub app with
// that code; at its next refresh the hub takes it up: its own token, the staff
// member's deleted. The account is scoped to the hub's facility.
func TestHubAccount(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	id := h.HubID()
	staff := h.Runtime().HubToken
	reg := e.central.DataStoreValue("facility-hub-registry", id)
	if reg["boxKey"] == nil || reg["accountRequested"] != true || h.AccountCode() == "" {
		t.Fatalf("no account request, or no code on the hub: %v", reg)
	}
	if done := e.approve(); len(done) != 1 || done[0] != id {
		t.Fatalf("approved %v", done)
	}
	if done, err := e.approveWith(h.AccountCode()); len(done) != 0 || err == nil {
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
	if reg := e.central.DataStoreValue("facility-hub-registry", id); reg["account"] != nil || reg["accountRequested"] != nil {
		t.Errorf("the account's credentials stay in the registry: %v", reg)
	}
	if h.AccountCode() != "" {
		t.Error("an account code is still shown")
	}
	e.sync()
	if hb := e.central.DataStoreValue("facility-hub-status", id); hb["hubTokenOwner"] != id {
		t.Errorf("heartbeat owner %v", hb["hubTokenOwner"])
	}
}

// Review K1: anyone in the hub owners group can write the registry and put their own
// key in a hub's entry. The approval needs the code on the hub's own screen, which no
// other key matches; a wrong code is refused too.
func TestApproveNeedsTheCodeOnTheHub(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	id := h.HubID()
	onScreen := h.AccountCode()
	if _, err := e.approveWith("AAAA-AAAA-AAAA-AAAA"); err == nil {
		t.Fatal("approved with a wrong code")
	}
	reg := e.central.DataStoreValue("facility-hub-registry", id)
	attacker, _, _ := hubaccount.NewBoxKey()
	reg["boxKey"] = base64.StdEncoding.EncodeToString(attacker)
	b, _ := json.Marshal(reg)
	e.central.DataStore["facility-hub-registry"][id] = b
	if _, err := e.approveWith(onScreen); err == nil {
		t.Fatal("approved an entry whose key is not the hub's")
	}
	if e.central.Users[id] != nil || e.central.DataStoreValue("facility-hub-registry", id)["account"] != nil {
		t.Fatal("an account was created or handed over")
	}
	h.RefreshCatalog(context.Background()) // the hub publishes its key again
	if _, err := e.approveWith(onScreen); err != nil {
		t.Fatalf("approving with the hub's key back: %v", err)
	}
}

// Review K1: an account the hub cannot open (sealed to another key) is not left in its
// entry: the hub asks again, so the national team sees it waiting.
func TestHubAsksAgainWhenTheBoxDoesNotOpen(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	id := h.HubID()
	other, _, _ := hubaccount.NewBoxKey()
	box, _ := hubaccount.Seal(other, []byte(`{"username":"x"}`), id)
	reg := e.central.DataStoreValue("facility-hub-registry", id)
	reg["account"] = map[string]string{"username": id, "box": base64.StdEncoding.EncodeToString(box)}
	delete(reg, "accountRequested")
	b, _ := json.Marshal(reg)
	e.central.DataStore["facility-hub-registry"][id] = b
	h.RefreshCatalog(context.Background())
	if reg := e.central.DataStoreValue("facility-hub-registry", id); reg["account"] != nil || reg["accountRequested"] != true {
		t.Fatalf("the hub did not ask again: %v", reg)
	}
}

// Review K2: a hub on its own account shows no code, so an entry marked "asks again"
// by someone else cannot be approved; after a take-over it shows a new one.
func TestOnlyAHubAskingShowsACode(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	first := h.AccountCode()
	e.adopt()
	if h.AccountCode() != "" {
		t.Fatal("a hub on its own account shows a code")
	}
	reg := e.central.DataStoreValue("facility-hub-registry", h.HubID())
	attacker, _, _ := hubaccount.NewBoxKey()
	reg["boxKey"], reg["accountRequested"] = base64.StdEncoding.EncodeToString(attacker), true
	b, _ := json.Marshal(reg)
	e.central.DataStore["facility-hub-registry"][h.HubID()] = b
	if _, err := e.approveWith(first); err == nil {
		t.Fatal("approved again with an old code")
	}
	if err := h.TakeOverHubToken(context.Background(), "nurse_a", fc.Password); err != nil {
		t.Fatal(err)
	}
	h.RefreshCatalog(context.Background())
	if c := h.AccountCode(); c == "" || c == first {
		t.Fatalf("after a take-over: code %q (first %q)", c, first)
	}
}

// Review I-d: the move key is readable by administrators only. Someone who created its
// entry beforehand (and so would own it) is shut out: approval makes it anew.
func TestMoveKeyOnlyForAdministrators(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	key := "/api/dataStore/facility-hub-keys/" + h.HubID()
	as := func(user, method string) int {
		req, _ := http.NewRequest(method, e.cSrv.URL+key, strings.NewReader(`{"moveKey":"c3F1YXR0ZWQ="}`))
		req.SetBasicAuth(user, fc.Password)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if st := as("nurse_a", "POST"); st >= 300 {
		t.Fatalf("planting the entry: %d", st)
	}
	e.adopt()
	if st := as("nurse_a", "GET"); st == 200 {
		t.Error("whoever created the entry first reads the move key")
	}
	req, _ := http.NewRequest("GET", e.cSrv.URL+key, nil)
	req.Header.Set("Authorization", "ApiToken "+h.Runtime().HubToken)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Error("the hub's own token reads the move key")
		}
	}
}

// typedMove posts a new central address and its code on the hub's admin page, as nurse.
func typedMove(e *env, nurse *client, addr, code string) string {
	e.t.Helper()
	resp, err := nurse.postForm("/hub/ui/move", url.Values{"central_url": {addr}, "code": {code}})
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.Header.Get("Location")
}

// trap is a server that records whether the hub sent it anything.
func trap(t *testing.T) (*httptest.Server, *atomic.Bool) {
	got := &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(true)
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// DEPLOYMENT.md section 6, typed in: after an unplanned move, the national team gives
// each facility central's new address and a code made for it (Facility Hub app). A
// staff member enters both on the hub's page; the hub checks the code itself, sends
// nothing anywhere for it, and moves only to the address the code was made for.
func TestTypedCentralMove(t *testing.T) {
	e, newURL, down, _ := moveEnv(t)
	h := e.app.Hub
	nurse := e.web("nurse_a")
	nurse.login()
	down.Store(true)
	e.cycleOffline()
	if loc := typedMove(e, nurse, newURL, "AAAAA-AAAAA"); !strings.Contains(loc, "approved") || h.Central.BaseURL() != e.cSrv.URL {
		t.Fatalf("moved without a move key: %s", loc)
	}
	down.Store(false)
	e.online()
	e.adopt()
	down.Store(true)
	e.cycleOffline()
	srv, got := trap(t)
	typedMove(e, nurse, srv.URL, e.moveCode(newURL)) // a code for another address
	typedMove(e, nurse, srv.URL, "AAAAA-AAAAA")
	if got.Load() || h.Central.BaseURL() != e.cSrv.URL {
		t.Fatalf("a wrong code: sent something %v, central %s", got.Load(), h.Central.BaseURL())
	}
	typedMove(e, nurse, newURL, strings.ToLower(e.moveCode(newURL)))
	if got := h.Central.BaseURL(); got != newURL || !h.Online() {
		t.Fatalf("after the typed-in move: %s, online %v", got, h.Online())
	}
}

// A typed-in move needs central to be unreachable for real: the demo uplink switch
// does not count.
func TestTypedMoveNeedsCentralDown(t *testing.T) {
	e, newURL, _, _ := moveEnv(t)
	h := e.app.Hub
	nurse := e.web("nurse_a")
	nurse.login()
	e.adopt()
	off := false
	h.Monitor.Force(&off)
	if typedMove(e, nurse, newURL, e.moveCode(newURL)); h.Central.BaseURL() != e.cSrv.URL {
		t.Fatal("moved while central still answers")
	}
}

// Review I-e: a hub started while central does not answer counts the outage from its
// start, so it can still follow an announced move.
func TestHubStartedDuringOutageCanMove(t *testing.T) {
	e, newURL, down, _ := moveEnv(t)
	announce(e, newURL)
	e.cfgHook = func(c *config.Config) { c.CentralURL = e.cSrv.URL; c.CentralMoveAfter = time.Hour }
	nurse := e.web("nurse_a")
	nurse.login()
	e.stop()
	down.Store(true)
	e.start()
	e.app.Hub.Cfg.CentralURL = ""
	e.web("nurse_a").login() // offline: unlocks the hub
	e.cycleOffline()
	if e.app.Hub.Store.Meta("offline_since") == "" {
		t.Fatal("the outage is not counted from the hub's start")
	}
	downFor(e, 2*time.Hour)
	e.app.Worker.Cycle(context.Background())
	if e.app.Hub.Central.BaseURL() != newURL {
		t.Fatalf("not moved: %s", e.app.Hub.Central.BaseURL())
	}
}

// downFor backdates when central stopped answering.
func downFor(e *env, d time.Duration) {
	e.app.Hub.Store.SetMeta("offline_since", time.Now().Add(-d).UTC().Format(time.RFC3339Nano))
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
	plain, _ := json.Marshal(hubaccount.Credentials{Username: "hub_admin", Password: fc.Password, MoveKey: []byte("x")})
	box, _ := hubaccount.Seal(pub, plain, id)
	reg["account"] = map[string]string{"username": "hub_admin", "box": base64.StdEncoding.EncodeToString(box)}
	b, _ := json.Marshal(reg)
	e.central.DataStore["facility-hub-registry"][id] = b
	h.RefreshCatalog(context.Background())
	if rt := h.Runtime(); rt.HubTokenOwner != "" || rt.HubPassword != "" || rt.MoveKey != nil {
		t.Fatal("took up an account named for someone else")
	}
}

// Review C1: the hub replaces the password and token it was handed as soon as it takes
// the account up, and deletes the account's other tokens: whoever saw them on the way
// is shut out.
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
	other := e.central.CreateToken(id) // made by someone who saw the credentials
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
// aside: the hub keeps no password it no longer uses, and asks for an account again.
// Its move key stays: it is not tied to the account.
func TestTakeOverSetsTheOwnAccountAside(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	e.adopt()
	if err := h.TakeOverHubToken(context.Background(), "nurse_a", fc.Password); err != nil {
		t.Fatal(err)
	}
	rt := h.Runtime()
	if rt.TokenOwner() != "nurse_a" || rt.HubPassword != "" || rt.MoveKey == nil {
		t.Fatalf("after the take-over: owner %q, password kept %v, move key kept %v", rt.TokenOwner(), rt.HubPassword != "", rt.MoveKey != nil)
	}
	h.RefreshCatalog(context.Background())
	if reg := e.central.DataStoreValue("facility-hub-registry", h.HubID()); reg["accountRequested"] != true {
		t.Errorf("the hub does not ask for an account again: %v", reg)
	}
}

// Review I-a: central changes the password but the answer is lost. The hub kept the
// new password before asking, so it finishes at the next refresh instead of holding a
// password central no longer accepts.
func TestPasswordChangeSurvivesALostAnswer(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	e.approve()
	e.central.LoseResponse = map[string]int{"PUT /api/me/changePassword": 1}
	h.RefreshCatalog(context.Background())
	h.RefreshCatalog(context.Background())
	rt := h.Runtime()
	if rt.TokenOwner() != h.HubID() || rt.HubPassword != e.central.PasswordOf(h.HubID()) || h.Store.Meta("account_rotate") != "" {
		t.Fatalf("after a lost answer: owner %q, password matches %v, rotate %q", rt.TokenOwner(),
			rt.HubPassword == e.central.PasswordOf(h.HubID()), h.Store.Meta("account_rotate"))
	}
}

// Review I-b: the hub's write to its registry entry is done on central but the answer
// is lost. The credentials are still in the entry, and the next refresh takes them up.
func TestTakeUpSurvivesALostAnswer(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	e.approve()
	e.central.LoseResponse = map[string]int{"PUT /api/dataStore/facility-hub-registry/" + h.HubID(): 1}
	h.RefreshCatalog(context.Background())
	h.RefreshCatalog(context.Background())
	if h.Runtime().TokenOwner() != h.HubID() {
		t.Fatal("the account was lost with the answer")
	}
}

// Review 4, I1: the code covers the facility too. Someone who changes the facility in
// the registry entry cannot get the account created for another facility.
func TestApproveCodeCoversTheFacility(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	reg := e.central.DataStoreValue("facility-hub-registry", h.HubID())
	reg["facility"] = fc.OtherFacility
	b, _ := json.Marshal(reg)
	e.central.DataStore["facility-hub-registry"][h.HubID()] = b
	if _, err := e.approveWith(h.AccountCode()); err == nil || e.central.Users[h.HubID()] != nil {
		t.Fatalf("approved for another facility: %v", err)
	}
}

// Review 4, I1: an account the hub refuses (here one with authorities) does not leave
// it stuck: it asks again, so the national team sees it waiting.
func TestHubAsksAgainWhenTheAccountIsRefused(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	e.approve()
	e.central.Users[h.HubID()].Authorities = []string{"ALL"}
	h.RefreshCatalog(context.Background())
	if reg := e.central.DataStoreValue("facility-hub-registry", h.HubID()); reg["account"] != nil || reg["accountRequested"] != true {
		t.Fatalf("the hub did not ask again: %v", reg)
	}
	// Review 5: approving again says what is wrong with the account, instead of handing
	// over the same account the hub refuses; once corrected, it goes through.
	if _, err := e.approveWith(h.AccountCode()); err == nil || !strings.Contains(err.Error(), "authorities") {
		t.Fatalf("approved the refused account again: %v", err)
	}
	e.central.Users[h.HubID()].Authorities = []string{}
	e.adopt()
}

// Review 5: a disabled account (how a hub's account is revoked) is not handed over
// again by an approval that only replaces its password.
func TestApproveRefusesADisabledAccount(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	e.approve()
	e.central.Users[h.HubID()].Disabled = true
	h.RefreshCatalog(context.Background())
	if _, err := e.approveWith(h.AccountCode()); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("approved a disabled account: %v", err)
	}
}

// Review 5: the hub account role must have no authorities, or every hub refuses its
// account.
func TestApproveRefusesARoleWithAuthorities(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.central.RoleAuthorities = map[string][]string{"HubRoleAcct": {"F_TRACKED_ENTITY_ADD"}}
	if _, err := e.approveWith(e.app.Hub.AccountCode()); err == nil || e.central.Users[e.app.Hub.HubID()] != nil {
		t.Fatalf("approved with a role that has authorities: %v", err)
	}
}

// Review 4, I2: after a lost answer to a password change, an error while finding out
// whether central took it (here 503) is not read as "not taken".
func TestPasswordCheckSurvivesAnError(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	e.approve()
	e.central.LoseResponse = map[string]int{"PUT /api/me/changePassword": 1}
	h.RefreshCatalog(context.Background())
	if h.Runtime().HubPasswordNext == "" {
		t.Fatal("no password waiting to be confirmed")
	}
	e.central.FailPaths = map[string]int{"/api/me": 503}
	h.RefreshCatalog(context.Background())
	e.central.FailPaths = nil
	h.RefreshCatalog(context.Background())
	rt := h.Runtime()
	if rt.HubPassword != e.central.PasswordOf(h.HubID()) || h.Store.Meta("account_rotate") != "" {
		t.Fatalf("password matches %v, rotate %q", rt.HubPassword == e.central.PasswordOf(h.HubID()), h.Store.Meta("account_rotate"))
	}
}

// Review 4, I3: a new dataStore entry is public until its sharing is set, so the move
// key is written only once the entry is restricted.
func TestMoveKeyNeverPublic(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.central.FailPaths = map[string]int{"/api/sharing": 500}
	if _, err := e.approveWith(e.app.Hub.AccountCode()); err == nil {
		t.Fatal("approved without restricting the move key")
	}
	if v := e.central.DataStoreValue("facility-hub-keys", e.app.Hub.HubID()); v["moveKey"] != nil {
		t.Fatalf("the move key was written before its entry was restricted: %v", v)
	}
}
