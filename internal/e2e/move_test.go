package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// moveEnv: central reachable at an old address, which the test can take down or turn
// into a permanent redirect, and at a new one (the same DHIS2, so it holds this hub's
// registry entry).
func moveEnv(t *testing.T) (e *env, newURL string, down, redirect *atomic.Bool) {
	t.Helper()
	e = &env{t: t, central: fc.New(), dir: t.TempDir()}
	down, redirect = &atomic.Bool{}, &atomic.Bool{}
	newSrv := httptest.NewServer(e.central)
	t.Cleanup(newSrv.Close)
	e.cSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case down.Load():
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					conn.Close()
					return
				}
			}
		case redirect.Load():
			http.Redirect(w, r, newSrv.URL+r.URL.RequestURI(), http.StatusPermanentRedirect)
		default:
			e.central.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(e.cSrv.Close)
	e.cfgHook = func(c *config.Config) { c.CentralURL = e.cSrv.URL }
	e.start()
	e.setup()
	return e, newSrv.URL, down, redirect
}

func (e *env) cycleOffline() {
	for i := 0; i < 2; i++ { // two failed probes mark the hub offline
		e.app.Hub.Monitor.Probe(context.Background(), e.app.Hub.Central.BaseURL())
	}
	e.app.Worker.Cycle(context.Background())
}

// DEPLOYMENT.md section 6: the national team announces central's new address on the
// current central; when the old one stops answering, hubs switch to the new one,
// which must hold this hub's registry entry. Nothing is sent to it before that check
// but the hub's own token, which the current central vouched for by announcing it.
func TestCentralMoveAnnounced(t *testing.T) {
	e, newURL, down, _ := moveEnv(t)
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{"central-move": json.RawMessage(`{"url":"` + newURL + `"}`)}
	if err := e.app.Hub.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := e.app.Hub.Central.BaseURL(); got != e.cSrv.URL {
		t.Fatalf("switched while the old address still answers: %s", got)
	}
	down.Store(true)
	e.cycleOffline()
	if got := e.app.Hub.Central.BaseURL(); got != newURL || !e.app.Hub.Online() {
		t.Fatalf("after the old address died: %s, online %v", got, e.app.Hub.Online())
	}
	e.app.Hub.Lock() // a reboot keeps the new address
	if got := e.app.Hub.Store.Meta("central_url"); got != newURL {
		t.Fatalf("stored address %q", got)
	}
}

// An announced address that is not the same DHIS2 (no registry entry for this hub) is
// not used: the hub keeps waiting, offline, with its data.
func TestCentralMoveRefusesAnotherServer(t *testing.T) {
	e, _, down, _ := moveEnv(t)
	other := httptest.NewServer(fc.New())
	t.Cleanup(other.Close)
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{"central-move": json.RawMessage(`{"url":"` + other.URL + `"}`)}
	e.app.Hub.RefreshCatalog(context.Background())
	down.Store(true)
	e.cycleOffline()
	if got := e.app.Hub.Central.BaseURL(); got != e.cSrv.URL || e.app.Hub.Online() {
		t.Fatalf("switched to another server: %s", got)
	}
}

// The old address answers with a permanent redirect to the new one: the hub checks the
// new one and switches.
func TestCentralMoveRedirect(t *testing.T) {
	e, newURL, _, redirect := moveEnv(t)
	redirect.Store(true)
	e.cycleOffline()
	if got := e.app.Hub.Central.BaseURL(); got != newURL || !e.app.Hub.Online() {
		t.Fatalf("after a permanent redirect: %s, online %v", got, e.app.Hub.Online())
	}
}
