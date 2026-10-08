// Regression tests for the findings in docs/REVIEW.md.
package e2e

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/core"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/ui"
)

// M2: a write accepted while an upgrade copies the store would be lost with the
// store the upgrade replaces. The hub refuses it instead.
func TestUpgradeFreezesLocalWrites(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	h := e.app.Hub

	h.FreezeLocalWrites()
	a.json("POST", postSync, newPatient("FrozTe00001", "FrozEn00001", "Frozen", "", nil), 503)
	if n := h.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("%d outbox entries written during the upgrade", n)
	}
	if st, body := a.do("GET", "/api/trackedEntityAttributes/"+fc.AttrTBID+"/generate", nil); st != 503 {
		t.Fatalf("reserved value handed out during the upgrade: %d %s", st, body)
	}

	h.ThawLocalWrites() // the upgrade failed and the hub carries on
	a.json("POST", postSync, newPatient("FrozTe00001", "FrozEn00001", "Frozen", "", nil), 200)
	if n := h.Store.CountOutbox(store.OutboxPending); n != 1 {
		t.Fatalf("outbox entries after thaw: %d", n)
	}
}

// M5: the hub-level token is renewed before it expires, and the old one revoked.
func TestHubTokenRenewal(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	old := h.Runtime().HubToken
	e.sync()
	if h.Runtime().HubToken != old {
		t.Fatal("a fresh hub token was renewed")
	}
	h.UpdateRuntime(func(rt *core.Runtime) { rt.HubTokenExp = time.Now().Add(48 * time.Hour).Format(time.RFC3339) })
	e.sync()
	rt := h.Runtime()
	if rt.HubToken == old || !e.central.TokenValid(rt.HubToken) {
		t.Fatal("hub token not renewed")
	}
	if e.central.TokenValid(old) {
		t.Fatal("old hub token not revoked")
	}
	if exp, _ := time.Parse(time.RFC3339, rt.HubTokenExp); time.Until(exp) < 80*24*time.Hour {
		t.Fatalf("new expiry %s", rt.HubTokenExp)
	}
}

// M1: the hub refuses Host names it does not answer to, and a response cached for one
// Host is never replayed to another client with that Host baked in.
func TestHostHeader(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.app.Hub.Cfg.HubHosts = []string{"alias.test"}
	a := e.web("nurse_a")
	a.login()
	b := e.web("nurse_b")
	b.login()

	if st, _ := a.doHost("evil.example", "GET", "/dhis-web-capture/index.html", nil); st != 421 {
		t.Fatalf("foreign Host: got %d, want 421", st)
	}
	st, body := a.doHost("alias.test", "GET", "/dhis-web-capture/index.html", nil)
	if st != 200 || !strings.Contains(string(body), `content="http://alias.test"`) {
		t.Fatalf("online, base URL not pointed at the hub: %d %s", st, body)
	}
	// The cache holds central's own form, never the requesting Host.
	if c, _ := e.app.Hub.Store.GetCacheAnyUser("GET", "/dhis-web-capture/index.html"); c == nil || bytes.Contains(c.Body, []byte("alias.test")) {
		t.Fatalf("cached copy carries the requesting Host: %v", c)
	}
	e.offline()
	st, body = b.do("GET", "/dhis-web-capture/index.html", nil) // served from nurse_a's cache entry
	if st != 200 || strings.Contains(string(body), "alias.test") || !strings.Contains(string(body), e.srv.URL) {
		t.Fatalf("offline, cached page carries another client's Host: %d %s", st, body)
	}
}

// M4: sessions end after inactivity.
func TestSessionIdleExpiry(t *testing.T) {
	e := newEnv(t)
	e.setup()
	search := "/api/tracker/trackedEntities?orgUnits=" + fc.Facility + "&program=" + fc.Program
	e.app.Hub.Sessions.Idle = 100 * time.Millisecond
	a := e.web("nurse_a")
	a.login()
	a.json("GET", search, nil, 200)
	time.Sleep(200 * time.Millisecond)
	if st, _ := a.do("GET", "/api/me", nil); st != 401 {
		t.Fatalf("idle session still accepted: %d", st)
	}
}

