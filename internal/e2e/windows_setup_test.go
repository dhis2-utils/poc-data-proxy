package e2e

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// pcEnv is a hub as the Windows installer sets it up: set-up only on the PC itself.
func pcEnv(t *testing.T) *env {
	e := &env{t: t, central: fc.New(), dir: t.TempDir()}
	e.cSrv = httptest.NewServer(e.central)
	t.Cleanup(e.cSrv.Close)
	e.cfgHook = func(c *config.Config) { c.SetupLocalOnly, c.Listen = true, ":8443" }
	e.start()
	return e
}

func get(e *env, path, remote string) (int, string) {
	r := httptest.NewRequest("GET", "http://localhost"+path, nil)
	r.RemoteAddr = remote
	w := httptest.NewRecorder()
	e.app.Handler.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// Review focus 4: on a PC, set-up opens only on the PC itself.
func TestSetupOnlyOnThePC(t *testing.T) {
	e := pcEnv(t)
	if code, body := get(e, "/hub/setup", "192.168.1.50:40000"); code != http.StatusForbidden || !strings.Contains(body, "Set up this hub on the PC it runs on") {
		t.Fatalf("from the LAN: %d %.200s", code, body)
	}
	if code, body := get(e, "/hub/setup", "127.0.0.1:40000"); code != 200 || !strings.Contains(body, "<form") {
		t.Fatalf("from the PC: %d %.200s", code, body)
	}
}

// On the PC, the hub's page shows the address other devices use, with the port.
func TestLoginShowsAddressOnThePC(t *testing.T) {
	e := pcEnv(t)
	e.setup()
	_, body := get(e, "/hub/login", "127.0.0.1:40000")
	if !strings.Contains(body, "https://dhis2.local:8443") {
		t.Fatalf("login page on the PC: %.600s", body)
	}
	if _, body := get(e, "/hub/login", "192.168.1.50:40000"); strings.Contains(body, "Devices on this network") {
		t.Fatal("the address box shown to another device")
	}
}
