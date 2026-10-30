package central

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Review M6: the error text names the status and code, never central's body.
func TestHTTPErrorOmitsBody(t *testing.T) {
	e := &HTTPError{Status: 409, Body: []byte(`{"errorCode":"E1064","message":"value 'Amina' already exists"}`)}
	if got := e.Error(); got != "central returned 409 (E1064)" {
		t.Fatal(got)
	}
	e = &HTTPError{Status: 400, Body: []byte(`Bad request: Amina`)}
	if got := e.Error(); strings.Contains(got, "Amina") {
		t.Fatal(got)
	}
}

// Review 2 F9: a captive portal does not make the hub think central is reachable.
func TestProbeTellsCentralFromPortal(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dhis-web-login/", http.StatusFound) // 2.42, anonymous
	}))
	defer central.Close()
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://portal.example/login", http.StatusFound)
	}))
	defer portal.Close()
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>Welcome to hotel Wi-Fi</html>"))
	}))
	defer html.Close()
	for url, want := range map[string]bool{central.URL: true, portal.URL: false, html.URL: false} {
		m := NewMonitor(time.Second, time.Second)
		if got := m.Probe(context.Background(), url); got != want {
			t.Errorf("%s: got %v, want %v", url, got, want)
		}
	}
}

// Central's answers name it in more forms than its configured address: behind a TLS
// proxy that does not say so it redirects to http://, and DHIS2 under a context path
// (https://x.org/dhis) redirects to /dhis/login/ and answers a login with /dhis/.
// Each form is a path on the hub; anything else is left alone.
func TestLocal(t *testing.T) {
	for _, tc := range []struct{ base, in, want string }{
		{"https://x.org/facility-hub", "https://x.org/facility-hub/login/", "/login/"},
		{"https://x.org/facility-hub", "http://x.org/facility-hub/login/", "/login/"},
		{"https://x.org/facility-hub", "/facility-hub/login/", "/login/"},
		{"https://x.org/facility-hub", "/facility-hub/", "/"},
		{"https://x.org/facility-hub", "/facility-hub", "/"},
		{"https://x.org/facility-hub", "https://x.org/facility-hub", "/"},
		{"https://x.org/facility-hub", "/facility-hub?x=1", "/?x=1"},
		{"https://x.org/facility-hub", "/facility-hubx/a", "/facility-hubx/a"},
		{"https://x.org/facility-hub", "https://x.org/facility-hubx", "https://x.org/facility-hubx"},
		{"https://x.org/facility-hub", "https://other.org/facility-hub/a", "https://other.org/facility-hub/a"},
		{"https://x.org", "http://x.org/a", "/a"},
		{"https://x.org", "/login/", "/login/"},
		{"https://x.org", "https://x.org", "/"},
	} {
		c := NewClient(tc.base, nil)
		if got := c.Local(tc.in); got != tc.want {
			t.Errorf("base %s: Local(%q) = %q, want %q", tc.base, tc.in, got, tc.want)
		}
	}
	if got := NewClient("https://x.org/dhis", nil).BaseURLs(); len(got) != 2 || got[0] != "https://x.org/dhis" || got[1] != "http://x.org/dhis" {
		t.Errorf("BaseURLs = %v", got)
	}
}

// D6: every probe opens a new connection instead of reusing a kept-alive one, so a
// probe cannot succeed on a stale connection while new connections fail (DNS down,
// a firewall refusing them): the hub then flapped online and offline every probe.
func TestProbeOpensANewConnectionEachTime(t *testing.T) {
	var mu sync.Mutex
	conns := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns[r.RemoteAddr] = true
		mu.Unlock()
		w.Write([]byte("pong"))
	}))
	defer srv.Close()
	m := NewMonitor(time.Second, time.Second)
	for range 3 {
		if !m.Probe(context.Background(), srv.URL) {
			t.Fatal("probe failed")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 3 {
		t.Errorf("3 probes used %d connections; want one per probe (no keep-alive)", len(conns))
	}
}
