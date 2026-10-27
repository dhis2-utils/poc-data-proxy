package e2e

import (
	"context"
	"testing"

	"github.com/dhis2/facility-hub/internal/store"
)

// A cycle pushes before it re-reads the catalog and users' access: on a link that
// drops every few seconds the refresh never finished, and nothing was pushed for as
// long as the flapping lasted (docs/ADVERSARIAL-TESTING.md, D11).
func TestPushBeforeAccessRefresh(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.app.Hub.Cfg.AccessRefreshInterval = 0 // a refresh in every cycle
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient("FirstTe0001", "FirstEn0001", "First", "", nil), 200)
	e.online()
	e.central.DropPaths = map[string]bool{"/api/programs": true} // the link drops during the refresh
	e.app.Worker.Cycle(context.Background())
	if n := e.app.Hub.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("pending after a cycle whose refresh failed: %d, want the push done first", n)
	}
	if e.central.Object("FirstTe0001") == nil {
		t.Fatal("the save is not on central")
	}
}
