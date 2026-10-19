package e2e

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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

// Review critical: a hub on a PC (no central_url, as hub init writes) sets up without
// the USB stick's setup code, from the PC itself.
func TestPCSetupNeedsNoCode(t *testing.T) {
	e := &env{t: t, central: fc.New(), dir: t.TempDir()}
	e.cSrv = httptest.NewTLSServer(e.central) // set-up accepts only https
	t.Cleanup(e.cSrv.Close)
	e.cfgHook = func(c *config.Config) { c.SetupLocalOnly, c.CentralURL = true, "" }
	e.start()
	tlsCfg := e.cSrv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	e.app.Hub.Central.HTTP.Transport.(*http.Transport).TLSClientConfig = tlsCfg
	e.app.Hub.Monitor.Client = e.cSrv.Client()
	if _, err := os.Stat(filepath.Join(e.dir, "setup-token")); err == nil {
		t.Error("a setup code was made for a hub set up on its own PC")
	}
	if _, page := get(e, "/hub/setup", "127.0.0.1:40000"); strings.Contains(page, "setup_token") {
		t.Fatal("the set-up page on the PC asks for a setup code")
	}
	// Central's address, checked, with no code; then a staff login.
	c := e.web("hub_admin")
	resp, err := c.postForm("/hub/setup", url.Values{"step": {"server"}, "central_url": {e.cSrv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := regexp.MustCompile(`name="server" value="([^"]+)"`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatalf("the address step: %.400s", b)
	}
	resp, err = c.postForm("/hub/setup", url.Values{"step": {"login"}, "server": {m[1]}, "central_url": {e.cSrv.URL},
		"username": {"hub_admin"}, "password": {fc.Password}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "This hub now serves") || e.app.Hub.Mode() != "active" {
		t.Fatalf("set-up: mode %s, %.400s", e.app.Hub.Mode(), b)
	}
}
