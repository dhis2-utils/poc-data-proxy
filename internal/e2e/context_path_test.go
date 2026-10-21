package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// asUnderContextPath serves the fake central as DHIS2 2.43 does at https://x.org/dhis
// behind a TLS proxy that does not tell it so (seen on implement.im.dhis2.org,
// 2026-10-21): a visitor not logged in is redirected to http://x.org/dhis/login/,
// with the other scheme, and a login answers redirectUrl "/dhis/".
func asUnderContextPath(c *fc.Central, prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, prefix); ok {
			r.URL.Path = rest
		}
		if r.URL.Path == "" || r.URL.Path == "/" {
			if r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "" {
				http.Redirect(w, r, "https://"+r.Host+prefix+"/login/", http.StatusFound)
				return
			}
			http.Redirect(w, r, "https://"+r.Host+prefix+"/apps/capture", http.StatusFound)
			return
		}
		if r.URL.Path != "/api/auth/login" {
			c.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		c.ServeHTTP(rec, r)
		body := rec.Body.Bytes()
		var m map[string]any
		if json.Unmarshal(body, &m) == nil && m["redirectUrl"] != nil {
			m["redirectUrl"] = prefix + "/"
			body, _ = json.Marshal(m)
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(body)
	})
}

// A central under a context path, behind a TLS proxy that names it with the other
// scheme: every redirect it gives, and the login's redirectUrl, must stay on the hub.
// Otherwise a browser that opens dhis2.local ends up on central itself and never opens
// Capture through the hub, so Capture is not there offline (Windows VM, 2026-10-21).
func TestCentralUnderContextPath(t *testing.T) {
	e := &env{t: t, central: fc.New(), dir: t.TempDir()}
	e.cSrv = httptest.NewServer(asUnderContextPath(e.central, "/dhis"))
	t.Cleanup(e.cSrv.Close)
	base := e.cSrv.URL + "/dhis"
	e.cfgHook = func(c *config.Config) { c.CentralURL = base }
	e.start()
	e.setupAt(base)

	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, cookie string) *http.Response {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		req.Header.Set("Accept", "text/html")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	if loc := get("/", "").Header.Get("Location"); loc != e.srv.URL+"/login/" {
		t.Errorf("not logged in, / redirects to %q, want the hub's /login/", loc)
	}

	b, _ := json.Marshal(map[string]string{"username": "nurse_a", "password": fc.Password})
	resp, err := browser.Post(e.srv.URL+"/api/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var login struct{ LoginStatus, RedirectURL string }
	json.NewDecoder(resp.Body).Decode(&login)
	resp.Body.Close()
	if login.LoginStatus != "SUCCESS" || login.RedirectURL != "/" {
		t.Fatalf("login through the hub: %+v, want SUCCESS to /", login)
	}
	var cookie []string
	for _, c := range resp.Cookies() {
		cookie = append(cookie, c.Name+"="+c.Value)
	}
	if loc := get("/", strings.Join(cookie, "; ")).Header.Get("Location"); loc != e.srv.URL+"/apps/capture" {
		t.Errorf("logged in, / redirects to %q, want the hub's /apps/capture", loc)
	}
}

// setupAt is setup with central at url.
func (e *env) setupAt(url string) {
	e.t.Helper()
	c := e.web("hub_admin")
	resp, err := c.postForm("/hub/setup", map[string][]string{"step": {"login"}, "central_url": {url},
		"username": {"hub_admin"}, "password": {fc.Password}})
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "This hub now serves") {
		e.t.Fatalf("setup failed: %s", b)
	}
	e.sync()
}

// A staff member's first login through the hub starts a pull for them at once: until
// it runs, the hub has nothing of theirs to serve offline, and the next cycle can be
// 15 minutes away (found against the demo central, 2026-10-21: a user who logged in and
// then went offline saw empty working lists). Later logins wait for the cycle as before.
func TestFirstLoginStartsAPull(t *testing.T) {
	e := newEnv(t)
	e.setup()
	pending := func() bool {
		select {
		case <-e.app.Hub.SyncNow:
			return true
		default:
			return false
		}
	}
	pending()
	e.web("nurse_a").login()
	if !pending() {
		t.Fatal("nurse_a's first login did not start a pull")
	}
	e.web("nurse_a").login()
	if pending() {
		t.Fatal("a later login of nurse_a started another pull")
	}
}
