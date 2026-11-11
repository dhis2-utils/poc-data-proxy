package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/upgrade"
)

// releaseSource is a GitHub-like release download server: <base>/v<version>/<file>.
type releaseSource struct {
	mu    sync.Mutex
	files map[string][]byte // "v0.3.0/release.json" → content
	hits  []string
	priv  ed25519.PrivateKey
	srv   *httptest.Server
}

func newReleaseSource(t *testing.T) (*releaseSource, string) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	rs := &releaseSource{files: map[string][]byte{}, priv: priv}
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, "/")
		rs.hits = append(rs.hits, p)
		b, ok := rs.files[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(rs.srv.Close)
	return rs, base64.StdEncoding.EncodeToString(pub)
}

// publish puts a signed release for version on the server; manifestVersion is what its
// release.json claims (normally the same).
func (rs *releaseSource) publish(version, manifestVersion string) {
	bin := []byte("not really a hub " + version)
	sum := sha256.Sum256(bin)
	plat := runtime.GOOS + "/" + runtime.GOARCH
	name := "hub-" + runtime.GOOS + "-" + runtime.GOARCH
	m, _ := json.Marshal(upgrade.Manifest{Version: manifestVersion, Files: map[string]string{plat: name},
		SHA256: map[string]string{plat: hex.EncodeToString(sum[:])}})
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.files["v"+version+"/release.json"] = m
	rs.files["v"+version+"/release.json.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(rs.priv, m)))
	rs.files["v"+version+"/"+name] = bin
}

func (rs *releaseSource) requested() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.hits...)
}

func (e *env) choose(choice string) {
	e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{"release": json.RawMessage(choice)}
}

func upgradeEnv(t *testing.T) (*env, *releaseSource) {
	e := newEnv(t)
	e.setup()
	e.web("nurse_a").login()
	rs, key := newReleaseSource(t)
	e.app.Upgrader.Base, e.app.Upgrader.Key = rs.srv.URL, key
	return e, rs
}

func lastUpgrade(e *env) map[string]any {
	var r map[string]any
	json.Unmarshal([]byte(e.app.Hub.Store.Meta(core.MetaUpgrade)), &r)
	return r
}

// downloaded waits for the release download a cycle started in the background.
func (e *env) downloaded() { e.app.Upgrader.WaitDownload() }

// The country's choice on central names the version; the hub fetches that release from
// the release source in the background, checks it, and upgrades at the next cycle.
// (The test binary is not a hub, so the upgrade stops at the migration, which shows the
// download and checks passed.)
func TestChoiceUpgrades(t *testing.T) {
	e, rs := upgradeEnv(t)
	rs.publish("0.3.0", "0.3.0")
	e.choose(`{"version":"0.3.0","pilotHubs":[],"percent":100}`)
	e.sync()
	e.downloaded()
	if r := lastUpgrade(e); r != nil {
		t.Fatalf("upgraded in the cycle that started the download: %v", r)
	}
	e.sync()
	got := strings.Join(rs.requested(), " ")
	if !strings.Contains(got, "v0.3.0/release.json") || !strings.Contains(got, "v0.3.0/hub-") {
		t.Fatalf("requests: %s", got)
	}
	if r := lastUpgrade(e); r["to"] != "0.3.0" || !strings.Contains(r["reason"].(string), "migration") {
		t.Fatalf("last upgrade %v", r)
	}
}

// An approved hub takes the choice only signed with its move key (review R-07); the
// tests around this one use a hub without a move key, which takes it as read.
func TestChoiceSignedForApprovedHub(t *testing.T) {
	e, rs := upgradeEnv(t)
	e.giveMoveKey()
	rs.publish("0.3.0", "0.3.0")
	e.choose(`{"version":"0.3.0","pilotHubs":[],"percent":100}`)
	e.sync()
	if got := rs.requested(); len(got) != 0 || !slices.Contains(e.app.Hub.Unsigned(), "facility-hub-admin/release") {
		t.Fatalf("an unsigned choice: fetched %v, listed %v", got, e.app.Hub.Unsigned())
	}
	e.putSigned("facility-hub-admin/release", `{"version":"0.3.0","pilotHubs":[],"percent":100}`)
	e.sync()
	e.downloaded()
	if len(rs.requested()) == 0 || len(e.app.Hub.Unsigned()) != 0 {
		t.Fatalf("a signed choice: fetched %v, listed %v", rs.requested(), e.app.Hub.Unsigned())
	}
}

func TestNotInStage(t *testing.T) {
	e, rs := upgradeEnv(t)
	rs.publish("0.3.0", "0.3.0")
	e.choose(`{"version":"0.3.0","pilotHubs":["hub-someone-else"],"percent":0}`)
	e.sync()
	if got := rs.requested(); len(got) != 0 {
		t.Fatalf("a hub outside the stage fetched %v", got)
	}
	e.choose(`{"version":"0.3.0","pilotHubs":["` + e.app.Hub.HubID() + `"],"percent":0}`)
	e.sync()
	e.downloaded()
	if len(rs.requested()) == 0 {
		t.Fatal("a pilot hub did not fetch the release")
	}
}

