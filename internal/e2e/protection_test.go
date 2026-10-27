package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
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
	registry := "facility-hub-registry/" + e.app.Hub.HubID()
	want := []string{"facility-hub-admin/central-move", registry}
	if got := e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["unprotected"]; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("heartbeat unprotected %v, want %v", got, want)
	}
	a := e.web("hub_admin")
	a.login()
	if _, page := a.do("GET", "/hub/ui", nil); !strings.Contains(string(page), "facility-hub-admin/central-move") {
		t.Error("the admin page does not name the unprotected entry")
	}

	protect(e, "facility-hub-admin", "central-move")
	protect(e, "facility-hub-registry", e.app.Hub.HubID())
	e.sync()
	if got, _ := e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["unprotected"].([]any); len(got) > 0 {
		t.Errorf("heartbeat unprotected %v once all are protected", got)
	}
}
