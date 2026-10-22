package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// Offline, a read the hub cannot answer ("needs connection": a screen nobody opened
// online) is counted per endpoint, and the heartbeat carries the top ones, so the
// national team sees how often staff meet it and on which screens. Endpoints only:
// identifiers become {uid}, parameters are names without values. Answered reads and
// refused saves are not counted.
func TestOfflineMissesInHeartbeat(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	b := e.web("nurse_b")
	b.login()
	programs := "/api/programs?fields=id,displayName&paging=false"
	a.do("GET", programs, nil)
	e.offline()
	if st, _ := a.do("GET", programs, nil); st != 200 {
		t.Fatalf("nurse_a's own copy offline: %d", st)
	}
	for i := 0; i < 2; i++ {
		if st, _ := b.do("GET", programs, nil); st != 503 {
			t.Fatalf("nurse_b, never loaded online: %d", st)
		}
	}
	b.do("GET", "/api/optionSets/AbCdEfGhIjK?fields=id", nil)
	e.online()
	e.sync()

	var hb struct {
		Misses []struct {
			Request string `json:"request"`
			Count   int    `json:"count"`
		} `json:"offlineMisses"`
		Total int `json:"offlineMissesTotal"`
	}
	raw, _ := json.Marshal(e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID()))
	if err := json.Unmarshal(raw, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.Total != 3 || len(hb.Misses) != 2 {
		t.Fatalf("misses: %+v", hb)
	}
	if m := hb.Misses[0]; !strings.HasPrefix(m.Request, "GET /api/programs ?fields,paging") || m.Count != 2 {
		t.Errorf("the most frequent miss first, by endpoint: %+v", m)
	}
	if m := hb.Misses[1]; !strings.HasPrefix(m.Request, "GET /api/optionSets/{uid} ?fields") || strings.Contains(string(raw), "AbCdEfGhIjK") {
		t.Errorf("identifiers stay out of the heartbeat: %+v", m)
	}
}