func TestNoDowngradeNoRetry(t *testing.T) {
	e, rs := upgradeEnv(t)
	rs.publish("0.0.9", "0.0.9")
	e.choose(`{"version":"0.0.9","percent":100}`) // older than the hub's 0.1.0
	e.sync()
	rs.publish("0.3.0", "0.3.0")
	e.app.Hub.Store.SetMeta("upgrade_skip", "0.3.0") // failed here before
	e.choose(`{"version":"0.3.0","percent":100}`)
	e.sync()
	if got := rs.requested(); len(got) != 0 {
		t.Fatalf("fetched %v", got)
	}
}

func TestManifestVersionMustMatchChoice(t *testing.T) {
	e, rs := upgradeEnv(t)
	rs.publish("0.3.0", "0.3.1")
	e.choose(`{"version":"0.3.0","percent":100}`)
	e.sync()
	if r := lastUpgrade(e); r["result"] != "ignored" {
		t.Fatalf("last upgrade %v", r)
	}
	for _, p := range rs.requested() {
		if strings.Contains(p, "/hub-") {
			t.Fatal("downloaded the binary of a mislabelled release")
		}
	}
}

func TestBadSignatureIgnored(t *testing.T) {
	e, rs := upgradeEnv(t)
	rs.publish("0.3.0", "0.3.0")
	rs.files["v0.3.0/release.json.sig"] = []byte(base64.StdEncoding.EncodeToString(make([]byte, 64)))
	e.choose(`{"version":"0.3.0","percent":100}`)
	e.sync()
	if r := lastUpgrade(e); r["result"] != "ignored" {
		t.Fatalf("last upgrade %v", r)
	}
	for _, p := range rs.requested() {
		if strings.Contains(p, "/hub-") {
			t.Fatal("downloaded the binary of an unsigned release")
		}
	}
}

func TestNoChoiceNoUpgrade(t *testing.T) {
	e, rs := upgradeEnv(t)
	rs.publish("0.3.0", "0.3.0")
	e.sync()
	if got := rs.requested(); len(got) != 0 {
		t.Fatalf("fetched %v without a choice", got)
	}
}

// Review: a release whose binary does not match its checksum is not downloaded again
// every cycle (costly on a modem).
func TestChecksumMismatchNotRetried(t *testing.T) {
	e, rs := upgradeEnv(t)
	rs.publish("0.3.0", "0.3.0")
	for p := range rs.files {
		if strings.Contains(p, "/hub-") {
			rs.files[p] = []byte("tampered")
		}
	}
	e.choose(`{"version":"0.3.0","percent":100}`)
	e.sync()
	e.downloaded()
	e.sync()
	e.downloaded()
	if r := lastUpgrade(e); r["result"] != "ignored" {
		t.Fatalf("last upgrade %v", r)
	}
	n := 0
	for _, p := range rs.requested() {
		if strings.Contains(p, "/hub-") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("binary downloaded %d times", n)
	}
}

// Review: a slow link that keeps delivering finishes; one that stops is given up on
// after the idle time, not after a fixed total. Neither holds the sync cycle (security
// review 2026-10-20, S13): the download runs beside it.
func TestDownloadIdleTimeout(t *testing.T) {
	restore := upgrade.SetDownloadIdle(300 * time.Millisecond)
	t.Cleanup(restore)
	e, rs := upgradeEnv(t)
	rs.publish("0.3.0", "0.3.0")
	bin := rs.files["v0.3.0/hub-"+runtime.GOOS+"-"+runtime.GOARCH]
	stall := false
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/hub-") {
			rs.srv.Config.Handler.ServeHTTP(w, r)
			return
		}
		for i := range bin { // a byte every 20 ms: 1.4 s in all, never idle for long
			w.Write(bin[i : i+1])
			w.(http.Flusher).Flush()
			if stall && i == 5 {
				time.Sleep(2 * time.Second)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	t.Cleanup(slow.Close)
	e.app.Upgrader.Base = slow.URL
	e.choose(`{"version":"0.3.0","percent":100}`)
	start := time.Now()
	e.sync()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("the download held the cycle for %v", d)
	}
	e.downloaded()
	e.sync()
	if r := lastUpgrade(e); r["to"] != "0.3.0" || !strings.Contains(fmt.Sprint(r["reason"]), "migration") {
		t.Fatalf("slow but steady download did not finish: %v", r)
	}
	e.app.Hub.Store.SetMeta("upgrade_skip", "")
	e.app.Hub.Store.SetMeta(core.MetaUpgrade, "")
	os.RemoveAll(filepath.Join(e.dir, "bin"))
	stall = true
	e.sync()
	start = time.Now()
	e.downloaded()
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("a stalled download was kept for %v", d)
	}
	e.sync()
	if r := lastUpgrade(e); r != nil {
		t.Fatalf("upgraded from a stalled download: %v", r)
	}
}

