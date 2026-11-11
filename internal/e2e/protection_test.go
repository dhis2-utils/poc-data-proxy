package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/core"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// Finding 5 (critical review 2026-10-27): on 2.42 any user may write the hub's dataStore
// entries until a country protects them with sharing (DEPLOYMENT.md, "Protect the data
// store namespaces"). The hub reports the entries it trusts that are still open, in
// its heartbeat and on its admin page; protected and missing ones are not listed.
func TestHeartbeatReportsUnprotectedEntries(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{
		"accounts":     json.RawMessage(`{}`),
		"central-move": json.RawMessage(`{"url":"https://new.example.org"}`),
	}
	protect(e, "facility-hub-admin", "accounts")
	e.sync()
	want := []string{"facility-hub-admin/central-move"} // the registry entry: protected at set-up
	if got := e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["unprotected"]; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("heartbeat unprotected %v, want %v", got, want)
	}
	a := e.web("hub_admin")
	a.login()
	if _, page := a.do("GET", "/hub/ui", nil); !strings.Contains(string(page), "facility-hub-admin/central-move") {
		t.Error("the admin page does not name the unprotected entry")
	}

	protect(e, "facility-hub-admin", "central-move")
	e.sync()
	if got, _ := e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["unprotected"].([]any); len(got) > 0 {
		t.Errorf("heartbeat unprotected %v once all are protected", got)
	}
}

// A new registry entry is writable by any user on 2.42. The hub owns its entry, and
// shares it as readable by all and writable by the hub owners, the hub accounts and
// the hub managers groups (facility-hub-admin/accounts), at set-up. Set-up needs the
// hub owners group named: before that a hub is not set up at all, and the facility is
// left as it was.
func TestHubProtectsItsRegistryEntry(t *testing.T) {
	accounts := `{"group":"HubGrpAccts","managers":"HubGrpMgrs","owners":"HubGrpOwnrs"}`
	unprotected := func(e *env) any {
		return e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["unprotected"]
	}
	write := func(e *env, user string) int {
		req, _ := http.NewRequest("PUT", e.cSrv.URL+"/api/dataStore/facility-hub-registry/"+e.app.Hub.HubID(), strings.NewReader(`{"status":"retired"}`))
		req.SetBasicAuth(user, fc.Password)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	t.Run("at set-up", func(t *testing.T) {
		e := newEnv(t)
		e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{"accounts": json.RawMessage(accounts)}
		protect(e, "facility-hub-admin", "accounts")
		e.central.Users["nurse_a"].Groups = []string{"HubGrpOwnrs"}
		e.setup()
		if got, _ := unprotected(e).([]any); len(got) > 0 {
			t.Errorf("heartbeat unprotected %v after set-up", got)
		}
		if code := write(e, "outsider_d"); code != http.StatusForbidden {
			t.Errorf("a user outside the groups wrote the entry: %d", code)
		}
		// A hub owner still takes the hub over: the take-over writes the entry.
		if err := e.app.Hub.TakeOverHubToken(context.Background(), "nurse_a", fc.Password); err != nil {
			t.Errorf("take-over by a hub owner: %v", err)
		}
		// Take-over, like set-up, needs the hub owners group named.
		e.central.DataStore["facility-hub-admin"]["accounts"] = json.RawMessage(`{"group":"HubGrpAccts"}`)
		if err := e.app.Hub.TakeOverHubToken(context.Background(), "hub_admin", fc.Password); !errors.Is(err, core.ErrNoOwnersGroup) {
			t.Errorf("take-over without a hub owners group: %v", err)
		}
	})

	// DHIS2 (2.42, 2.43) drops, with a 200, a group the account that shares cannot see.
	// The hub says so, and shares the entry again once the group is visible.
	t.Run("hub accounts group dropped", func(t *testing.T) {
		e := newEnv(t)
		e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{"accounts": json.RawMessage(accounts)}
		protect(e, "facility-hub-admin", "accounts")
		e.central.UnseenGroups = map[string]bool{"HubGrpAccts": true}
		e.setup()
		dropped := func() string {
			return fmt.Sprint(e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["registryDropped"])
		}
		if got := dropped(); got != "[HubGrpAccts]" {
			t.Errorf("heartbeat registryDropped %s, want [HubGrpAccts]", got)
		}
		a := e.web("hub_admin")
		a.login()
		if _, page := a.do("GET", "/hub/ui", nil); !strings.Contains(string(page), "Central dropped groups from this hub") {
			t.Error("the admin page does not say central dropped the group")
		}
		// A superuser saves the groups in the app's Settings: the group becomes visible.
		e.central.UnseenGroups = nil
		e.sync()
		if got := dropped(); got != "[]" {
			t.Errorf("heartbeat registryDropped %s once the group is visible", got)
		}
	})

	t.Run("no hub owners group named", func(t *testing.T) {
		e := newEnv(t)
		e.central.DataStore["facility-hub-admin"]["accounts"] = json.RawMessage(`{"group":"HubGrpAccts"}`)
		c := e.web("hub_admin")
		resp, err := c.postForm("/hub/setup", url.Values{"step": {"login"}, "central_url": {e.cSrv.URL},
			"username": {"hub_admin"}, "password": {fc.Password}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if e.app.Hub.Mode() != "setup" || !strings.Contains(string(b), "name the hub owners group") {
			t.Fatalf("set up without a hub owners group: mode %q, %.300s", e.app.Hub.Mode(), b)
		}
		if n := len(e.central.DataStore["facility-hub-registry"]); n > 0 || e.central.TokensOf("hub_admin") > 0 {
			t.Errorf("a refused set-up left %d registry entries, %d tokens", n, e.central.TokensOf("hub_admin"))
		}
	})
}

// An approved hub names, in its heartbeat and on its admin page, the instructions it
// ignored for want of a valid code (review R-07); a signed one clears the list.
func TestHeartbeatReportsUnsignedEntries(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.giveMoveKey()
	e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{"central-move": json.RawMessage(`{"url":"https://new.example.org"}`)}
	e.app.Hub.RefreshCatalog(context.Background())
	e.sync()
	if got := e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["unsigned"]; fmt.Sprint(got) != "[facility-hub-admin/central-move]" {
		t.Errorf("heartbeat unsigned %v", got)
	}
	a := e.web("hub_admin")
	a.login()
	if _, page := a.do("GET", "/hub/ui", nil); !strings.Contains(string(page), "not signed for this hub") {
		t.Error("the admin page does not say what the hub ignored")
	}
	e.putSigned("facility-hub-admin/central-move", `{"url":"https://new.example.org"}`)
	e.app.Hub.RefreshCatalog(context.Background())
	e.sync()
	if got, _ := e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["unsigned"].([]any); len(got) > 0 {
		t.Errorf("heartbeat unsigned %v once the entry is signed", got)
	}
}
