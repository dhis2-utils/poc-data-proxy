// Regression tests for the findings in docs/REVIEW.md.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// R-05: a wrong Basic password from anyone gets central's 401 and nothing more; it
// does not end the user's sessions or block their offline login. Only a password that
// worked before (cached, or opening the key slot) means the account changed on central.
func TestWrongBasicPasswordKeepsUserLoggedIn(t *testing.T) {
	e := newEnv(t)
	e.setup()
	search := "/api/tracker/trackedEntities?orgUnits=" + fc.Facility + "&program=" + fc.Program
	web := e.web("nurse_a")
	web.login()
	if st, _ := e.androidPw("nurse_a", "x").do("GET", "/api/me", nil); st != 401 {
		t.Fatalf("a wrong Basic password: %d, want 401", st)
	}
	if u, _ := e.app.Hub.Store.UserByName("nurse_a"); u.NeedsLogin {
		t.Fatal("a wrong Basic password marked nurse_a as needing an online login")
	}
	web.json("GET", search, nil, 200)
	e.offline()
	web.json("GET", search, nil, 200)
	e.web("nurse_a").login()
}

// R-36: the hub's login page refuses a cross-site post, and logout acts only on a
// same-origin POST, not on a link or image elsewhere.
func TestHubLoginOriginAndLogoutPost(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("hub_admin")
	req, _ := http.NewRequest("POST", e.srv.URL+"/hub/login", strings.NewReader(url.Values{"username": {"hub_admin"}, "password": {fc.Password}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://evil.example")
	resp, err := a.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("cross-site login: %d, want 403", resp.StatusCode)
	}
	resp, err = a.postForm("/hub/login", url.Values{"username": {"hub_admin"}, "password": {fc.Password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/hub/ui" {
		t.Fatalf("login: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if st, _ := a.do("GET", "/hub/logout", nil); st != 302 {
		t.Fatalf("GET logout: %d", st)
	}
	if st, b := a.do("GET", "/hub/ui", nil); st != 200 || !strings.Contains(string(b), "Log out") {
		t.Fatalf("a GET of /hub/logout ended the session: %d", st)
	}
	resp, err = a.postForm("/hub/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st, _ := a.do("GET", "/hub/ui", nil); st != 302 {
		t.Fatalf("still logged in after POST logout: %d", st)
	}
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

// R-04: parallel wrong passwords get no more checks than the limit; the attempt is
// counted before the password is checked, not after.
func TestLoginThrottleParallel(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.web("nurse_b").login()
	e.offline()
	codes := make(chan int, 40)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, _ := e.web("nurse_b").do("POST", "/api/auth/login", map[string]string{"username": "nurse_b", "password": "guess"})
			codes <- st
		}()
	}
	wg.Wait()
	close(codes)
	n := map[int]int{}
	for st := range codes {
		n[st]++
	}
	if n[401] > 5 || n[401]+n[429] != 40 {
		t.Fatalf("40 parallel wrong passwords: %v, want at most 5 checked (401), the rest 429", n)
	}
}

// L2: during a lockout, Basic auth answers a right and a wrong password alike (429),
// though the hub caches the right one from the staff member's tablet: otherwise the
// lockout told them apart, at once. The tablet itself, at its own address, goes on
// working with its cached credential, even when its address is locked out too.
func TestBasicLockoutAnswersRightAndWrongAlike(t *testing.T) {
	e := newEnv(t)
	e.setup()
	tablet := e.android("nurse_b")
	if st, b := tablet.do("GET", "/api/me", nil); st != 200 {
		t.Fatalf("the tablet's first request: %d %s", st, b)
	}
	// Another device on the facility network (the hub sees its address).
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "10.66.0.9:40000"
		e.app.Handler.ServeHTTP(w, r)
	}))
	defer other.Close()
	try := func(srv, method, path, pw string, body any) int {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, srv+path, rd)
		req.Header.Set("Content-Type", "application/json")
		if pw != "" {
			req.SetBasicAuth("nurse_b", pw)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, srv := range []string{other.URL, e.srv.URL} { // both addresses locked out
		for range 5 {
			if st := try(srv, "POST", "/api/auth/login", "", map[string]string{"username": "nurse_b", "password": "guess"}); st != 401 {
				t.Fatalf("a wrong password: %d, want 401", st)
			}
		}
	}
	if st := try(other.URL, "GET", "/api/me", "guess-2", nil); st != 429 {
		t.Errorf("locked out, a wrong Basic password from another device: %d, want 429", st)
	}
	if st := try(other.URL, "GET", "/api/me", fc.Password, nil); st != 429 {
		t.Errorf("locked out, the right Basic password from another device: %d, want 429 as for a wrong one", st)
	}
	if st, b := tablet.do("GET", "/api/me", nil); st != 200 {
		t.Errorf("locked out, the tablet with its cached credential: %d %s, want 200", st, b)
	}
}

// M8, REVIEW-2 F1: nobody at the setup page can choose the central server. With
// central_url only, the form value is ignored; with central_hosts, the URL must be on
// them; with neither, setup needs the token from the label. Cross-site posts are
// refused, and a failed attempt leaves the hub's central URL as it was.
func TestSetupCentralURL(t *testing.T) {
	e := newEnv(t)
	h := e.app.Hub
	post := func(origin string, form url.Values) (int, string) {
		if form.Get("step") == "" {
			form.Set("step", "login")
		}
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
	server := url.Values{"step": {"server"}, "central_url": {"https://attacker.example"}}
	if _, page := post(e.srv.URL, server); !strings.Contains(page, "wrong setup code") {
		t.Fatalf("setup without token: %s", page)
	}
	// A login posted straight away, with an address of its choosing, is refused: only
	// an address the server step checked (with the code) is used.
	if _, page := post(e.srv.URL, url.Values{"central_url": {"https://attacker.example"}}); !strings.Contains(page, "start with central") {
		t.Fatalf("login without the server step: %s", page)
	}
	tok, _ := os.ReadFile(filepath.Join(e.dir, "setup-token"))
	if _, page := post(e.srv.URL, url.Values{"step": {"server"}, "central_url": {"http://attacker.example"}, "setup_token": {string(tok)}}); !strings.Contains(page, "https://") {
		t.Fatalf("plain http central URL accepted: %s", page)
	}

	h.Cfg.CentralURL, h.Cfg.CentralHosts = e.cSrv.URL, []string{"*.hmis.example"}
	if _, page := post(e.srv.URL, url.Values{"step": {"server"}, "central_url": {"https://attacker.example"}}); !strings.Contains(page, "must be an https URL on") {
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

// REVIEW-2 F4: metadata is not served from another user's copy offline: central
// answers it per user (metadata sharing, from public, group and individual sharing;
// display names in the user's language), and the hub does not evaluate that. Only
// answers that do not depend on who asks are shared. Offline answers say so in
// X-Hub-Mode, and narrowed searches in X-Hub-Scope.
func TestPerUserMetadataNotShared(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	b := e.web("nurse_b")
	b.login()
	// nurse_b's screens load at login (proxy.ReplayAfterLogin) before nurse_a opens
	// these: what is checked is that nurse_b never gets nurse_a's copy.
	e.app.Proxy.WaitReplays()
	perUser := []string{
		"/api/42/organisationUnits?fields=id&withinUserHierarchy=true",
		"/api/organisationUnits/" + fc.Facility + "?fields=id,displayName",
		"/api/programs?fields=id,displayName&paging=false",
		"/api/system/info?fields=version",
	}
	for _, p := range perUser {
		a.do("GET", p, nil)
	}
	a.do("GET", "/api/loginConfig", nil)
	e.offline()
	for _, p := range perUser {
		if st, _ := b.do("GET", p, nil); st != 503 {
			t.Errorf("nurse_b got nurse_a's copy of %s offline: %d", p, st)
		}
	}
	if st, _ := b.do("GET", "/api/loginConfig", nil); st != 200 {
		t.Errorf("the login page's settings, the same for everyone, not shared: %d", st)
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
	// A browser on the hub's own page, under its no-referrer policy: Origin is null.
	for site, want := range map[string]bool{"same-origin": true, "cross-site": false, "": false} {
		req, _ := http.NewRequest("POST", e.srv.URL+"/hub/setup", strings.NewReader(url.Values{"step": {"login"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "null")
		if site != "" {
			req.Header.Set("Sec-Fetch-Site", site)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.StatusCode != 403; got != want {
			t.Errorf("Origin null, Sec-Fetch-Site %q: status %d", site, resp.StatusCode)
		}
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

// T2/T4, as decided 2026-10-30: the pool size is a per-attribute setting managed in
// the Facility Hub app (facility-hub-config/reserved), with reserved_pool_size as the
// default when there is none; refills run from a low watermark (target/4) up to the
// target, in requests of at most central's 1000; and a tablet gets up to Android's
// own 100 values per request when the pool allows it.
func TestReservedPoolSetting(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.android("nurse_a")
	a.json("GET", "/api/me", nil, 200)
	now := func() string { return e.app.Hub.CentralTime(time.Now()) }
	reserve := func(n int) int {
		t.Helper()
		st, body := a.do("GET", "/api/trackedEntityAttributes/"+fc.AttrTBID+"/generateAndReserve?numberToReserve="+fmt.Sprint(n), nil)
		var vals []any
		json.Unmarshal(body, &vals)
		if st != 200 {
			t.Fatalf("reserve %d: %d %.200s", n, st, body)
		}
		return len(vals)
	}

	// No setting: the config default (5 in this env) is the target.
	e.sync()
	if have := e.app.Hub.Store.CountReservedValues(fc.AttrTBID, now()); have != e.app.Hub.Cfg.ReservedPoolSize {
		t.Fatalf("no setting: pool %d, want the config default %d", have, e.app.Hub.Cfg.ReservedPoolSize)
	}

	// The app's setting: a default of 40 and an override of 250 for the TB identifier.
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"reserved": json.RawMessage(`{"default": 40, "attributes": {"` + fc.AttrTBID + `": 250}}`),
	}
	if err := e.app.Hub.RefreshCatalog(context.Background()); err != nil { // the cycle refreshes the catalog only at its interval
		t.Fatal(err)
	}
	e.sync()
	if have := e.app.Hub.Store.CountReservedValues(fc.AttrTBID, now()); have != 250 {
		t.Fatalf("setting override: pool %d, want 250", have)
	}
	// Offline, a tablet asking for Android's 100 gets 100 while the pool allows it.
	e.offline()
	if got := reserve(100); got != 100 {
		t.Errorf("T4: a tablet asking for 100 values got %d", got)
	}
	// Above the low watermark (250/4 = 62) the next sync refills nothing: 150 >= 62.
	e.online()
	e.sync()
	if have := e.app.Hub.Store.CountReservedValues(fc.AttrTBID, now()); have != 150 {
		t.Errorf("above the low watermark: pool %d after the sync, want 150 (no refill)", have)
	}
	// Below it, the sync refills up to the target.
	e.offline()
	reserve(100) // 50 left < 62
	e.online()
	e.sync()
	if have := e.app.Hub.Store.CountReservedValues(fc.AttrTBID, now()); have != 250 {
		t.Errorf("below the low watermark: pool %d after the sync, want 250", have)
	}

	// A target below the watermark's divisor is exact: 3 refills even at empty
	// (the division once truncated 3/4 to 0, so a target of 1–3 never refilled).
	e.offline()
	reserve(100) // 150 left
	reserve(100) // 50 left
	reserve(100) // empty
	e.online()
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"reserved": json.RawMessage(`{"default": 40, "attributes": {"` + fc.AttrTBID + `": 3}}`),
	}
	if err := e.app.Hub.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.sync()
	if have := e.app.Hub.Store.CountReservedValues(fc.AttrTBID, now()); have != 3 {
		t.Fatalf("target 3: pool %d after the sync, want 3", have)
	}
	// Offline, a target of 3 caps a request at 3/2 = 1 value: the pool drains one
	// per request and refills again at empty.
	e.offline()
	for i := 0; i < 3; i++ {
		if got := reserve(1); got != 1 {
			t.Errorf("target 3: a request for 1 value got %d", got)
		}
	}
	if have := e.app.Hub.Store.CountReservedValues(fc.AttrTBID, now()); have != 0 {
		t.Fatalf("target 3: pool %d after three requests, want 0", have)
	}
	e.online()
	e.sync()
	if have := e.app.Hub.Store.CountReservedValues(fc.AttrTBID, now()); have != 3 {
		t.Errorf("target 3: pool %d after refilling at empty, want 3", have)
	}

	// An explicit 0 turns the attribute's pool off: no refill while online, and
	// offline requests get needs-connection even when leftovers sit in the pool.
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"reserved": json.RawMessage(`{"default": 40, "attributes": {"` + fc.AttrTBID + `": 0}}`),
	}
	if err := e.app.Hub.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.sync()
	if have := e.app.Hub.Store.CountReservedValues(fc.AttrTBID, now()); have != 3 {
		t.Errorf("target 0: pool %d after the sync, want the 3 leftovers untouched", have)
	}
	e.offline()
	st, body := a.do("GET", "/api/trackedEntityAttributes/"+fc.AttrTBID+"/generateAndReserve?numberToReserve=5", nil)
	if st != 503 {
		t.Errorf("target 0: generateAndReserve offline: %d %.200s, want 503 needs-connection", st, body)
	}
	if have := e.app.Hub.Store.CountReservedValues(fc.AttrTBID, now()); have != 3 {
		t.Errorf("target 0: pool %d after the refused request, want the 3 leftovers", have)
	}
}

// A22: when a generated attribute's pattern changes on central, the next sync empties
// the hub's pool for it and refills under the new pattern, so tablets stop getting
// values of the old one. A sync with the pattern unchanged keeps the pool.
func TestReservedPoolPatternChange(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.android("nurse_a")
	a.json("GET", "/api/me", nil, 200)
	count := func() int { return e.app.Hub.Store.CountReservedValues(fc.AttrTBID, e.app.Hub.CentralTime(time.Now())) }
	reserve := func(n int) {
		t.Helper()
		st, body := a.do("GET", "/api/trackedEntityAttributes/"+fc.AttrTBID+"/generateAndReserve?numberToReserve="+fmt.Sprint(n), nil)
		if st != 200 {
			t.Fatalf("reserve %d offline: %d %.200s", n, st, body)
		}
	}
	full := e.app.Hub.Cfg.ReservedPoolSize // 5 in this env
	e.sync()
	if have := count(); have != full {
		t.Fatalf("pool %d after the first sync, want %d", have, full)
	}
	e.offline()
	reserve(1)
	e.online()
	e.sync()
	if have := count(); have != full-1 {
		t.Fatalf("pattern unchanged: pool %d after the sync, want %d kept (above the low watermark)", have, full-1)
	}

	e.central.GeneratedPattern = `"PX" + SEQUENTIAL(#######)`
	if err := e.app.Hub.RefreshCatalog(context.Background()); err != nil { // the cycle refreshes it only at its interval
		t.Fatal(err)
	}
	e.sync()
	// Without the drop, full-1 values of the old pattern stay: above the watermark, no refill.
	if have := count(); have != full {
		t.Errorf("pattern changed: pool %d after the sync, want %d (old values dropped, refilled)", have, full)
	}
	e.offline()
	reserve(1)
	e.online()
	e.sync()
	if have := count(); have != full-1 {
		t.Errorf("new pattern unchanged since: pool %d after the sync, want %d kept", have, full-1)
	}
}