// Review: a hub that starts locked after an upgrade (the key handover failed) has not
// opened its migrated data yet: the upgrade is confirmed, and the old store removed,
// only after the first unlock checks it.
func TestUpgradeConfirmedAfterUnlock(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.web("nurse_a").login()
	prev := filepath.Join(e.dir, "hub.db.prev")
	os.WriteFile(prev, []byte("the old store"), 0o600)
	mk, _ := json.Marshal(upgrade.Marker{State: "swapped", From: "0.0.9", To: "0.1.0", ExePath: filepath.Join(e.dir, "x"),
		PrevBinary: filepath.Join(e.dir, "y"), PrevStore: prev})
	os.WriteFile(filepath.Join(e.dir, "upgrade.json"), mk, 0o600)
	e.stop()
	e.start() // locked: no handed-over key
	if e.app.Hub.Store.Unlocked() {
		t.Fatal("unlocked without a login")
	}
	e.app.Upgrader.AfterBoot(context.Background(), e.srv.URL+"/hub/status")
	if _, err := os.Stat(prev); err != nil {
		t.Fatal("the old store was removed before the migrated one was opened")
	}
	if r := lastUpgrade(e); r["result"] == "ok" {
		t.Fatal("confirmed while locked")
	}
	e.web("nurse_a").login()
	for i := 0; i < 50 && lastUpgrade(e)["result"] != "ok"; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if r := lastUpgrade(e); r["result"] != "ok" {
		t.Fatalf("not confirmed after the unlock: %v", r)
	}
	if _, err := os.Stat(prev); err == nil {
		t.Error("the old store stays after the confirmation")
	}
}

// An upgrade cut short in its store swap (a power cut after the live store was moved
// aside) restarts on the old binary: the old store, with its unsent saves, is put back
// before it is opened, and the upgrade is reported as failed, not as done. The version
// is tried once more (a power cut says nothing against it); after a second cut it is
// skipped, as after a failed health check.
func TestUpgradeCutShortKeepsStore(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient("CutTe000001", "CutEn000001", "Cut", "NAT-5151", nil), 200)
	hubID := e.app.Hub.HubID()
	for attempt := 1; attempt <= 2; attempt++ {
		e.stop()
		live := filepath.Join(e.dir, "hub.db")
		for _, f := range []string{live, live + ".data"} { // the migrated copy
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(strings.Replace(f, "hub.db", "hub.db.next", 1), b, 0o600)
		}
		mk, _ := json.Marshal(upgrade.Marker{State: "swapped", From: "0.1.0", To: "0.2.0", ExePath: filepath.Join(e.dir, "x"),
			PrevBinary: filepath.Join(e.dir, "y"), PrevStore: live + ".prev"})
		os.WriteFile(filepath.Join(e.dir, "upgrade.json"), mk, 0o600)
		if err := os.Rename(live, live+".prev"); err != nil { // the swap's first step, then the cut
			t.Fatal(err)
		}
		e.start() // the old binary (0.1.0)
		e.app.Upgrader.AfterBoot(context.Background(), e.srv.URL+"/hub/status")
		if got := e.app.Hub.HubID(); got != hubID {
			t.Fatalf("attempt %d: hub ID %q after the restart, want %q: the old store is not in place", attempt, got, hubID)
		}
		if r := lastUpgrade(e); r["result"] != "failed" || r["to"] != "0.2.0" {
			t.Fatalf("attempt %d: a swap cut short is reported as %v", attempt, r)
		}
		skipped := e.app.Hub.Store.Meta("upgrade_skip") == "0.2.0"
		if attempt == 1 && skipped {
			t.Error("the version is not tried again after one cut")
		}
		if attempt == 2 && !skipped {
			t.Error("the version is tried again after a second cut")
		}
		a = e.web("nurse_a")
		a.login()
		if p, err := e.app.Hub.Store.PendingOutbox(); err != nil || len(p) != 1 {
			t.Fatalf("attempt %d: pending saves after the restart: %d, %v", attempt, len(p), err)
		}
	}
}

// swappedTo leaves the marker of an upgrade to version that has swapped in this
// binary and not been confirmed yet, with an old store to roll back to.
func (e *env) swappedTo(version string) (prev string) {
	prev = filepath.Join(e.dir, "hub.db.prev")
	os.WriteFile(prev, []byte("the old store"), 0o600)
	mk, _ := json.Marshal(upgrade.Marker{State: "swapped", From: "0.0.9", To: version, ExePath: filepath.Join(e.dir, "x"),
		PrevBinary: filepath.Join(e.dir, "y"), PrevStore: prev})
	os.WriteFile(filepath.Join(e.dir, "upgrade.json"), mk, 0o600)
	return prev
}

