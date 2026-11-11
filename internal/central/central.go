// Package central is the hub's HTTP client for the central DHIS2 server and the
// connectivity monitor (spec 5.2).
package central

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Auth is how a request authenticates to central. Exactly one field is normally set.
type Auth struct {
	PAT      string // personal access token: "Authorization: ApiToken <key>"
	Username string // Basic auth (login verification, PAT fallback)
	Password string
	Cookie   string // central session cookie header value, e.g. "JSESSIONID=..."
}

func (a Auth) Apply(h http.Header) {
	h.Del("Authorization")
	h.Del("Cookie")
	switch {
	case a.PAT != "":
		h.Set("Authorization", "ApiToken "+a.PAT)
	case a.Username != "":
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.Username+":"+a.Password)))
	case a.Cookie != "":
		h.Set("Cookie", a.Cookie)
	}
}

// Client talks to central and reports network failures to the monitor.
type Client struct {
	mu      sync.RWMutex
	baseURL string
	HTTP    *http.Client
	Monitor *Monitor
	// SawDate, when set, gets central's clock from the Date header of each answer:
	// the hub's lower bound for the true time when its own clock is wrong (no RTC,
	// docs/NO-RTC.md).
	SawDate func(time.Time)
}

// proxyURL is the web proxy set on the hub's admin page; empty uses the environment's
// (HTTPS_PROXY). One per process, like the environment.
var proxyURL atomic.Pointer[url.URL]

// SetProxy sets the web proxy for every outbound connection ("" for the environment's).
func SetProxy(p string) {
	if u, err := url.Parse(p); p != "" && err == nil {
		proxyURL.Store(u)
		return
	}
	proxyURL.Store(nil)
}

// Proxy is the transports' proxy function.
func Proxy(r *http.Request) (*url.URL, error) {
	if u := proxyURL.Load(); u != nil {
		return u, nil
	}
	return http.ProxyFromEnvironment(r)
}

func NewClient(baseURL string, m *Monitor) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = Proxy
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), Monitor: m,
		HTTP: &http.Client{Transport: tr, Timeout: 5 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) BaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

// BaseURLs are central's address as its answers may name it: as configured, and with
// the other scheme (a central behind a TLS proxy that does not tell it so redirects to
// http://).
func (c *Client) BaseURLs() []string {
	base := c.BaseURL()
	if base == "" {
		return nil
	}
	if rest, ok := strings.CutPrefix(base, "https://"); ok {
		return []string{base, "http://" + rest}
	}
	if rest, ok := strings.CutPrefix(base, "http://"); ok {
		return []string{base, "https://" + rest}
	}
	return []string{base}
}

// Local returns u as a path on the hub when it points at central: at one of its
// BaseURLs, or at an absolute path under its context path (DHIS2 at https://x.org/dhis
// redirects to /dhis/login/). Anything else comes back unchanged.
func (c *Client) Local(u string) string {
	under := func(u, prefix string) (string, bool) {
		rest, ok := strings.CutPrefix(u, prefix)
		if !ok || (rest != "" && !strings.ContainsAny(rest[:1], "/?#")) {
			return u, false
		}
		if rest == "" || rest[0] != '/' {
			rest = "/" + rest
		}
		return rest, true
	}
	for _, b := range c.BaseURLs() {
		if rest, ok := under(u, b); ok {
			return rest
		}
	}
	if strings.HasPrefix(u, "/") {
		if pu, err := url.Parse(c.BaseURL()); err == nil && pu.Path != "" && pu.Path != "/" {
			if rest, ok := under(u, strings.TrimRight(pu.Path, "/")); ok {
				return rest
			}
		}
	}
	return u
}

func (c *Client) SetBaseURL(u string) {
	c.mu.Lock()
	c.baseURL = strings.TrimRight(u, "/")
	c.mu.Unlock()
}

// ErrNetwork wraps transport failures (no HTTP response).
var ErrNetwork = errors.New("central unreachable")

// IsNetwork reports whether err means central could not be reached.
func IsNetwork(err error) bool { return errors.Is(err, ErrNetwork) }

