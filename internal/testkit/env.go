package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/app"
	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// Env is one hub between test clients and a central: fake, or the live instance the
// HUB_MANIFEST environment variable describes.
type Env struct {
	T       testing.TB
	M       *Manifest
	Net     *Netcut  // the hub's uplink
	Central *Central // central directly, as the superuser
	Fake    *fc.Central
	Dir     string
	App     *app.App
	Srv     *httptest.Server

	listen string   // fixed address to serve on (ServeOn); default an ephemeral port
	hosts  []string // extra hub host names
	recMu  sync.Mutex
	rec    []Request
}

// Request is one answer the hub gave, as recorded for Requests.
type Request struct {
	Method, Path, Query string
	Status              int
	Online              bool
}

// Target returns the manifest of the central under test: the live instance in
// HUB_MANIFEST, or a fresh fake.
func Target(t testing.TB) (*Manifest, *fc.Central) {
	t.Helper()
	if p := os.Getenv("HUB_MANIFEST"); p != "" {
		m, err := LoadManifest(p)
		if err != nil {
			t.Fatal(err)
		}
		return m, nil
	}
	return Fake(t)
}

// Live reports whether tests run against a real DHIS2.
func (e *Env) Live() bool { return e.Fake == nil }

// NeedFake skips a test that relies on the fake's fault injection.
func (e *Env) NeedFake() {
	e.T.Helper()
	if e.Live() {
		e.T.Skip("needs the fake central's fault injection")
	}
}

// Start builds a hub on a fresh data directory, with its uplink through a Netcut.
func Start(t testing.TB) *Env {
	t.Helper()
	m, f := Target(t)
	n, err := NewNetcut(m.CentralURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	e := &Env{T: t, M: m, Net: n, Fake: f, Dir: t.TempDir(),
		Central: NewCentral(m.CentralURL, m.AdminUser, m.AdminPassword)}
	e.boot()
	t.Cleanup(e.stop)
	return e
}

func (e *Env) Config() config.Config {
	c := config.Default()
	c.CentralURL = e.Net.URL()
	c.SetupUserGroup = e.M.SetupGroup
	c.DataDir = e.Dir
	c.TLS = false
	c.SyncInterval = time.Hour // tests run cycles by hand
	c.PingTimeout = 5 * time.Second
	c.ReservedPoolSize = 10
	c.OwnershipCheckInterval = 0 // every cycle: tests sync by hand
	c.AccessRefreshInterval = 0  // likewise
	c.ReconnectDelay = 0
	c.LogRequests = true
	c.HubHosts = append(c.HubHosts, e.hosts...)
	return c
}

func (e *Env) boot() {
	a, err := app.New(e.Config(), "0.1.0", false, nil)
	if err != nil {
		e.T.Fatal(err)
	}
	e.App = a
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		a.Handler.ServeHTTP(sw, r)
		e.recMu.Lock()
		e.rec = append(e.rec, Request{r.Method, r.URL.Path, r.URL.RawQuery, sw.status, !e.Net.Down()})
		e.recMu.Unlock()
	})
	if e.listen == "" {
		e.Srv = httptest.NewServer(h)
	} else {
		l, err := net.Listen("tcp", e.listen)
		if err != nil {
			e.T.Fatal(err)
		}
		e.Srv = httptest.NewUnstartedServer(h)
		e.Srv.Listener.Close()
		e.Srv.Listener = l
		e.Srv.Start()
		e.Srv.URL = "http://127.0.0.1:" + strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	}
	e.probe()
}

func (e *Env) stop() {
	if e.Srv != nil {
		e.Srv.Close()
		e.App.Close()
		e.Srv = nil
	}
}

// ServeOn moves the hub to a fixed address (e.g. ":49237", for a device outside this
// process), reachable also under hosts. Call it before Setup: it reboots the hub.
func (e *Env) ServeOn(addr string, hosts ...string) {
	e.listen, e.hosts = addr, hosts
	e.Reboot()
}

