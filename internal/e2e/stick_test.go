package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

// diskDies replaces the hub's disk with an empty one and boots it.
func (e *env) diskDies() {
	e.stop()
	entries, _ := os.ReadDir(e.dir)
	for _, de := range entries {
		os.RemoveAll(filepath.Join(e.dir, de.Name()))
	}
	e.central.SetDown(false)
	e.start()
}

// restoreFromStick restores hub id from the stick on the setup page, as user, and
// boots the restored hub; it returns the page the restore answered.
func (e *env) restoreFromStick(id, user, password string) string {
	e.t.Helper()
	resp, err := e.web(user).postForm("/hub/setup", url.Values{"step": {"restore"}, "hub": {id},
		"username": {user}, "password": {password}})
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "restarts as hub") {
		e.stop() // the hub restarts into the restored copy
		e.start()
	}
	return string(b)
}

// stickWithSaves sets up a hub with a stick, takes the first snapshot, then saves a new
// patient offline and copies it: what the stick holds when the disk dies.
func stickWithSaves(t *testing.T) (*env, string, string) {
	e, usb := stickEnv(t, false)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.app.Stick.Tick(context.Background()) // the first snapshot
	if st := e.app.Stick.Status(); st == nil || !st.Present || !st.Writable || st.LastSnapshot == "" {
		t.Fatalf("stick status after the first tick: %+v", st)
	}
	e.offline()
	a.json("POST", postSync, newPatient("StickTe0001", "StickEn0001", "Stick", "NAT-5151", nil), 200)
	e.app.Stick.Tick(context.Background()) // copies the save
	if st := e.app.Stick.Status(); st.LastCopy == "" {
		t.Fatalf("no copy after a save: %+v", st)
	}
	return e, usb, e.app.Hub.HubID()
}

// RECOVERY.md C2: the stick keeps a daily snapshot and every offline save since. A
// disk that dies is replaced by an empty one, which starts as a new hub; on its setup
// page a staff member of the old hub restores it from the stick with their own
// password (checked offline against the copy's key slots), and it restarts as the
// same hub. At the first staff login it puts back the saves made after the snapshot
// and pushes them.
func TestStickMirrorAndRestore(t *testing.T) {
	e, _, id := stickWithSaves(t)
	e.diskDies()
	if e.app.Hub.Mode() != "setup" || e.app.Hub.HubID() == id {
		t.Fatal("a new disk restored itself from the stick without anyone asking")
	}
	if _, page := e.web("nurse_a").do("GET", "/hub/setup", nil); !strings.Contains(string(page), id) {
		t.Fatalf("the setup page does not offer the stick's hub %s", id)
	}
	if b := e.restoreFromStick(id, "nurse_a", "wrong"); strings.Contains(b, "restarts as hub") {
		t.Fatal("restored with a wrong password")
	}
	e.restoreFromStick(id, "nurse_a", fc.Password)
	h := e.app.Hub
	if h.HubID() != id || h.Mode() != "active" || h.Store.Unlocked() {
		t.Fatalf("after restore: id %s (want %s), mode %s, unlocked %v", h.HubID(), id, h.Mode(), h.Store.Unlocked())
	}
	a := e.web("nurse_a")
	a.login() // unlocks, and puts back the saves made after the snapshot
	e.sync()
	if e.central.Object("StickTe0001") == nil {
		t.Fatal("the save made after the snapshot did not reach central")
	}
}

// Review critical: the saves to put back are on the disk once the restore is chosen,
// so a stick pulled out (or a copy tick) before the first login loses nothing.
func TestStickRestoreSurvivesPulledStick(t *testing.T) {
	e, usb, id := stickWithSaves(t)
	e.diskDies()
	e.restoreFromStick(id, "nurse_a", fc.Password)
	os.RemoveAll(usb) // the stick is pulled out before anyone logs in
	os.MkdirAll(usb, 0o700)
	e.web("nurse_a").login()
	e.sync()
	if e.central.Object("StickTe0001") == nil {
		t.Fatal("the save was lost with the stick")
	}
}