// Do sends a request. pathAndQuery starts with "/". A network error asks the monitor
// for a probe now (spec 5.2) and is returned wrapped in ErrNetwork.
func (c *Client) Do(ctx context.Context, method, pathAndQuery string, body []byte, auth Auth, hdr http.Header) (*http.Response, error) {
	base := c.BaseURL()
	if c.Monitor.ForcedOffline() {
		return nil, fmt.Errorf("%w: uplink switched off on the hub", ErrNetwork)
	}
	if base == "" {
		return nil, fmt.Errorf("%w: central URL not configured", ErrNetwork)
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+pathAndQuery, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	auth.Apply(req.Header)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The URL in a transport error reaches logs, the outbox's last error and the
		// heartbeat; its query can hold data values and search terms (review R-44).
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL, _, _ = strings.Cut(ue.URL, "?")
		}
		c.Failed(ctx, err)
		return nil, fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	if c.SawDate != nil {
		if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
			c.SawDate(d)
		}
	}
	return resp, nil
}

// Failed reports a request to central that failed without an answer, or whose answer
// could not be read, as central unreachable: unless the client that asked went away
// and cancelled it (a browser leaving the page, as on logout).
func (c *Client) Failed(ctx context.Context, err error) {
	if ctx.Err() == nil || isTimeout(err) {
		c.Monitor.ReportFailure(err)
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// HTTPError is a non-2xx answer from central.
type HTTPError struct {
	Status int
	Body   []byte
}

// Error names the status and DHIS2 error code only. Central's body can quote
// submitted values, and this text reaches logs, the status page and the heartbeat
// on central (review M6); callers that need the body read e.Body.
func (e *HTTPError) Error() string {
	if code := ErrorCode(e.Body); code != "" {
		return fmt.Sprintf("central returned %d (%s)", e.Status, code)
	}
	return fmt.Sprintf("central returned %d", e.Status)
}

// ErrorCode returns the errorCode of a DHIS2 web message body, or "".
func ErrorCode(body []byte) string {
	var m struct {
		ErrorCode string `json:"errorCode"`
	}
	json.Unmarshal(body, &m)
	if len(m.ErrorCode) > 16 {
		return ""
	}
	return m.ErrorCode
}

// FromCentral reports whether err is an answer of DHIS2 itself: an HTTP error whose
// body is a web message (JSON with httpStatusCode). A proxy's or a captive portal's
// error page is not one.
func FromCentral(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	var m struct {
		Code *int `json:"httpStatusCode"`
	}
	return json.Unmarshal(he.Body, &m) == nil && m.Code != nil
}

// JSON does a request and decodes a 2xx JSON response into out (if non-nil).
func (c *Client) JSON(ctx context.Context, method, pathAndQuery string, in any, auth Auth, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	resp, err := c.Do(ctx, method, pathAndQuery, body, auth, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		c.Failed(ctx, err)
		return fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	if resp.StatusCode/100 != 2 {
		return &HTTPError{Status: resp.StatusCode, Body: b}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// Status returns the HTTP status of an HTTPError, or 0.
func Status(err error) int {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func Q(path string, kv ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	if len(v) == 0 {
		return path
	}
	return path + "?" + v.Encode()
}

// --- connectivity monitor --------------------------------------------------------

// Monitor tracks whether central is reachable (spec 5.2).
type Monitor struct {
	mu       sync.Mutex
	online   bool
	failures int
	// listeners are called synchronously, in the order of the switches (notifyMu), so
	// records such as the hub's history of outages see them in order. forced is true
	// for a switch by Force (the admin page's simulated uplink cut).
	listeners []func(online, forced bool)
	notifyMu  sync.Mutex
	// Client probes central (nil: one that follows no redirects; tests give one that
	// trusts their test certificate).
	Client *http.Client
	// movedTo is where the current https address redirected permanently, to another
	// host, at movedN probes in a row: a candidate new address for central from three
	// (core.TryCentralMove). A probe that reaches central clears it.
	movedTo  string
	movedN   int
	Interval time.Duration
	lastWhy  time.Time // last "still unreachable" log
	Timeout  time.Duration
	// Forced, when set, pins the state (tests and the "cut the uplink" demo switch).
	forced *bool
	// probeNow wakes Run for a probe out of turn: a request to central failed.
	probeNow chan struct{}
}

func NewMonitor(interval, timeout time.Duration) *Monitor {
	return &Monitor{Interval: interval, Timeout: timeout, probeNow: make(chan struct{}, 1)}
}

func (m *Monitor) Online() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.forced != nil {
		return *m.forced
	}
	return m.online
}

// OnChange registers a callback for state switches, called synchronously and in
// order: one that takes time starts its own goroutine.
func (m *Monitor) OnChange(fn func(online, forced bool)) {
	m.mu.Lock()
	m.listeners = append(m.listeners, fn)
	m.mu.Unlock()
}

func (m *Monitor) set(online bool) {
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	changed := m.online != online
	m.online = online
	ls := append([]func(bool, bool){}, m.listeners...)
	forced := m.forced != nil
	m.mu.Unlock()
	if changed && !forced {
		slog.Info("connectivity", "online", online)
		for _, fn := range ls {
			fn(online, false)
		}
	}
}

// ReportFailure asks for a probe now: a request to central failed (network error or
// timeout). The probes decide, by their usual rule, whether central is down: one slow
// request, such as a heavy analytics query, does not take the whole hub offline
// (review R-21).
func (m *Monitor) ReportFailure(err error) {
	slog.Warn("upstream request failed", "err", err)
	select {
	case m.probeNow <- struct{}{}:
	default: // a probe is already asked for
	}
}

// ReportNotCentral switches to offline at once: something other than central answered
// a request (a captive portal or a proxy's error page, FINDINGS 22). Its answers must
// not be taken for central's until a probe reaches central again.
func (m *Monitor) ReportNotCentral(err error) {
	slog.Warn("upstream request answered by something other than central", "err", err)
	m.mu.Lock()
	m.failures = 2
	m.mu.Unlock()
	m.set(false)
}

// Force pins the state; nil releases it.
func (m *Monitor) Force(online *bool) {
	m.mu.Lock()
	was := m.forced
	m.forced = online
	ls := append([]func(bool, bool){}, m.listeners...)
	before := m.online
	if was != nil {
		before = *was
	}
	after := m.online
	if online != nil {
		after = *online
	}
	m.mu.Unlock()
	if before != after {
		slog.Info("connectivity (forced)", "online", after)
		m.notifyMu.Lock()
		defer m.notifyMu.Unlock()
		for _, fn := range ls {
			fn(after, true)
		}
	}
}

// Reachable is what the probes found, whatever the "cut the uplink" switch says.
func (m *Monitor) Reachable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.online
}

func (m *Monitor) ForcedOffline() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.forced != nil && !*m.forced
}

// Probe pings central once and updates the state: offline after 2 failures,
// online after 1 success.
// probeClient does not follow redirects: a captive portal redirects to itself. It also
// opens a new connection for every probe (no keep-alives): a kept-alive connection
// would answer while new ones fail — DNS down, or a firewall refusing new connections
// — and the hub would flap online and offline every probe (D6, seen on a Pi 5 on 30
// October when Firewalla blocked its internet). The cost is one TLS handshake per
// probe: every ping_interval (15 s by default) while offline, every four times that
// (60 s) while online (B1, decided 2026-11-02).
var probeClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport:     probeTransport(),
}

// probeTransport is the default transport without keep-alives: every request opens a
// new connection.
func probeTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	return tr
}

// isCentral tells central's answer to an anonymous ping from a captive portal or a
// proxy error page (REVIEW-2 F9): "pong", 401, or a redirect to central's own login
// page (what 2.42 answers) count; anything else does not.
func isCentral(baseURL string, resp *http.Response) bool {
	switch {
	case resp.StatusCode == http.StatusOK:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		return strings.TrimSpace(string(b)) == "pong"
	case resp.StatusCode == http.StatusUnauthorized:
		return true
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		loc, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
		base, berr := url.Parse(baseURL)
		return err == nil && berr == nil && strings.EqualFold(loc.Host, base.Host)
	}
	return false
}

func (m *Monitor) Probe(ctx context.Context, baseURL string) bool {
	ok := false
	var why string
	if baseURL != "" {
		ctx, cancel := context.WithTimeout(ctx, m.Timeout)
		defer cancel()
		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/system/ping", nil)
		pc := m.Client
		if pc == nil {
			pc = probeClient
		}
		if resp, err := pc.Do(req); err == nil {
			ok = isCentral(baseURL, resp)
			m.noteMove(baseURL, resp, ok)
			resp.Body.Close()
			why = fmt.Sprintf("status %d after %s", resp.StatusCode, time.Since(start).Round(time.Millisecond))
		} else {
			why = fmt.Sprintf("%v after %s", err, time.Since(start).Round(time.Millisecond))
		}
	}
	// While offline, say once a minute why probes fail: a reconnect that took minutes
	// in the load test left nothing in the log to explain it.
	m.mu.Lock()
	if !ok && !m.online && time.Since(m.lastWhy) > time.Minute {
		m.lastWhy = time.Now()
		m.mu.Unlock()
		slog.Info("central still unreachable", "probe", why)
	} else {
		m.mu.Unlock()
	}
	m.mu.Lock()
	if ok {
		m.failures = 0
	} else {
		m.failures++
	}
	f := m.failures
	m.mu.Unlock()
	if ok {
		m.set(true)
	} else if f >= 2 {
		m.set(false)
	}
	return ok
}

// noteMove records a permanent redirect of the ping to another host, from an https
// address only (verified TLS: not a captive portal on plain http); ok clears it.
func (m *Monitor) noteMove(baseURL string, resp *http.Response, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	base, berr := url.Parse(baseURL)
	loc, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
	if ok || berr != nil || base.Scheme != "https" || err != nil || strings.EqualFold(loc.Host, base.Host) ||
		(resp.StatusCode != http.StatusMovedPermanently && resp.StatusCode != http.StatusPermanentRedirect) {
		m.movedTo, m.movedN = "", 0
		return
	}
	next := strings.TrimSuffix(loc.String(), "/api/system/ping")
	if next != m.movedTo {
		m.movedTo, m.movedN = next, 0
	}
	m.movedN++
}

// MovedTo is where the current address redirects permanently, after three probes in a
// row ("" otherwise).
func (m *Monitor) MovedTo() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.movedN < 3 {
		return ""
	}
	return m.movedTo
}