// Requests returns what the hub has answered so far, and forgets it.
func (e *Env) Requests() []Request {
	e.recMu.Lock()
	defer e.recMu.Unlock()
	out := e.rec
	e.rec = nil
	return out
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Reboot stops the hub (losing its in-memory key and sessions) and starts it again
// on the same data directory.
func (e *Env) Reboot() {
	e.stop()
	e.boot()
}

func (e *Env) probe() {
	ctx := context.Background()
	e.App.Hub.Monitor.Probe(ctx, e.Net.URL())
	e.App.Hub.Monitor.Probe(ctx, e.Net.URL())
}

// Offline cuts the uplink; Online restores it.
func (e *Env) Offline() {
	e.T.Helper()
	e.Net.SetDown(true)
	e.probe()
	if e.App.Hub.Online() {
		e.T.Fatal("hub still online")
	}
}

func (e *Env) Online() {
	e.T.Helper()
	e.Net.SetDown(false)
	e.probe()
	if !e.App.Hub.Online() {
		e.T.Fatal("hub still offline")
	}
}

// Sync runs one sync cycle and fails the test on error.
func (e *Env) Sync() {
	e.T.Helper()
	if err := e.App.Worker.Cycle(context.Background()); err != nil {
		e.T.Fatalf("sync: %v", err)
	}
}

// Setup sets the hub up as the admin role and runs the first sync.
func (e *Env) Setup() {
	e.T.Helper()
	c := e.Web(RoleAdmin)
	resp, err := c.PostForm("/hub/setup", url.Values{"step": {"login"}, "username": {c.User}, "password": {e.M.Password}})
	if err != nil {
		e.T.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// On a live instance an earlier run's hub may be registered for the facility:
	// confirm replacing it, as an installer would.
	if m := nonceRe.FindSubmatch(b); m != nil && bytes.Contains(b, []byte(`value="confirm"`)) {
		resp, err = c.PostForm("/hub/setup", url.Values{"step": {"confirm"}, "nonce": {string(m[1])}, "replace": {"yes"}})
		if err != nil {
			e.T.Fatal(err)
		}
		b, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if !bytes.Contains(b, []byte("This hub now serves")) {
		e.T.Fatalf("setup failed: %.2000s", b)
	}
	e.Sync()
}

var nonceRe = regexp.MustCompile(`name="nonce" value="([^"]+)"`)

// --- clients ------------------------------------------------------------------------

// Client is a device using the hub: the web app (session cookie) or the Android app
// (Basic auth on every request).
type Client struct {
	e     *Env
	HTTP  *http.Client
	Role  string
	User  string
	basic bool
}

func (e *Env) user(role string) User {
	e.T.Helper()
	u, ok := e.M.User(role)
	if !ok {
		e.T.Skipf("this setup has no %s role", role)
	}
	return u
}

func (e *Env) Web(role string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{e: e, Role: role, User: e.user(role).Username, HTTP: &http.Client{Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (e *Env) Android(role string) *Client {
	return &Client{e: e, Role: role, User: e.user(role).Username, basic: true, HTTP: &http.Client{}}
}

func (c *Client) Do(method, path string, body any) (int, []byte) {
	c.e.T.Helper()
	st, _, b := c.do(method, path, body)
	return st, b
}

// DoHeader is a GET that also returns the answer's headers (X-Hub-Mode says whether
// the hub answered itself).
func (c *Client) DoHeader(method, path string) (int, http.Header, []byte) {
	c.e.T.Helper()
	return c.do(method, path, nil)
}

func (c *Client) do(method, path string, body any) (int, http.Header, []byte) {
	c.e.T.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.Srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if c.basic {
		req.SetBasicAuth(c.User, c.e.M.Password)
		req.Header.Set("User-Agent", "okhttp/4.12.0")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.e.T.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// JSON does a request, requires the status, and decodes an object answer.
func (c *Client) JSON(method, path string, body any, want int) map[string]any {
	c.e.T.Helper()
	st, b := c.Do(method, path, body)
	if st != want {
		c.e.T.Fatalf("%s %s as %s: got %d want %d: %.1500s", method, path, c.Role, st, want, b)
	}
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

func (c *Client) Login() {
	c.e.T.Helper()
	st, b := c.Do("POST", "/api/auth/login", map[string]string{"username": c.User, "password": c.e.M.Password})
	if st != 200 || !bytes.Contains(b, []byte("SUCCESS")) {
		c.e.T.Fatalf("login %s: %d %s", c.Role, st, b)
	}
}

// PostForm posts a form the way a browser on the hub's own page does.
func (c *Client) PostForm(path string, form url.Values) (*http.Response, error) {
	req, _ := http.NewRequest("POST", c.e.Srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", c.e.Srv.URL)
	return c.HTTP.Do(req)
}

// CentralObject reads a tracker object from central directly as the superuser:
// kind is trackedEntities, enrollments or events. It returns nil if central does not
// have it.
func (e *Env) CentralObject(kind, uid string) map[string]any {
	e.T.Helper()
	if e.Fake != nil {
		if o := e.Fake.Object(uid); o != nil {
			return o
		}
		return nil
	}
	st, b, err := e.Central.Do("GET", "/api/tracker/"+kind+"/"+uid+"?fields=*", nil)
	if err != nil {
		e.T.Fatal(err)
	}
	if st != 200 {
		return nil
	}
	var o map[string]any
	json.Unmarshal(b, &o)
	return o
}

// CentralImport writes a tracker payload to central directly as the superuser, as if
// someone elsewhere had entered it.
func (e *Env) CentralImport(payload map[string]any) {
	e.T.Helper()
	if e.Fake != nil {
		e.T.Fatal("CentralImport: use the fake's Seed")
	}
	st, b, err := e.Central.Do("POST", SyncImport, payload)
	if err != nil || st != 200 {
		e.T.Fatalf("central import: %d %v %.1500s", st, err, b)
	}
}

// CentralDelete deletes a tracker object on central directly as the superuser, as
// someone elsewhere would: kind is trackedEntities, enrollments or events. Central
// keeps it as deleted (soft delete), with what is under it.
func (e *Env) CentralDelete(kind, uid string) {
	e.T.Helper()
	if e.Fake != nil {
		e.Fake.SoftDelete(uid)
		return
	}
	key := map[string]string{"trackedEntities": "trackedEntity", "enrollments": "enrollment", "events": "event"}[kind]
	st, b, err := e.Central.Do("POST", "/api/tracker?async=false&importStrategy=DELETE",
		map[string]any{kind: []any{map[string]any{key: uid}}})
	if err != nil || st != 200 {
		e.T.Fatalf("central delete %s %s: %d %v %.1500s", kind, uid, st, err, b)
	}
}

// AndroidUser is an Android client for any user of the manifest (a matrix user).
func (e *Env) AndroidUser(username string) *Client {
	return &Client{e: e, Role: username, User: username, basic: true, HTTP: &http.Client{}}
}

// SetUplink cuts or restores the uplink without failing the test: for control
// endpoints called from other goroutines (the client drivers).
func (e *Env) SetUplink(up bool) error {
	e.Net.SetDown(!up)
	e.probe()
	if e.App.Hub.Online() != up {
		return fmt.Errorf("hub online=%v, want %v", e.App.Hub.Online(), up)
	}
	return nil
}
