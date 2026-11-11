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
// B1, decided 2026-11-02: the probe cadence follows the state — ping_interval while
// offline, four times that while online. No sleeps: Probe is driven by hand.
func TestProbeCadenceFollowsTheState(t *testing.T) {
	var mu sync.Mutex
	down := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if down {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte("pong"))
	}))
	defer srv.Close()
	m := NewMonitor(2*time.Second, time.Second)
	if !m.Probe(context.Background(), srv.URL) {
		t.Fatal("probe failed while up")
	}
	if d := m.nextDelay(); d != 2*time.Second*probeOnlineEvery {
		t.Errorf("after a success: nextDelay %s, want 8s", d)
	}
	mu.Lock()
	down = true
	mu.Unlock()
	m.Probe(context.Background(), srv.URL) // failure 1: still online
	if !m.Online() {
		t.Fatal("offline after one failure")
	}
	if d := m.nextDelay(); d != 2*time.Second {
		t.Errorf("after one failure (still online): nextDelay %s, want the interval 2s", d)
	}
	m.Probe(context.Background(), srv.URL) // failure 2: flips offline
	if m.Online() {
		t.Fatal("still online after two failures")
	}
	if d := m.nextDelay(); d != 2*time.Second {
		t.Errorf("after the two failures that flip offline: nextDelay %s, want the interval 2s", d)
	}
	mu.Lock()
	down = false
	mu.Unlock()
	if !m.Probe(context.Background(), srv.URL) {
		t.Fatal("probe failed while up")
	}
	if d := m.nextDelay(); d != 2*time.Second*probeOnlineEvery {
		t.Errorf("after a success again: nextDelay %s, want 8s", d)
	}
}

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

// A transport error does not carry the request's query: aggregate values and search
// terms travel there, and the error reaches logs and the heartbeat (review R-44).
func TestTransportErrorOmitsQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // nothing listens: connection refused
	c := NewClient(srv.URL, NewMonitor(time.Second, time.Second))
	_, err := c.Do(context.Background(), "POST", "/api/dataEntry/dataValues?de=x&value=Amina", nil, Auth{}, nil)
	if err == nil || !IsNetwork(err) {
		t.Fatalf("got %v, want a network error", err)
	}
	if strings.Contains(err.Error(), "Amina") || !strings.Contains(err.Error(), "/api/dataEntry/dataValues") {
		t.Errorf("error text: %v", err)
	}
}

// Review R-21: a request to central that times out asks for a probe now, and the
// probes decide. A slow request while central answers its pings leaves the hub online;
// a real outage still takes it offline, by the probes' rule (two failures), within
// about one interval of the failed request rather than the online cadence.
func TestFailedRequestAsksForAProbe(t *testing.T) {
	var mu sync.Mutex
	pings := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/system/ping" {
			mu.Lock()
			pings++
			mu.Unlock()
			w.Write([]byte("pong"))
			return
		}
		time.Sleep(500 * time.Millisecond) // a heavy analytics query
	}))
	defer srv.Close()
	pingCount := func() int { mu.Lock(); defer mu.Unlock(); return pings }
	const interval = time.Second // online cadence 4 s
	m := NewMonitor(interval, time.Second)
	c := NewClient(srv.URL, m)
	c.HTTP.Timeout = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx, c.BaseURL)
	waitFor := func(what string, within time.Duration, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(within); !ok(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("not %s within %s", what, within)
			}
		}
	}
	waitFor("online", 2*time.Second, m.Online)
	before := pingCount()

	if _, err := c.Do(context.Background(), "GET", "/api/analytics", nil, Auth{}, nil); !IsNetwork(err) {
		t.Fatalf("slow request: got %v, want a timeout", err)
	}
	waitFor("probed after the slow request", 2*time.Second, func() bool { return pingCount() > before })
	time.Sleep(interval + 200*time.Millisecond) // past a second probe, had the first failed
	if !m.Online() {
		t.Fatal("a slow request took the hub offline while central answered its pings")
	}

	srv.Close() // the outage: nothing answers
	start := time.Now()
	if _, err := c.Do(context.Background(), "GET", "/api/x", nil, Auth{}, nil); !IsNetwork(err) {
		t.Fatalf("during the outage: got %v, want a network error", err)
	}
	waitFor("offline after an outage", interval+time.Second, func() bool { return !m.Online() })
	if d := time.Since(start); d < interval/2 {
		t.Errorf("offline after %s: one failed probe switched it, not two", d)
	}
}
