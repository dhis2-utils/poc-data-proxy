package e2e

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/dhis2/facility-hub/internal/core"
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

// The country's choice on central names the version; the hub fetches that release from
// the release source, checks it, and goes on to upgrade. (The test binary is not a hub,
// so the upgrade stops at the migration, which shows the download and checks passed.)
func TestChoiceUpgrades(t *testing.T) {
	e, rs := upgradeEnv(t)
	rs.publish("0.3.0", "0.3.0")
	e.choose(`{"version":"0.3.0","pilotHubs":[],"percent":100}`)
	e.sync()
	got := strings.Join(rs.requested(), " ")
	if !strings.Contains(got, "v0.3.0/release.json") || !strings.Contains(got, "v0.3.0/hub-") {
		t.Fatalf("requests: %s", got)
	}
	if r := lastUpgrade(e); r["to"] != "0.3.0" || !strings.Contains(r["reason"].(string), "migration") {
		t.Fatalf("last upgrade %v", r)
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