// Review important: a save pushed after the snapshot is not sent again by a restore
// (it would overwrite a change made on central since).
func TestStickRestoreDoesNotResend(t *testing.T) {
	e, _, id := stickWithSaves(t)
	e.app.Stick.Tick(context.Background())
	// The snapshot (taken before the save) does not hold it; take one that does, with it
	// pending, then push it and copy again: the stick now knows it was sent.
	e.app.Hub.Cfg.USBSnapshotInterval = 0
	e.app.Stick.Tick(context.Background())
	e.app.Hub.Cfg.USBSnapshotInterval = time.Hour
	e.online()
	e.sync()
	e.app.Stick.Tick(context.Background())
	if e.central.Object("StickTe0001") == nil {
		t.Fatal("not pushed")
	}
	e.central.Seed("StickTe0001", "StickEn0001", "ChangedOnCentral", "NAT-5151", nil)
	e.diskDies()
	e.restoreFromStick(id, "nurse_a", fc.Password)
	e.web("nurse_a").login()
	e.sync()
	if !strings.Contains(fmt.Sprint(e.central.Object("StickTe0001")), "ChangedOnCentral") {
		t.Fatal("the restore sent a save central already had, over a newer change")
	}
}

// Review important: power lost between the snapshot's two renames leaves the previous
// snapshot as snapshot.old; it is still offered.
func TestStickRestoreFromPreviousSnapshot(t *testing.T) {
	e, usb, id := stickWithSaves(t)
	dir := filepath.Join(usb, "facility-hub", id)
	if err := os.Rename(filepath.Join(dir, "snapshot"), filepath.Join(dir, "snapshot.old")); err != nil {
		t.Fatal(err)
	}
	e.diskDies()
	e.restoreFromStick(id, "nurse_a", fc.Password)
	if e.app.Hub.HubID() != id {
		t.Fatal("the previous snapshot was not used")
	}
}

// Review important: the stick's records are sealed whole: one changed on the stick is
// not put back (its user or flags cannot be swapped).
func TestStickTamperedRecordNotRestored(t *testing.T) {
	e, usb, id := stickWithSaves(t)
	files, _ := filepath.Glob(filepath.Join(usb, "facility-hub", id, "outbox", "*"))
	if len(files) != 1 {
		t.Fatalf("records on the stick: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	b[len(b)/2] ^= 0xff
	os.WriteFile(files[0], b, 0o600)
	e.diskDies()
	e.restoreFromStick(id, "nurse_a", fc.Password)
	e.web("nurse_a").login()
	e.sync()
	if e.central.Object("StickTe0001") != nil {
		t.Fatal("a record changed on the stick was put back and pushed")
	}
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

// Review critical: an aggregate value corrected offline rewrites its outbox entry in
// place; the stick follows, so a restore puts back the corrected value, not the first.
func TestStickKeepsCorrectedAggregateValue(t *testing.T) {
	e, _ := stickEnv(t, false)
	e.setup()
	e.web("nurse_a").login()
	h := e.app.Hub
	e.app.Stick.Tick(context.Background()) // the snapshot, before the edits
	u, _ := h.Store.UserByName("nurse_a")
	edit := func(value string) {
		t.Helper()
		if err := h.Store.Tx(context.Background(), func(tx *sql.Tx) error {
			_, err := h.Store.AppendAggregate(tx, u.ID, []byte(`{"value":"`+value+`"}`), "DsAbcdefghi|202610|OuAbcdefghi|aoc|DeAbcdefghi|coc|value", "DsAbcdefghi|202610|OuAbcdefghi|aoc", false)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		e.app.Stick.Tick(context.Background())
	}
	edit("5")
	edit("7") // corrected: the same entry, rewritten
	id := h.HubID()
	e.diskDies()
	e.restoreFromStick(id, "nurse_a", fc.Password)
	e.central.SetDown(true) // nothing is pushed while we look
	e.offline()
	e.web("nurse_a").login()
	entries, err := e.app.Hub.Store.PendingOutbox()
	if err != nil || len(entries) != 1 || !strings.Contains(string(entries[0].Payload), `"7"`) {
		var got []string
		for _, x := range entries {
			got = append(got, string(x.Payload))
		}
		t.Fatalf("restored outbox: %v %v", got, err)
	}
}