// N5 (docs/ADVERSARIAL-TESTING.md): a new release whose central is slow or down at its
// first start is kept. Its health check tests the hub itself; central is not part of it.
func TestUpgradeConfirmedWithCentralDown(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.web("nurse_a").login()
	e.swappedTo("0.1.0")
	e.offline()
	e.app.Upgrader.AfterBoot(context.Background(), e.srv.URL+"/hub/status")
	if r := lastUpgrade(e); r["result"] != "ok" {
		t.Fatalf("with central down the upgrade is %v", r)
	}
}

// N5: until its health check has passed, a new release takes no save of its own and
// pushes nothing: a rollback would put back the old store, and lose the one and push
// the other again. Saves get the hub's "upgrading" answer, and clients retry.
func TestUnconfirmedUpgradeTakesNoSaves(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient("UpgTe000001", "UpgEn000001", "Queued", "NAT-6161", nil), 200)
	e.swappedTo("0.1.0")
	e.stop()
	e.start() // the new release, before AfterBoot has checked it
	a = e.web("nurse_a")
	a.login()
	e.offline()
	if st, b := a.do("POST", postSync, newPatient("UpgTe000002", "UpgEn000002", "During", "NAT-6162", nil)); st != http.StatusServiceUnavailable ||
		!strings.Contains(string(b), "upgrading") {
		t.Fatalf("an offline save before the health check: %d %s", st, b)
	}
	e.online()
	e.sync()
	if e.central.Object("UpgTe000001") != nil {
		t.Fatal("the new release pushed before its health check")
	}
	if p, _ := e.app.Hub.Store.PendingOutbox(); len(p) != 1 {
		t.Fatalf("pending saves %d, want 1", len(p))
	}

	e.app.Upgrader.AfterBoot(context.Background(), e.srv.URL+"/hub/status")
	if r := lastUpgrade(e); r["result"] != "ok" {
		t.Fatalf("last upgrade %v", r)
	}
	e.offline()
	a.json("POST", postSync, newPatient("UpgTe000003", "UpgEn000003", "After", "NAT-6163", nil), 200)
	e.online()
	e.sync()
	for _, te := range []string{"UpgTe000001", "UpgTe000003"} {
		if e.central.Object(te) == nil {
			t.Errorf("%s not pushed after the health check", te)
		}
	}
}

// N13/H3: on a central version this release does not support, the hub passes tracker
// and aggregate requests through while online, also with saves queued; those stay
// queued. Once a release is chosen, the upgrade does not push them (the push needs the
// version's handling): the new release does.
func TestNeedsUpgradePassesThrough(t *testing.T) {
	e, rs := upgradeEnv(t)
	nurse := e.android("nurse_a")
	e.offline()
	nurse.json("POST", postSync, newPatient("NupTe000001", "NupEn000001", "Queued", "NAT-7171", nil), 200)
	e.central.SetVersion("2.44.0")
	e.online()
	e.app.Worker.Cycle(context.Background()) // "hub needs upgrade"
	if e.app.Hub.API() != nil {
		t.Fatal("2.44 is supported")
	}
	if st, b := nurse.do("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnit="+fc.Facility, nil); st != 200 {
		t.Fatalf("an online read: %d %s", st, b)
	}
	nurse.json("POST", postSync, newPatient("NupTe000002", "NupEn000002", "Online", "NAT-7172", nil), 200)
	if e.central.Object("NupTe000002") == nil {
		t.Error("an online save did not reach central")
	}
	agg := "GET /api/dataEntry/dataValues"
	before := e.central.RequestCount(agg)
	nurse.do("GET", "/api/dataEntry/dataValues?ds=x&pe=202601&ou="+fc.Facility, nil)
	if e.central.RequestCount(agg) == before {
		t.Error("an online aggregate read was not passed through")
	}

	rs.publish("0.3.0", "0.3.0")
	e.choose(`{"version":"0.3.0","pilotHubs":[],"percent":100}`)
	e.app.Worker.Cycle(context.Background())
	e.downloaded()
	e.app.Worker.Cycle(context.Background()) // the upgrade: this test binary is no hub, so it stops at the migration
	if r := lastUpgrade(e); r["to"] != "0.3.0" || !strings.Contains(fmt.Sprint(r["reason"]), "migration") {
		t.Fatalf("last upgrade %v", r)
	}
	if e.central.Object("NupTe000001") != nil {
		t.Error("pushed to a central this release does not support")
	}
	if p, _ := e.app.Hub.Store.PendingOutbox(); len(p) != 1 {
		t.Fatalf("pending saves %d, want 1", len(p))
	}
}
