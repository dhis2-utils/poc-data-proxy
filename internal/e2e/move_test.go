package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	e.cfgHook = func(c *config.Config) { c.CentralURL = e.cSrv.URL; c.CentralMoveAfter = 0 }
	e.start()
	e.setup()
	e.app.Hub.Cfg.CentralURL = "" // the generic image: no address pinned in hub.yaml
	return e, newSrv.URL, down, redirect
}

func announce(e *env, url string) {
	e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{"central-move": e.signed("facility-hub-admin/central-move", `{"url":"`+url+`"}`)}
	protect(e, "facility-hub-admin", "central-move")
	e.app.Hub.RefreshCatalog(context.Background())
}

// protect shares a dataStore entry as the Facility Hub app does: readable by all,
// writable by administrators only.
func protect(e *env, ns, key string) {
	e.t.Helper()
	var meta struct{ ID string }
	do := func(method, path, body string, out any) {
		req, _ := http.NewRequest(method, e.cSrv.URL+path, strings.NewReader(body))
		req.SetBasicAuth("hub_admin", fc.Password)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			e.t.Fatalf("%s %s: %v %v", method, path, resp, err)
		}
		defer resp.Body.Close()
		if out != nil {
			json.NewDecoder(resp.Body).Decode(out)
		}
	}
	do("GET", "/api/dataStore/"+ns+"/"+key+"/metaData", "", &meta)
	do("POST", "/api/sharing?type=dataStore&id="+meta.ID, `{"object":{"publicAccess":"r-------","userAccesses":[],"userGroupAccesses":[]}}`, nil)
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
	announce(e, newURL)
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
	announce(e, other.URL)
	down.Store(true)
	e.cycleOffline()
	if got := e.app.Hub.Central.BaseURL(); got != e.cSrv.URL || e.app.Hub.Online() {
		t.Fatalf("switched to another server: %s", got)
	}
}

// Review fixes: no move for a hub pinned to its address in hub.yaml; none while the old
// address has been down only briefly; an announcement in a namespace hub owners can
// write is not taken, nor one a hub without a move key cannot check.
func TestCentralMoveGuards(t *testing.T) {
	t.Run("pinned", func(t *testing.T) {
		e, newURL, down, _ := moveEnv(t)
		e.app.Hub.Cfg.CentralURL = e.cSrv.URL
		announce(e, newURL)
		down.Store(true)
		e.cycleOffline()
		if got := e.app.Hub.Central.BaseURL(); got != e.cSrv.URL {
			t.Fatalf("a pinned hub moved to %s", got)
		}
	})
	t.Run("brief outage", func(t *testing.T) {
		e, newURL, down, _ := moveEnv(t)
		e.app.Hub.Cfg.CentralMoveAfter = time.Hour
		announce(e, newURL)
		down.Store(true)
		e.cycleOffline()
		if got := e.app.Hub.Central.BaseURL(); got != e.cSrv.URL {
			t.Fatalf("moved after a brief outage: %s", got)
		}
	})
	t.Run("hub owners' namespace", func(t *testing.T) {
		e, newURL, down, _ := moveEnv(t)
		e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{"central-move": json.RawMessage(`{"url":"` + newURL + `"}`)}
		e.app.Hub.RefreshCatalog(context.Background())
		down.Store(true)
		e.cycleOffline()
		if got := e.app.Hub.Central.BaseURL(); got != e.cSrv.URL {
			t.Fatalf("moved on an announcement any hub owner can write: %s", got)
		}
	})
	t.Run("not approved", func(t *testing.T) {
		// A hub without a move key cannot check a code: it takes no announcement.
		e, newURL, down, _ := moveEnv(t)
		e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{"central-move": json.RawMessage(`{"url":"` + newURL + `"}`)}
		protect(e, "facility-hub-admin", "central-move")
		e.app.Hub.RefreshCatalog(context.Background())
		down.Store(true)
		e.cycleOffline()
		if got := e.app.Hub.Central.BaseURL(); got != e.cSrv.URL {
			t.Fatalf("a hub without a move key moved: %s", got)
		}
	})
	t.Run("not yet", func(t *testing.T) {
		e, newURL, down, _ := moveEnv(t)
		e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{"central-move": e.signed("facility-hub-admin/central-move",
			`{"url":"`+newURL+`","from":"`+time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339)+`"}`)}
		protect(e, "facility-hub-admin", "central-move")
		e.app.Hub.RefreshCatalog(context.Background())
		down.Store(true)
		e.cycleOffline()
		if got := e.app.Hub.Central.BaseURL(); got != e.cSrv.URL {
			t.Fatalf("moved before the announced time: %s", got)
		}
	})
}

