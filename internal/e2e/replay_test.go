package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// At a staff member's online login, the hub loads in the background, with their own
// token, the metadata and settings requests the facility's apps were seen making
// (any user's cached requests), so they can open those screens offline without having
// opened them online first. Central answers as them: their copy is their own (nurse_b
// cannot write the third stage; nurse_a can). Not repeated: data (tracker, aggregate
// values, analytics), generated IDs, validation, requests naming another user, and the
// hub's own answers. Once a day per user.
func TestReplayAtLogin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()

	screens := []string{
		"/api/programs?fields=id,programStages[id,access]&paging=false",
		"/api/userSettings",
		"/api/42/system/info?fields=version",
		"/api/organisationUnits/" + fc.Facility + "?fields=id,displayName",
		"/api/dataStore/capture/missingKey", // a probe central answers 404: an answer too
	}
	notReplayed := []string{
		"/api/users/HubUsrNursA?fields=id,displayName",                                   // names nurse_a
		"/api/userLookup?query=nurse&user=nurse_a",                                       // names a user
		"/api/trackedEntityAttributes/" + fc.AttrTBID + "/generate?ORG_UNIT_CODE=OU_559", // reserves a value
		"/api/dataValueSets?dataSet=AbCdEfGhIjK&period=202610&orgUnit=" + fc.Facility,    // data
		"/api/analytics?dimension=dx:AbCdEfGhIjK&filter=pe:LAST_MONTH",                   // data
		"/api/42/validation/dataSet/AbCdEfGhIjK?pe=202610&ou=" + fc.Facility,             // central's check of data
	}
	for _, p := range append(screens, notReplayed...) {
		a.do("GET", p, nil)
	}
	before := map[string]int{}
	for _, p := range notReplayed {
		before[pathOf(p)] = e.central.RequestCount("GET " + pathOf(p))
	}

	b := e.web("nurse_b")
	b.login() // online, no screens opened
	e.app.Proxy.WaitReplays()
	for _, p := range notReplayed {
		if n := e.central.RequestCount("GET " + pathOf(p)); n != before[pathOf(p)] {
			t.Errorf("%s repeated for nurse_b: %d requests, %d before", p, n, before[pathOf(p)])
		}
	}
	probe := "GET /api/dataStore/capture/missingKey" // asked by nothing but the replay
	loaded := e.central.RequestCount(probe)
	if loaded != 2 {
		t.Errorf("not loaded for nurse_b: %d requests", loaded)
	}
	b.login() // the same day: not again
	e.app.Proxy.WaitReplays()
	if n := e.central.RequestCount(probe); n != loaded {
		t.Errorf("a second login the same day loaded again: %d requests, %d before", n, loaded)
	}

	e.offline()
	for _, p := range screens {
		if st, _ := b.do("GET", p, nil); st != 200 && st != 404 {
			t.Errorf("nurse_b offline, %s: %d", p, st)
		}
	}
	_, body := b.do("GET", screens[0], nil)
	var progs struct {
		Programs []struct {
			ID     string `json:"id"`
			Stages []struct {
				ID     string `json:"id"`
				Access struct {
					Data struct{ Write bool } `json:"data"`
				} `json:"access"`
			} `json:"programStages"`
		} `json:"programs"`
	}
	json.Unmarshal(body, &progs)
	found := false
	for _, p := range progs.Programs {
		for _, s := range p.Stages {
			if s.ID == fc.Stage3 {
				found = true
				if s.Access.Data.Write {
					t.Errorf("nurse_b was served nurse_a's copy: stage 3 writable")
				}
			}
		}
	}
	if !found {
		t.Errorf("nurse_b's program list offline: %s", body)
	}
	for _, p := range notReplayed {
		if strings.Contains(p, "/generate") {
			continue // the hub answers from its reserved values
		}
		if st, _ := b.do("GET", p, nil); st != 503 {
			t.Errorf("nurse_b offline, %s (not repeated): %d", p, st)
		}
	}
}

func pathOf(uri string) string { p, _, _ := strings.Cut(uri, "?"); return p }

// Capture's layout entry is loaded at every user's browser login, though no one was
// seen asking for it: only the enrollment dashboard reads it, and Capture 107 crashes
// offline without it (PENDING.md, item 25). Once a day, as the rest of the replay.
func TestReplayLoadsCaptureLayout(t *testing.T) {
	e := newEnv(t)
	e.setup()
	layout := "/api/42/dataStore/capture?fields=."
	b := e.web("nurse_b")
	b.login() // online; no dashboard opened by anyone
	e.app.Proxy.WaitReplays()
	loaded := e.central.RequestCount("GET /api/42/dataStore/capture")
	if loaded != 1 {
		t.Errorf("the layout entry was asked %d times at the login, want 1", loaded)
	}
	b.login() // the same day: not again
	e.app.Proxy.WaitReplays()
	if n := e.central.RequestCount("GET /api/42/dataStore/capture"); n != loaded {
		t.Errorf("a second login the same day loaded it again: %d requests, %d before", n, loaded)
	}
	e.offline()
	if st, body := b.do("GET", layout, nil); st != 200 {
		t.Errorf("nurse_b offline, %s: %d %s", layout, st, body)
	}
}
