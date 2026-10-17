package e2e

import (
	"context"
	"encoding/json"
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

// stickEnv is an e2e env whose hub has a USB stick (a folder here, with no mount
// check) and, with codeMode, no fixed central address: set-up then needs the setup
// code.
func stickEnv(t *testing.T, codeMode bool) (*env, string) {
	t.Helper()
	usb := t.TempDir()
	e := &env{t: t, central: fc.New(), dir: t.TempDir()}
	e.cSrv = httptest.NewServer(e.central)
	t.Cleanup(e.cSrv.Close)
	e.cfgHook = func(c *config.Config) {
		c.USBDir, c.USBRequireMount = usb, false
		if codeMode {
			c.CentralURL = ""
		}
	}
	e.start()
	return e, usb
}

// Before set-up, the hub writes its setup code and ID to the stick in the box; once
// set up, it removes the file (a used code no longer works).
func TestSetupCodeOnStick(t *testing.T) {
	e, usb := stickEnv(t, true)
	e.app.Stick.Tick(context.Background())
	b, err := os.ReadFile(filepath.Join(usb, "facility-hub", "setup-code.txt"))
	if err != nil {
		t.Fatal(err)
	}
	code, _ := os.ReadFile(filepath.Join(e.dir, "setup-token"))
	if !strings.Contains(string(b), strings.TrimSpace(string(code))) || !strings.Contains(string(b), e.app.Hub.HubID()) {
		t.Fatalf("setup-code.txt: %s", b)
	}
}

// RECOVERY.md C2: the stick keeps a daily snapshot and every offline save since. A
// disk that dies is replaced by an empty one: the hub restores from the stick as the
// same hub, locked; at the first staff login it puts back the saves made after the
// snapshot and pushes them.
func TestStickMirrorAndRestore(t *testing.T) {
	e, usb := stickEnv(t, false)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	id := e.app.Hub.HubID()
	e.app.Stick.Tick(context.Background()) // the first snapshot
	st := e.app.Stick.Status()
	if st == nil || !st.Present || !st.Writable || st.LastSnapshot == "" {
		t.Fatalf("stick status after the first tick: %+v", st)
	}
	e.offline()
	a.json("POST", postSync, newPatient("StickTe0001", "StickEn0001", "Stick", "NAT-5151", nil), 200)
	e.app.Stick.Tick(context.Background()) // copies the save
	if st := e.app.Stick.Status(); st.LastCopy == "" {
		t.Fatalf("no copy after a save: %+v", st)
	}

	// The disk dies: a new, empty one.
	e.stop()
	entries, _ := os.ReadDir(e.dir)
	for _, de := range entries {
		os.RemoveAll(filepath.Join(e.dir, de.Name()))
	}
	e.central.SetDown(false)
	e.start()
	h := e.app.Hub
	if h.HubID() != id || h.Mode() != "active" || h.Store.Unlocked() {
		t.Fatalf("after restore: id %s (want %s), mode %s, unlocked %v", h.HubID(), id, h.Mode(), h.Store.Unlocked())
	}
	a = e.web("nurse_a")
	a.login() // unlocks, and puts back the saves made after the snapshot
	e.sync()
	if e.central.Object("StickTe0001") == nil {
		t.Fatal("the save made after the snapshot did not reach central")
	}
	_ = usb
}

// A save leaves the stick once central has it: a restore must never send it again
// (it could overwrite newer edits on central). The heartbeat reports the stick.
func TestStickDropsPushedSaves(t *testing.T) {
	e, usb := stickEnv(t, false)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient("StickTe0002", "StickEn0002", "Stick", "NAT-5252", nil), 200)
	e.app.Stick.Tick(context.Background())
	dir := filepath.Join(usb, "facility-hub", e.app.Hub.HubID(), "outbox")
	if files, _ := os.ReadDir(dir); len(files) != 1 {
		t.Fatalf("copied saves: %d, want 1", len(files))
	}
	e.online()
	e.sync()
	e.app.Stick.Tick(context.Background())
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatalf("a pushed save stays on the stick: %d files", len(files))
	}
	hb, _ := json.Marshal(e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["usb"])
	if !strings.Contains(string(hb), `"present":true`) || !strings.Contains(string(hb), `"lastSnapshot"`) {
		t.Fatalf("heartbeat usb: %s", hb)
	}
	if st, page := a.do("GET", "/hub/ui", nil); st != 200 || !strings.Contains(string(page), "USB stick") || !strings.Contains(string(page), "Working") {
		t.Fatalf("admin page: %d", st)
	}
}