// M4: an account disabled on central stops working through the hub, online on the
// next credential check and offline from then on, until an online login succeeds.
func TestDisabledUserLosesAccess(t *testing.T) {
	old := core.BasicRecheck
	core.BasicRecheck = 0
	t.Cleanup(func() { core.BasicRecheck = old })
	e := newEnv(t)
	e.setup()
	search := "/api/tracker/trackedEntities?orgUnits=" + fc.Facility + "&program=" + fc.Program
	tablet := e.android("nurse_a")
	tablet.json("GET", search, nil, 200)
	web := e.web("nurse_a")
	web.login()

	e.central.SetDisabled("nurse_a", true)
	if st, _ := tablet.do("GET", search, nil); st != 401 {
		t.Fatalf("disabled user's Basic credential still accepted online: %d", st)
	}
	e.offline()
	if st, _ := tablet.do("GET", search, nil); st != 401 {
		t.Fatalf("disabled user's Basic credential accepted offline: %d", st)
	}
	if st, _ := web.do("GET", search, nil); st != 401 {
		t.Fatalf("disabled user's web session survived: %d", st)
	}
	if st, _ := e.web("nurse_a").do("POST", "/api/auth/login", map[string]string{"username": "nurse_a", "password": fc.Password}); st != 401 {
		t.Fatalf("disabled user logged in offline: %d", st)
	}

	e.central.SetDisabled("nurse_a", false)
	e.online()
	tablet.json("GET", search, nil, 200)
	e.offline()
	tablet.json("GET", search, nil, 200)
}

// M3: repeated failed logins lock out that user from that address, for the web login
// and Basic auth alike; other users are unaffected.
func TestLoginThrottle(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.web("nurse_a").login()
	e.web("nurse_b").login()
	e.offline()
	for i := 0; i < 5; i++ {
		if st, _ := e.web("nurse_b").do("POST", "/api/auth/login", map[string]string{"username": "nurse_b", "password": "guess"}); st != 401 {
			t.Fatalf("attempt %d: %d", i, st)
		}
	}
	if st, _ := e.web("nurse_b").do("POST", "/api/auth/login", map[string]string{"username": "nurse_b", "password": fc.Password}); st != 429 {
		t.Fatalf("locked-out web login: got %d, want 429", st)
	}
	if st, _ := e.android("nurse_b").do("GET", "/api/me", nil); st != 429 {
		t.Fatalf("locked-out Basic login: got %d, want 429", st)
	}
	e.web("nurse_a").login()
}

