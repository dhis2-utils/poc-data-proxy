package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

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
