package e2e

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// With local reads on, the hub answers a user's metadata requests from their copy
// while online, until it learns of a change: on central (the hourly check), from the
// Facility Hub app (refresh on all hubs), on the hub page, from a write through the
// hub, or in the user's access. Offline it answers from the copy whatever happened
// (core/metadata.go, proxy.localCopy).
func TestMetadataFromHubWhileOnline(t *testing.T) {
	e := newEnv(t)
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"localReads": json.RawMessage(`{"programs":["` + fc.Program + `"]}`)}
	e.setup()
	e.app.Hub.Cfg.AccessRefreshInterval = 0 // the hourly check in every cycle
	a := e.web("nurse_a")
	a.login()
	e.sync() // the first check only notes the time

	const path = "/api/programs/" + fc.Program + "?fields=id,name"
	asked := func() int { return e.central.RequestCount("GET /api/programs/" + fc.Program) }
	open := func(what string, wantCentral bool) {
		t.Helper()
		before := asked()
		if st, b := a.do("GET", path, nil); st != 200 || !strings.Contains(string(b), fc.Program) {
			t.Fatalf("%s: %d %s", what, st, b)
		}
		if went := asked() > before; went != wantCentral {
			t.Errorf("%s: asked central %v, want %v", what, went, wantCentral)
		}
	}
	open("first time, no copy", true)
	open("again: from the copy", false)
	before := e.central.RequestCount("GET /api/me")
	a.do("GET", "/api/me?fields=id,username", nil)
	if e.central.RequestCount("GET /api/me") != before {
		t.Error("/api/me went to central: the copy from the login was not used")
	}

	time.Sleep(10 * time.Millisecond)
	e.central.MetadataChangedAt = time.Now().UTC()
	e.sync()
	open("changed on central", true)
	open("after the change, from the new copy", false)
	// Checks overlap by ten minutes (for a hub clock ahead of central's): a change in
	// that window is seen again by the next check. Here they run seconds apart.
	e.central.MetadataChangedAt = time.Time{}

	e.central.DataStore["facility-hub-config"]["metadataReload"] = json.RawMessage(`{"at":"2026-11-06T13:00:00Z"}`)
	e.sync()
	open("refresh on all hubs", true)
	e.sync() // the same value again: nothing new
	open("the same national refresh, seen before", false)

	resp, err := a.postForm("/hub/ui/refresh-metadata", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	open("refresh on the hub page", true)

	if st, b := a.do("PUT", "/api/dataStore/capture/settings", map[string]any{"x": 1}); st/100 != 2 {
		t.Fatalf("data store write: %d %s", st, b)
	}
	open("a data store write through the hub", true)
	open("then from the copy", false)

	e.central.Users["nurse_a"].Groups = append(e.central.Users["nurse_a"].Groups, "HubGrpOther")
	e.sync()
	open("the user's access changed", true)

	e.central.MetadataChangedAt = time.Now().UTC()
	e.offline()
	if st, b := a.do("GET", path, nil); st != 200 || !strings.Contains(string(b), fc.Program) {
		t.Fatalf("offline after a change: %d %s, want the copy", st, b)
	}
}

// eRegisters asks /api/me for its org units with their programs
// (organisationUnits[id,name,path,programs[id,name],...]). The copy of /api/me kept at
// login names each org unit by id only, so it cannot give those fields; answered from
// it with local reads on, eRegisters 1.1.9 failed to start ("Cannot read properties of
// undefined (reading 'length')", unit.programs.length; found by TestCentralWork on
// 2.43, 9 November). Such a request goes to central.
func TestMeNestedFieldsWhileOnline(t *testing.T) {
	e := newEnv(t)
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"localReads": json.RawMessage(`{"programs":["` + fc.Program + `"]}`)}
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()

	before := e.central.RequestCount("GET /api/me")
	if st, b := a.do("GET", "/api/me?fields=id,username", nil); st != 200 || !strings.Contains(string(b), "nurse_a") {
		t.Fatalf("/api/me?fields=id,username: %d %s", st, b)
	}
	if e.central.RequestCount("GET /api/me") != before {
		t.Error("top-level fields went to central: the login copy can answer them")
	}
	if st, b := a.do("GET", "/api/me?fields=id,organisationUnits[id,name,path,programs[id,name]]", nil); st != 200 {
		t.Fatalf("/api/me with org units' programs: %d %s", st, b)
	}
	if e.central.RequestCount("GET /api/me") != before+1 {
		t.Error("/api/me with fields of its org units was answered from the login copy, which lacks them")
	}
}
