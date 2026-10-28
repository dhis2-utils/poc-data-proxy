package e2e

import (
	"encoding/json"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// The heartbeat's daily history counts the saves passed straight to central while
// online and those stored on the hub and sent later from the outbox, so the Facility
// Hub app shows how much a facility works through the hub offline.
func TestSavesCountedInHeartbeat(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	a.json("POST", postSync, newPatient("CountTe0001", "CountEn0001", "Online", "NAT-8001", nil), 200)
	a.json("GET", "/api/tracker/trackedEntities/CountTe0001?program="+fc.Program, nil, 200) // a read: not a save
	e.offline()
	a.json("POST", postSync, newPatient("CountTe0002", "CountEn0002", "Offline", "NAT-8002", nil), 200)
	a.json("POST", postSync, newPatient("CountTe0003", "CountEn0003", "Offline", "NAT-8003", nil), 200)
	e.online()
	e.sync()

	var hb struct {
		History []struct {
			Date         string `json:"date"`
			SavedOnline  int    `json:"savedOnline"`
			SavedOffline int    `json:"savedOffline"`
		} `json:"history"`
	}
	raw, _ := json.Marshal(e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID()))
	if err := json.Unmarshal(raw, &hb); err != nil || len(hb.History) == 0 {
		t.Fatalf("heartbeat: %s %v", raw, err)
	}
	today := hb.History[len(hb.History)-1]
	if today.Date != time.Now().UTC().Format(time.DateOnly) || today.SavedOnline != 1 || today.SavedOffline != 2 {
		t.Fatalf("today: %+v; want 1 online, 2 offline", today)
	}
}