// M8, REVIEW-2 F1: nobody at the setup page can choose the central server. With
// central_url only, the form value is ignored; with central_hosts, the URL must be on
// them; with neither, setup needs the token from the label. Cross-site posts are
// refused, and a failed attempt leaves the hub's central URL as it was.
func TestSetupCentralURL(t *testing.T) {
	e := newEnv(t)
	h := e.app.Hub
	post := func(origin string, form url.Values) (int, string) {
		form.Set("step", "login")
		form.Set("username", "hub_admin")
		form.Set("password", fc.Password)
		req, _ := http.NewRequest("POST", e.srv.URL+"/hub/setup", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := e.web("hub_admin").http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	before := h.Central.BaseURL()

	if st, _ := post("https://evil.example", url.Values{"central_url": {"https://attacker.example"}}); st != 403 {
		t.Fatalf("cross-site setup post: %d", st)
	}

	h.Cfg.CentralURL, h.Cfg.CentralHosts = "", nil // token mode
	if err := ui.EnsureSetupToken(h); err != nil {
		t.Fatal(err)
	}
	if _, page := post(e.srv.URL, url.Values{"central_url": {"https://attacker.example"}}); !strings.Contains(page, "wrong setup token") {
		t.Fatalf("setup without token: %s", page)
	}
	tok, _ := os.ReadFile(filepath.Join(e.dir, "setup-token"))
	if _, page := post(e.srv.URL, url.Values{"central_url": {"http://attacker.example"}, "setup_token": {string(tok)}}); !strings.Contains(page, "https://") {
		t.Fatalf("plain http central URL accepted: %s", page)
	}

	h.Cfg.CentralURL, h.Cfg.CentralHosts = e.cSrv.URL, []string{"*.hmis.example"}
	if _, page := post(e.srv.URL, url.Values{"central_url": {"https://attacker.example"}}); !strings.Contains(page, "must be an https URL on") {
		t.Fatalf("URL outside central_hosts accepted: %s", page)
	}
	if got := h.Central.BaseURL(); got != before {
		t.Fatalf("failed setup changed the central URL to %s", got)
	}

	h.Cfg.CentralHosts = nil // central_url only: the form value is ignored
	if _, page := post(e.srv.URL, url.Values{"central_url": {"https://attacker.example"}}); !strings.Contains(page, "This hub now serves") {
		t.Fatalf("setup with pinned URL failed: %s", page)
	}
	if got := h.Central.BaseURL(); got != e.cSrv.URL {
		t.Fatalf("hub points at %s", got)
	}
}

// M7: offline, the pool hands out only generated attributes the user can read, and at
// most half the pool per request.
func TestReservedValueLimits(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.android("nurse_a")
	a.json("GET", "/api/me", nil, 200)
	e.offline()
	if st, _ := a.do("GET", "/api/trackedEntityAttributes/"+fc.AttrFirstName+"/generate", nil); st != 403 {
		t.Fatalf("non-generated attribute: got %d, want 403", st)
	}
	st, body := a.do("GET", "/api/trackedEntityAttributes/"+fc.AttrTBID+"/generateAndReserve?numberToReserve=1000", nil)
	var vals []any
	json.Unmarshal(body, &vals)
	if st != 200 || len(vals) != e.app.Hub.Cfg.ReservedPoolSize/2 {
		t.Fatalf("got %d values (%d): %s", len(vals), st, body)
	}
}

// M9: offline, a record created on the hub is shown only to users whose search scope
// includes the facility, not to everyone with program access.
func TestLocalObjectsNeedSearchScope(t *testing.T) {
	e := newEnv(t)
	e.central.Users["nurse_b"].SearchOrgUnits = []string{fc.OtherFacility}
	e.setup()
	a := e.web("nurse_a")
	a.login()
	b := e.web("nurse_b")
	b.login()
	e.offline()
	a.json("POST", postSync, newPatient("ScopeTe0001", "ScopeEn0001", "Scope", "", nil), 200)
	search := "/api/tracker/trackedEntities?orgUnits=" + fc.Facility + "&program=" + fc.Program
	if got := uids(a.json("GET", search, nil, 200)["trackedEntities"], "trackedEntity"); !contains(got, "ScopeTe0001") {
		t.Fatalf("author does not see it: %v", got)
	}
	if got := uids(b.json("GET", search, nil, 200)["trackedEntities"], "trackedEntity"); contains(got, "ScopeTe0001") {
		t.Fatal("user without search scope at the facility sees a local record")
	}
}

// L3: an offline update cannot rewrite who created a record.
func TestCreatedByKept(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	b := e.web("nurse_b")
	b.login()
	e.offline()
	a.json("POST", postSync, newPatient("OwnTe000001", "OwnEn000001", "Own", "", nil), 200)
	upd := newPatient("OwnTe000001", "OwnEn000001", "Own", "", nil)
	te := upd["trackedEntities"].([]any)[0].(map[string]any)
	te["createdBy"] = map[string]any{"username": "someone_else"}
	te["createdAt"] = "2000-01-01T00:00:00.000"
	b.json("POST", postSync, upd, 200)
	ix := e.app.Hub.Index
	d := ix.Full(ix.Get("OwnTe000001"))
	if cb, _ := d["createdBy"].(map[string]any); cb["username"] != "nurse_a" || d["createdAt"] == "2000-01-01T00:00:00.000" {
		t.Fatalf("createdBy %v createdAt %v", d["createdBy"], d["createdAt"])
	}
}

// REVIEW-2 F1, F2: a signed release manifest can move hubs to a new central URL (a
// domain migration); an unsigned one can do nothing, and none of its content is kept.
func TestCentralMoveBySignedManifest(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	moved := httptest.NewServer(e.central) // the same DHIS2 at a new address
	t.Cleanup(moved.Close)

	pub, priv, _ := ed25519.GenerateKey(nil)
	files := map[string][]byte{}
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(channel.Close)
	h.Cfg.ReleaseChannel = channel.URL
	h.Cfg.ReleasePublicKey = base64.StdEncoding.EncodeToString(pub)
	manifest := []byte(`{"version":"0.1.0","files":{},"sha256":{},"centralUrl":"` + moved.URL + `","stage":{"percent":100}}`)

	files["release.json"] = manifest // unsigned
	e.sync()
	if h.Central.BaseURL() != e.cSrv.URL || strings.Contains(h.Store.Meta("upgrade_unsigned"), "0.1.0") {
		t.Fatalf("unsigned manifest acted on: %s %q", h.Central.BaseURL(), h.Store.Meta("upgrade_unsigned"))
	}

	files["release.json.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest)))
	e.sync()
	if h.Central.BaseURL() != moved.URL || h.Store.Meta(core.MetaCentralURL) != moved.URL {
		t.Fatalf("not moved: %s", h.Central.BaseURL())
	}
}

// The setup user (often a district officer) may not see every program facility staff
// work in. The hub caches the union of the programs its users can see.
func TestProgramsBeyondSetupUser(t *testing.T) {
	e := newEnv(t)
	e.central.Users["hub_admin"].NoProgramMeta = true
	e.setup()
	if n := len(e.app.Hub.Catalog().Programs); n != 0 {
		t.Fatalf("setup user sees %d programs", n)
	}
	a := e.web("nurse_a")
	a.login()
	if e.app.Hub.Catalog().Programs[fc.Program] == nil {
		t.Fatal("nurse_a's program not added at login")
	}
	e.sync()
	if e.app.Hub.Catalog().Programs[fc.Program] == nil {
		t.Fatal("program dropped on sync")
	}
	e.offline()
	got := uids(a.json("GET", "/api/tracker/trackedEntities?orgUnits="+fc.Facility+"&program="+fc.Program, nil, 200)["trackedEntities"], "trackedEntity")
	if !contains(got, "SeedTe00001") {
		t.Fatalf("program not pulled for nurse_a: %v", got)
	}
}

// REVIEW-2 F5: a central user outside the facility gets no key slot or stored token.
func TestOutsiderNotRegistered(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.web("outsider_d").login()
	if u, _ := e.app.Hub.Store.UserByName("outsider_d"); u != nil {
		t.Fatal("outsider got a key slot")
	}
}

// REVIEW-2 F4: per-user metadata is not served from another user's cache offline;
// offline answers say so in X-Hub-Mode, and narrowed searches in X-Hub-Scope.
func TestPerUserMetadataNotShared(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	b := e.web("nurse_b")
	b.login()
	tree := "/api/42/organisationUnits?fields=id&withinUserHierarchy=true"
	a.do("GET", tree, nil)
	a.do("GET", "/api/organisationUnits/"+fc.Facility+"?fields=id", nil)
	e.offline()
	if st, _ := b.do("GET", tree, nil); st != 503 {
		t.Fatalf("nurse_b got nurse_a's org unit tree offline: %d", st)
	}
	if st, _ := b.do("GET", "/api/organisationUnits/"+fc.Facility+"?fields=id", nil); st != 200 {
		t.Fatalf("one org unit by UID not shared: %d", st)
	}
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/tracker/trackedEntities?orgUnitMode=ACCESSIBLE&program="+fc.Program, nil)
	req.SetBasicAuth("nurse_a", fc.Password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Hub-Mode") != "offline" || resp.Header.Get("X-Hub-Scope") != "facility" {
		t.Fatalf("offline search headers: %d %v", resp.StatusCode, resp.Header)
	}
}

// REVIEW-2 F8: without password_fallback, a user the hub cannot create a PAT for has
// no stored password, and offline changes are refused rather than queued for ever.
func TestNoPasswordFallback(t *testing.T) {
	e := newEnv(t)
	e.central.Users["nurse_a"].NoPAT = true
	e.setup()
	a := e.web("nurse_a")
	a.login()
	if u, _ := e.app.Hub.Store.UserByName("nurse_a"); u == nil || u.HasPAT || u.HasPassword {
		t.Fatalf("user: %+v", u)
	}
	e.offline()
	a.json("POST", postSync, newPatient("NoPatTe0001", "NoPatEn0001", "NoPat", "", nil), 503)
	if n := e.app.Hub.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("%d entries queued without a way to send them", n)
	}
}

// REVIEW-2 F7, F9: no wildcard Host, security headers on hub pages, and form posts
// without Origin or Sec-Fetch-Site are refused.
func TestHubPageHardening(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "hub.yaml")
	os.WriteFile(f, []byte("hub_hosts: *\n"), 0o644)
	if _, err := config.Load(f); err == nil {
		t.Fatal(`hub_hosts: "*" accepted`)
	}
	e := newEnv(t)
	resp, err := http.Get(e.srv.URL + "/hub/setup")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, k := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options"} {
		if resp.Header.Get(k) == "" {
			t.Errorf("missing %s", k)
		}
	}
	resp, err = http.PostForm(e.srv.URL+"/hub/setup", url.Values{"step": {"login"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("post without Origin or Sec-Fetch-Site: %d", resp.StatusCode)
	}
}

// REVIEW-2 F9: a hub the registry marks as replaced stops serving patient data
// offline, keeps pushing, wipes nothing, and serves again if the entry is restored.
func TestRetiredHubStopsServing(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	h := e.app.Hub
	setStatus := func(status string) {
		v := e.central.DataStoreValue("facility-hub-registry", h.HubID())
		v["status"] = status
		b, _ := json.Marshal(v)
		e.central.DataStore["facility-hub-registry"][h.HubID()] = b
		e.sync()
	}
	search := "/api/tracker/trackedEntities?orgUnits=" + fc.Facility + "&program=" + fc.Program

	setStatus("retired")
	e.offline()
	a.json("POST", postSync, newPatient("RetTe000001", "RetEn000001", "Ret", "", nil), 503)
	a.json("GET", search, nil, 503)
	if h.Index.Len() == 0 {
		t.Fatal("retired hub wiped its data")
	}
	e.online()
	setStatus("active")
	e.offline()
	a.json("GET", search, nil, 200)
}