// A folder that is not a mount point is not a stick: nothing is written to the hub's
// own disk, and the hub reports the stick missing.
func TestStickMustBeMounted(t *testing.T) {
	e, usb := stickEnv(t, true)
	e.app.Hub.Cfg.USBRequireMount = true
	e.app.Stick.Tick(context.Background())
	if _, err := os.Stat(filepath.Join(usb, "facility-hub")); err == nil {
		t.Fatal("wrote to a folder that is not a mount point")
	}
	if st := e.app.Stick.Status(); st.Present {
		t.Fatalf("status: %+v", st)
	}
}

// Set-up with a generic image (DEPLOYMENT.md section 3): central's address and the
// setup code first; the hub accepts only https with a certificate the system trusts,
// and shows the server's own name and host before anyone logs in.
func TestSetupShowsServerBeforeLogin(t *testing.T) {
	e := &env{t: t, central: fc.New(), dir: t.TempDir()}
	e.cSrv = httptest.NewTLSServer(e.central)
	t.Cleanup(e.cSrv.Close)
	e.cfgHook = func(c *config.Config) { c.CentralURL = "" }
	e.start()
	trust := func() {
		tlsCfg := e.cSrv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
		e.app.Hub.Central.HTTP.Transport.(*http.Transport).TLSClientConfig = tlsCfg
		e.app.Hub.Monitor.Client = e.cSrv.Client()
	}
	code, _ := os.ReadFile(filepath.Join(e.dir, "setup-token"))
	c := e.web("hub_admin")
	post := func(form url.Values) string {
		t.Helper()
		resp, err := c.postForm("/hub/setup", form)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	server := func(u, code string) string {
		return post(url.Values{"step": {"server"}, "central_url": {u}, "setup_token": {code}})
	}
	if b := server(e.cSrv.URL, "WRONG-CODE"); !strings.Contains(b, "wrong setup") {
		t.Fatalf("wrong code accepted: %.300s", b)
	}
	if b := server(strings.Replace(e.cSrv.URL, "https:", "http:", 1), string(code)); !strings.Contains(b, "https") || strings.Contains(b, "Fake central") {
		t.Fatalf("plain http accepted: %.300s", b)
	}
	if b := server(e.cSrv.URL, string(code)); strings.Contains(b, "Fake central") {
		t.Fatalf("an untrusted certificate accepted: %.300s", b)
	}
	trust()
	b := server(e.cSrv.URL, string(code))
	host := strings.TrimPrefix(e.cSrv.URL, "https://")
	if !strings.Contains(b, "Fake central") || !strings.Contains(b, host) || !strings.Contains(b, `name="password"`) {
		t.Fatalf("the server is not shown before the login: %.600s", b)
	}
	m := regexp.MustCompile(`name="server" value="([^"]+)"`).FindStringSubmatch(b)
	if m == nil {
		t.Fatal("no server step to log in with")
	}
	b = post(url.Values{"step": {"login"}, "server": {m[1]}, "central_url": {"https://elsewhere.example"},
		"username": {"hub_admin"}, "password": {fc.Password}})
	if !strings.Contains(b, "This hub now serves") || e.app.Hub.Central.BaseURL() != e.cSrv.URL {
		t.Fatalf("set-up with the confirmed server: %s; %.300s", e.app.Hub.Central.BaseURL(), b)
	}
}
