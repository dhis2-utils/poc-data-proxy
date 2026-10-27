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

	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/hubtls"
	"github.com/dhis2/facility-hub/internal/stick"
	"github.com/dhis2/facility-hub/internal/store"
)

// stickEnv is an e2e env whose hub has a USB stick (a folder here, with no mount
// check) and, with codeMode, no fixed central address: set-up then needs the setup
// code.
func stickEnv(t *testing.T, codeMode bool) (*env, string) {
	t.Helper()
	usb := t.TempDir()
	e := &env{t: t, central: fc.New(), dir: t.TempDir()}
	e.central.Seed("SeedTe00001", "SeedEn00001", "Seeded", "NAT-0001", map[string]string{"SeedEv00001": fc.Stage1})
	e.cSrv = httptest.NewServer(e.central)
	t.Cleanup(e.cSrv.Close)
	e.cfgHook = func(c *config.Config) {
		c.USBDir, c.USBRequireMount = usb, false
		if codeMode {
			c.CentralURL = ""
		}
	}
	e.start()
	e.app.Stick.Tick(context.Background()) // at boot: the box's stick gets the hub's mark
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
	c := strings.TrimSpace(string(code))
	if !strings.Contains(string(b), c) || !strings.Contains(string(b), e.app.Hub.HubID()) {
		t.Fatalf("setup-code.txt: %s", b)
	}
	// 6 digits: it only proves that whoever sets the hub up holds the box, and wrong
	// codes are throttled (50, then one per 15 minutes, from every device together).
	if len(c) != 6 || strings.Trim(c, "0123456789") != "" {
		t.Fatalf("setup code %q", c)
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
	e.app.Stick.Tick(context.Background()) // the first copy: key file and state
	if st := e.app.Stick.Status(); st == nil || !st.Present || !st.Writable || st.LastCopy == "" {
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

// RECOVERY.md C2: the stick keeps the hub's key file, its state (users, settings) and
// every pending save; the cached data is not copied, since central has it. A disk that
// dies is replaced by an empty one, which starts as a new hub; on its setup page a
// staff member of the old hub restores it from the stick with their own password
// (checked offline against the copy's key slots), and it restarts as the same hub. At
// the first staff login it puts back the saves and pushes them, and pulls the rest
// again.
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
	a.login() // unlocks, and puts back the saves
	e.sync()
	if e.central.Object("StickTe0001") == nil {
		t.Fatal("the save on the stick did not reach central")
	}
	if h.Facility() == "" || h.Index.Get("SeedTe00001") == nil {
		t.Fatalf("the restored hub did not take up its work: facility %q", h.Facility())
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

// A save pushed before the disk died is not on the stick, so a restore never sends it
// again (it would overwrite a change made on central since).
func TestStickRestoreDoesNotResend(t *testing.T) {
	e, _, id := stickWithSaves(t)
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
	if !strings.Contains(string(hb), `"present":true`) || !strings.Contains(string(hb), `"lastCopy"`) {
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
	os.RemoveAll(filepath.Join(usb, "facility-hub")) // from the boot tick, without the check
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
	e.app.Stick.Tick(context.Background()) // the first copy, before the edits
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

// Review minor: a restored save whose user is not on the copy (added in the last
// minute before the disk died) cannot be pushed as them; the hub says so instead of
// keeping it silently.
func TestStickRestoreReportsUnknownUser(t *testing.T) {
	e, usb, id := stickWithSaves(t)
	h := e.app.Hub
	rec := store.RawOutbox{Seq: 999, UserID: 999, PayloadEnc: []byte("x"), CreatedAt: store.Now(), State: store.OutboxPending, Kind: store.OutboxTracker}
	b, _ := json.Marshal(rec)
	sealed, err := h.Store.Seal("stick-outbox:999", b)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(usb, "facility-hub", id, "outbox", "999.rec"), sealed, 0o600)
	e.diskDies()
	e.restoreFromStick(id, "nurse_a", fc.Password)
	a := e.web("nurse_a")
	a.login()
	if st, page := a.do("GET", "/hub/ui", nil); st != 200 || !strings.Contains(string(page), "not on this hub") {
		t.Fatalf("admin page does not report the save of an unknown user: %d", st)
	}
}

// Review minor: a set-up that fails after registering removes its registry entry and
// its token again.
func TestFailedSetupCleansUp(t *testing.T) {
	e := newEnv(t)
	e.central.FailPaths = map[string]int{"/api/programs": 500}
	resp, err := e.web("hub_admin").postForm("/hub/setup", url.Values{"step": {"login"}, "central_url": {e.cSrv.URL},
		"username": {"hub_admin"}, "password": {fc.Password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if e.app.Hub.Mode() != "setup" {
		t.Fatal("set up although the programs could not be read")
	}
	if v := e.central.DataStoreValue("facility-hub-registry", e.app.Hub.HubID()); v != nil {
		t.Errorf("registry entry left behind: %v", v)
	}
	if n := e.central.TokensOf("hub_admin"); n != 0 {
		t.Errorf("%d tokens left behind", n)
	}
	e.central.FailPaths = nil
	e.setup()
}

// Review I3: once the hub has gone active, a later failure (here the set-up user's
// login on the hub) must not remove its registry entry or its token; and a hub it
// replaces that cannot be marked retired does not undo the set-up.
func TestSetupFailingAfterActivationKeepsTheHub(t *testing.T) {
	e := newEnv(t)
	old, _ := json.Marshal(map[string]any{"hubId": "hub-old", "facility": fc.Facility, "status": "active"})
	e.central.DataStore["facility-hub-registry"] = map[string]json.RawMessage{"hub-old": old}
	e.central.OnTokenCreate = func(string) {
		// After the hub's token: retiring the old hub and the set-up user's login fail.
		e.central.FailPaths = map[string]int{"/api/me": 500, "/api/dataStore/facility-hub-registry/hub-old": 500}
	}
	c := e.web("hub_admin")
	resp, err := c.postForm("/hub/setup", url.Values{"step": {"login"}, "central_url": {e.cSrv.URL}, "username": {"hub_admin"}, "password": {fc.Password}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := regexp.MustCompile(`name="nonce" value="([^"]+)"`).FindSubmatch(page)
	if m == nil {
		t.Fatalf("no confirm step: %s", page)
	}
	resp, err = c.postForm("/hub/setup", url.Values{"step": {"confirm"}, "nonce": {string(m[1])}, "replace": {"yes"}})
	if err != nil {
		t.Fatal(err)
	}
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "This hub now serves") {
		t.Errorf("the set-up page does not say the hub is set up: %.300s", page)
	}
	e.central.FailPaths = nil
	if e.app.Hub.Mode() != "active" {
		t.Fatalf("mode %s", e.app.Hub.Mode())
	}
	if v := e.central.DataStoreValue("facility-hub-registry", e.app.Hub.HubID()); v == nil {
		t.Error("the active hub's registry entry was removed")
	}
	if !e.central.TokenValid(e.app.Hub.Runtime().HubToken) {
		t.Error("the active hub's token was deleted")
	}
}

// Review I6: when the hub's state on the stick does not open, the restored hub does
// not unlock without its settings: the login fails, and the hub stays locked with the
// restore still to do.
func TestStickStateNotOpeningKeepsTheHubLocked(t *testing.T) {
	e, usb, id := stickWithSaves(t)
	f := filepath.Join(usb, "facility-hub", id, "state.rec")
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0xff
	os.WriteFile(f, b, 0o600)
	e.diskDies()
	e.restoreFromStick(id, "nurse_a", fc.Password)
	// Online, the login itself passes through to central.
	e.web("nurse_a").do("POST", "/api/auth/login", map[string]string{"username": "nurse_a", "password": fc.Password})
	if e.app.Hub.Store.Unlocked() {
		t.Fatal("unlocked without the hub's state")
	}
	if e.app.Hub.Runtime() != nil {
		t.Error("a runtime without the restored state")
	}
}

// DEPLOYMENT.md, "Reaching the hub securely": a disk restored from the stick keeps the
// hub's authority and server certificate, so its devices notice nothing. The new disk
// made its own at boot; the restore replaces them.
func TestStickKeepsHubCertificate(t *testing.T) {
	e, _ := stickEnv(t, false)
	e.setup()
	if _, err := hubtls.Load(e.dir, nil); err != nil {
		t.Fatal(err)
	}
	fp := hubtls.Fingerprint(e.dir)
	e.web("nurse_a").login()
	e.app.Stick.Tick(context.Background())
	id := e.app.Hub.HubID()
	e.diskDies()
	if _, err := hubtls.Load(e.dir, nil); err != nil {
		t.Fatal(err)
	}
	if hubtls.Fingerprint(e.dir) == fp {
		t.Fatal("the new disk has the old authority before any restore")
	}
	e.restoreFromStick(id, "nurse_a", fc.Password)
	if got := hubtls.Fingerprint(e.dir); got != fp {
		t.Fatalf("authority after restore %s, want %s", got, fp)
	}
	if _, err := hubtls.Load(e.dir, nil); err != nil {
		t.Fatal(err)
	}
	if got := hubtls.Fingerprint(e.dir); got != fp {
		t.Fatal("the restored authority was replaced at the next start")
	}
}

// swapStick takes the stick at usb out and plugs in another (a blank one, or the stick
// at other); it returns where the stick taken out is kept.
func swapStick(t *testing.T, usb, other string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "stick")
	if err := os.Rename(usb, out); err != nil {
		t.Fatal(err)
	}
	if other == "" {
		os.MkdirAll(usb, 0o700)
	} else if err := os.Rename(other, usb); err != nil {
		t.Fatal(err)
	}
	return out
}

// Review critical: the hub copies only to the box's own stick (the one in the box
// before set-up, or one a staff member took on since): a stick someone plugs in gets
// nothing of the hub's, and the hub says an unknown stick is in.
func TestStickForeignGetsNothing(t *testing.T) {
	e, usb, id := stickWithSaves(t)
	box := swapStick(t, usb, "")
	e.app.Stick.Tick(context.Background())
	if entries, _ := os.ReadDir(usb); len(entries) != 0 {
		t.Fatalf("a foreign stick got %v", entries)
	}
	if st := e.app.Stick.Status(); !st.Present || !st.Unknown || st.Writable {
		t.Fatalf("status with a foreign stick: %+v", st)
	}
	a := e.web("nurse_a")
	a.login()
	if _, page := a.do("GET", "/hub/ui", nil); !strings.Contains(string(page), "copy on this stick") {
		t.Fatal("the admin page does not say the stick is not the box's")
	}
	// A copy of the box's stick's folders, without its mark, is no better.
	os.MkdirAll(filepath.Join(usb, "facility-hub", id), 0o700)
	os.WriteFile(filepath.Join(usb, "facility-hub", id, "stick-id"), []byte("guess"), 0o600)
	e.app.Stick.Tick(context.Background())
	if files, _ := filepath.Glob(filepath.Join(usb, "facility-hub", id, "*")); len(files) != 1 {
		t.Fatalf("a stick with a wrong mark got %v", files)
	}
	swapStick(t, usb, box) // the box's stick back: copying goes on
	a.json("POST", postSync, newPatient("StickTe0003", "StickEn0003", "Stick", "NAT-5353", nil), 200)
	e.app.Stick.Tick(context.Background())
	if files, _ := filepath.Glob(filepath.Join(usb, "facility-hub", id, "outbox", "*")); len(files) != 2 {
		t.Fatalf("the box's stick after it came back: %v (status %+v)", files, e.app.Stick.Status())
	}
}

// A lost stick is replaced on the admin page: the new one is taken on and gets the
// whole copy, a new disk restores from it, and the old one, if found, gets nothing more.
func TestStickReplaced(t *testing.T) {
	e, usb, id := stickWithSaves(t)
	old := swapStick(t, usb, "")
	a := e.web("hub_admin")
	a.login()
	resp, err := a.postForm("/hub/ui/usb", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	e.app.Stick.Tick(context.Background())
	for _, f := range []string{"keys.db", "state.rec", "stick-id"} {
		if _, err := os.Stat(filepath.Join(usb, "facility-hub", id, f)); err != nil {
			t.Fatalf("the new stick: %v (status %+v)", err, e.app.Stick.Status())
		}
	}
	newStick := swapStick(t, usb, old)
	e.app.Stick.Tick(context.Background())
	if st := e.app.Stick.Status(); !st.Unknown {
		t.Fatalf("the old stick is still taken: %+v", st)
	}
	swapStick(t, usb, newStick)
	e.diskDies()
	e.restoreFromStick(id, "nurse_a", fc.Password)
	e.web("nurse_a").login()
	e.sync()
	if e.central.Object("StickTe0001") == nil {
		t.Fatal("the save on the new stick did not reach central")
	}
	e.app.Stick.Tick(context.Background())
	if st := e.app.Stick.Status(); st.Unknown || st.LastCopy == "" {
		t.Fatalf("the restored hub does not copy to the stick it came from: %+v", st)
	}
}

// A hub upgraded from a version without marks takes on the stick that holds its copy,
// and only that one.
func TestStickMarkedAfterUpgrade(t *testing.T) {
	e, usb, id := stickWithSaves(t)
	e.app.Hub.Store.SetMeta(stick.MetaStick, "") // as before marks
	os.Remove(filepath.Join(usb, "facility-hub", id, "stick-id"))
	box := swapStick(t, usb, "")
	e.app.Stick.Tick(context.Background())
	if entries, _ := os.ReadDir(usb); len(entries) != 0 || !e.app.Stick.Status().Unknown {
		t.Fatalf("a blank stick was taken on: %v", entries)
	}
	swapStick(t, usb, box)
	e.app.Stick.Tick(context.Background())
	if st := e.app.Stick.Status(); st.Unknown || e.app.Hub.Store.Meta(stick.MetaStick) == "" {
		t.Fatalf("the hub's own stick was not taken on: %+v", st)
	}
}
