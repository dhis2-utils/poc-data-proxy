package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/app"
	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// managersGroup names nurse_b a hub manager, as facility-hub-admin/accounts does.
func managersGroup(e *env) {
	if e.central.DataStore["facility-hub-admin"] == nil {
		e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{}
	}
	e.central.DataStore["facility-hub-admin"]["accounts"] = json.RawMessage(`{"managers":"HubGrpMgrs"}`)
	e.central.Users["nurse_b"].Groups = append(e.central.Users["nurse_b"].Groups, "HubGrpMgrs")
}

func registryEntry(e *env) map[string]any {
	var reg map[string]any
	json.Unmarshal(e.central.DataStore["facility-hub-registry"][e.app.Hub.HubID()], &reg)
	return reg
}

func setRegistry(e *env, reg map[string]any) {
	b, _ := json.Marshal(reg)
	e.central.DataStore["facility-hub-registry"][e.app.Hub.HubID()] = b
}

func deleteTokenAs(e *env, user, uid string) int {
	req, _ := http.NewRequest("DELETE", e.cSrv.URL+"/api/apiToken/"+uid, nil)
	req.SetBasicAuth(user, fc.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// On DHIS2 2.42 nobody but a token's owner can delete it, not even a superuser, and
// disabling the owner does not stop it (FINDINGS.md). The hub shares each token it
// makes with the hub managers, and lists them in its registry entry, so the Facility
// Hub app can revoke a stolen hub's tokens.
func TestTokensSharedWithManagersAndListed(t *testing.T) {
	e := newEnv(t)
	managersGroup(e)
	e.setup()
	e.web("nurse_a").login()
	e.sync()
	u, err := e.app.Hub.Store.UserByName("nurse_a")
	if err != nil || u.PATUID == "" {
		t.Fatalf("nurse_a has no token: %v", err)
	}
	listed := registryEntry(e)["tokens"]
	var uids []string
	b, _ := json.Marshal(listed)
	json.Unmarshal(b, &uids)
	if !slices.Contains(uids, u.PATUID) || !slices.Contains(uids, e.app.Hub.Runtime().HubTokenUID) {
		t.Fatalf("registry lists %v; want nurse_a's %s and the hub's %s", uids, u.PATUID, e.app.Hub.Runtime().HubTokenUID)
	}
	if st := deleteTokenAs(e, "clerk_c", u.PATUID); st == 200 {
		t.Fatal("a user who is not a hub manager deleted nurse_a's token")
	}
	if st := deleteTokenAs(e, "nurse_b", u.PATUID); st != 200 {
		t.Fatalf("a hub manager could not delete nurse_a's token: %d", st)
	}
	key, _ := e.app.Hub.Store.PAT(u)
	if e.central.TokenValid(key) {
		t.Fatal("the deleted token still works")
	}
}

// A hub whose tokens were revoked in the Facility Hub app deletes what it still holds,
// stops serving offline data, and makes no new tokens.
func TestRevokedHubDropsItsTokens(t *testing.T) {
	e := newEnv(t)
	e.setup()
	nurse := e.web("nurse_a")
	nurse.login()
	e.sync()
	u, _ := e.app.Hub.Store.UserByName("nurse_a")
	userKey, _ := e.app.Hub.Store.PAT(u)
	hubKey := e.app.Hub.Runtime().HubToken
	reg := registryEntry(e)
	reg["status"], reg["revokedBy"], reg["revokedAt"] = "revoked", "hub_admin", time.Now().UTC().Format(time.RFC3339)
	setRegistry(e, reg)
	e.sync()
	if !e.app.Hub.Revoked() {
		t.Fatal("the hub did not take up its revocation")
	}
	if e.central.TokenValid(userKey) || e.central.TokenValid(hubKey) {
		t.Fatal("tokens of a revoked hub still work")
	}
	if u, _ := e.app.Hub.Store.UserByName("nurse_a"); u.HasPAT {
		t.Fatal("a revoked hub keeps its users' tokens")
	}
	e.offline()
	if st, _ := nurse.do("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnit="+fc.Facility, nil); st == 200 {
		t.Fatal("a revoked hub serves offline data")
	}
	e.online()
	e.sync() // nothing left to sync with; must not fail
	// Staff still log in, to reach the admin page and reset the hub, but get no token.
	e.web("nurse_b").do("POST", "/api/auth/login", map[string]string{"username": "nurse_b", "password": fc.Password})
	if u, _ := e.app.Hub.Store.UserByName("nurse_b"); u != nil && u.HasPAT {
		t.Fatal("a revoked hub made a token")
	}
}

// The country sets how long tokens live, in the Facility Hub app's settings: new tokens
// take it, and one with more life left than the setting allows is replaced early.
func TestTokenLifeFromCentral(t *testing.T) {
	e := newEnv(t)
	e.setup()
	nurse := e.web("nurse_a")
	nurse.login()
	life := func() time.Duration {
		u, _ := e.app.Hub.Store.UserByName("nurse_a")
		exp, _ := time.Parse(time.RFC3339, u.PATExpires)
		return time.Until(exp)
	}
	if d := life(); d < 80*24*time.Hour {
		t.Fatalf("default token life %v, want 90 days", d)
	}
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{"tokens": json.RawMessage(`{"expiryDays":30}`)}
	if err := e.app.Hub.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.app.Hub.TokenLife(); got != 30*24*time.Hour {
		t.Fatalf("token life %v after the setting, want 30 days", got)
	}
	e.web("nurse_a").login()
	if d := life(); d > 31*24*time.Hour || d < 29*24*time.Hour {
		t.Fatalf("token renewed to %v, want 30 days", d)
	}
	e.central.DataStore["facility-hub-config"]["tokens"] = json.RawMessage(`{"expiryDays":1}`)
	e.app.Hub.RefreshCatalog(context.Background())
	if got := e.app.Hub.TokenLife(); got != 7*24*time.Hour {
		t.Fatalf("token life %v for a setting below the minimum, want 7 days", got)
	}
}

// A short token life is not renewed at every login and cycle: a token is replaced in
// the last half of its life (at most a week before its end), not as soon as it is made
// (bug review 2026-10-20).
func TestShortTokenLifeNotRenewedEachLogin(t *testing.T) {
	e := newEnv(t)
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{"tokens": json.RawMessage(`{"expiryDays":7}`)}
	e.setup()
	e.app.Hub.RefreshCatalog(context.Background())
	e.sync() // the setup's 90-day hub token is replaced by a 7-day one, once
	e.web("nurse_a").login()
	u, _ := e.app.Hub.Store.UserByName("nurse_a")
	hubTok := e.app.Hub.Runtime().HubTokenUID
	e.web("nurse_a").login()
	e.sync()
	e.sync()
	if u2, _ := e.app.Hub.Store.UserByName("nurse_a"); u2.PATUID != u.PATUID {
		t.Fatalf("a 7-day token was renewed at the next login: %s -> %s", u.PATUID, u2.PATUID)
	}
	if got := e.app.Hub.Runtime().HubTokenUID; got != hubTok {
		t.Fatalf("a 7-day hub token was renewed at the next cycle: %s -> %s", hubTok, got)
	}
}

// As the Facility Hub app revokes: a hub manager deletes every listed token, the hub's
// own included, then marks the entry revoked. The hub cannot read its entry any more,
// but a staff login that would make a new token finds out first, and makes none
// (bug review 2026-10-20).
func TestRevokedFromAppMakesNoNewTokens(t *testing.T) {
	e := newEnv(t)
	managersGroup(e)
	e.setup()
	e.web("nurse_a").login()
	e.sync()
	reg := registryEntry(e)
	for _, uid := range reg["tokens"].([]any) {
		if st := deleteTokenAs(e, "nurse_b", uid.(string)); st != 200 {
			t.Fatalf("the manager could not delete %v: %d", uid, st)
		}
	}
	reg = registryEntry(e)
	reg["status"], reg["revokedBy"] = "revoked", "nurse_b"
	setRegistry(e, reg)
	e.app.Worker.Cycle(context.Background()) // its token is gone: it learns nothing here
	before := e.central.TokensOf("nurse_a")
	e.web("nurse_a").do("POST", "/api/auth/login", map[string]string{"username": "nurse_a", "password": fc.Password})
	if !e.app.Hub.Revoked() {
		t.Fatal("the hub did not find out at the login that it was revoked")
	}
	if n := e.central.TokensOf("nurse_a"); n != before {
		t.Fatalf("a revoked hub made a token at a login: %d -> %d", before, n)
	}
}

// A revoked hub can send nothing, so a reset that waits for its outbox would never
// come: it resets with RESET AND DISCARD, and says so without it (bug review
// 2026-10-20).
func TestRevokedHubResets(t *testing.T) {
	e := newEnv(t)
	e.setup()
	nurse := e.web("nurse_a")
	nurse.login()
	e.offline()
	nurse.json("POST", postSync, newPatient("RevTe000001", "RevEn000001", "Revoked", "NAT-7171", nil), 200)
	e.online()
	e.app.Hub.SetRevoked()
	e.app.Hub.DropTokens(context.Background())
	if err := app.Reset(e.app.Hub, e.app.Worker, "nurse_a", false); err == nil || !strings.Contains(err.Error(), "RESET AND DISCARD") {
		t.Fatalf("reset of a revoked hub with unsent saves: %v", err)
	}
	if err := app.Reset(e.app.Hub, e.app.Worker, "nurse_a", true); err != nil {
		t.Fatalf("reset and discard of a revoked hub: %v", err)
	}
	if e.app.Hub.Mode() != "setup" || e.app.Hub.Revoked() {
		t.Fatalf("after the reset: mode %s, revoked %v", e.app.Hub.Mode(), e.app.Hub.Revoked())
	}
}

// The heartbeat goes to central once an hour, not with every sync (13.6 KB with its
// 90-day history: 1.3 MB a day per hub at every 15 minutes, docs/reviews/KISS-
// 2026-10-20.md), and at the first sync after a start. Every cycle still counts in the
// history.
func TestHeartbeatHourly(t *testing.T) {
	e := newEnv(t)
	e.cfgHook = func(c *config.Config) { c.HeartbeatInterval = time.Hour }
	e.stop()
	e.start()
	key := "PUT /api/dataStore/facility-hub-status/" + e.app.Hub.HubID()
	before := e.central.RequestCount(key)
	e.setup() // its first cycle sends one
	e.sync()
	e.sync()
	e.sync()
	if n := e.central.RequestCount(key) - before; n != 1 {
		t.Fatalf("%d heartbeats in four cycles within an hour, want 1", n)
	}
	if h := e.app.Hub.History(); len(h) == 0 || h[len(h)-1].Syncs < 4 {
		t.Fatalf("the history does not count every cycle: %+v", h)
	}
	e.stop()
	e.start()
	e.web("hub_admin").login()
	e.sync()
	if n := e.central.RequestCount(key) - before; n != 2 {
		t.Fatalf("no heartbeat at the first sync after a start: %d", n)
	}
}