// probeOnlineEvery backs the probe cadence off while online (B1, decided 2026-11-02).
// The load on central comes from successful probes only — a failing probe never
// completes a TLS handshake — so the offline cadence stays Interval and only the
// online one is longer: one TLS handshake a minute at the default 15 s ping_interval
// instead of four.
const probeOnlineEvery = 4

// Run probes central until ctx ends, each after nextDelay().
func (m *Monitor) Run(ctx context.Context, baseURL func() string) {
	m.Probe(ctx, baseURL())
	t := time.NewTimer(m.nextDelay())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.probeNow:
			m.Probe(ctx, baseURL())
			t.Reset(m.nextDelay()) // no stale tick to drain since Go 1.23
		case <-t.C:
			m.Probe(ctx, baseURL())
			t.Reset(m.nextDelay())
		}
	}
}

// nextDelay is the wait after a probe: Interval * probeOnlineEvery while online, else
// Interval. A forced-offline hub (the admin switch) still probes at Interval: cheap,
// and recovery detection stays quick. After a failed probe the second one also comes
// at Interval: a failing probe costs central nothing, and an outage that a failed
// request reported takes the hub offline within one Interval.
func (m *Monitor) nextDelay() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.online && m.failures == 0 && (m.forced == nil || *m.forced) {
		return m.Interval * probeOnlineEvery
	}
	return m.Interval
}