// A permanent redirect counts only from an https address (a captive portal on plain
// http must not move a hub), after several in a row; the hub checks the new address
// and switches.
func TestCentralMoveRedirect(t *testing.T) {
	e := &env{t: t, central: fc.New(), dir: t.TempDir()}
	redirect := &atomic.Bool{}
	newSrv := httptest.NewTLSServer(e.central)
	t.Cleanup(newSrv.Close)
	e.cSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect.Load() {
			http.Redirect(w, r, newSrv.URL+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		e.central.ServeHTTP(w, r)
	}))
	t.Cleanup(e.cSrv.Close)
	e.cfgHook = func(c *config.Config) { c.CentralURL = e.cSrv.URL; c.CentralMoveAfter = 0 }
	e.start()
	tlsCfg := e.cSrv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	e.app.Hub.Central.HTTP.Transport.(*http.Transport).TLSClientConfig = tlsCfg
	probe := e.cSrv.Client()
	probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	e.app.Hub.Monitor.Client = probe
	e.app.Hub.Monitor.Probe(context.Background(), e.cSrv.URL)
	e.setup()
	e.app.Hub.Cfg.CentralURL = ""
	redirect.Store(true)
	for i := 0; i < 3; i++ {
		e.cycleOffline()
	}
	if got := e.app.Hub.Central.BaseURL(); got != newSrv.URL || !e.app.Hub.Online() {
		t.Fatalf("after permanent redirects: %s, online %v", got, e.app.Hub.Online())
	}
}

// A plain-http address that redirects (a captive portal, say) is not a move.
func TestCentralMoveIgnoresHTTPRedirect(t *testing.T) {
	e, newURL, _, redirect := moveEnv(t)
	redirect.Store(true)
	for i := 0; i < 3; i++ {
		e.cycleOffline()
	}
	if got := e.app.Hub.Central.BaseURL(); got == newURL {
		t.Fatal("a plain-http redirect moved the hub")
	}
}

// Finding 1 (critical review 2026-10-27): any user may write central-move until a
// country protects it, and the hub's ID is public. A server that echoes the ID back
// must get neither the hub's token nor the hub. The staff member who created the entry
// shares it read-only, as any owner may on 2.42: without a code for the hub it is
// still ignored (review R-07).
func TestCentralMoveSendsNothingToAForgedAddress(t *testing.T) {
	e, _, down, _ := moveEnv(t)
	e.giveMoveKey()
	var tokens atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			tokens.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"hubId": path.Base(r.URL.Path)})
	}))
	t.Cleanup(evil.Close)
	req, _ := http.NewRequest("POST", e.cSrv.URL+"/api/dataStore/facility-hub-admin/central-move", strings.NewReader(`{"url":"`+evil.URL+`"}`))
	req.SetBasicAuth("nurse_a", fc.Password) // a staff member, not an administrator
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("writing the announcement: %v %v", resp, err)
	}
	protect(e, "facility-hub-admin", "central-move")
	e.app.Hub.RefreshCatalog(context.Background())
	if !slices.Contains(e.app.Hub.Unsigned(), "facility-hub-admin/central-move") {
		t.Errorf("the forged announcement is not listed as ignored: %v", e.app.Hub.Unsigned())
	}
	down.Store(true)
	e.cycleOffline()
	if n := tokens.Load(); n > 0 {
		t.Errorf("the hub sent its credentials to the announced server %d times", n)
	}
	if got := e.app.Hub.Central.BaseURL(); got != e.cSrv.URL {
		t.Errorf("moved to a server that echoes its ID: %s", got)
	}
}
