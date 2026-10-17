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

func (a Auth) Empty() bool { return a.PAT == "" && a.Username == "" && a.Cookie == "" }

// Client talks to central and reports network failures to the monitor.
type Client struct {
	mu      sync.RWMutex
	baseURL string
	HTTP    *http.Client
	Monitor *Monitor
}

func NewClient(baseURL string, m *Monitor) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
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

func (c *Client) SetBaseURL(u string) {
	c.mu.Lock()
	c.baseURL = strings.TrimRight(u, "/")
	c.mu.Unlock()
}

// ErrNetwork wraps transport failures (no HTTP response).
var ErrNetwork = errors.New("central unreachable")

// IsNetwork reports whether err means central could not be reached.
func IsNetwork(err error) bool { return errors.Is(err, ErrNetwork) }

// Do sends a request. pathAndQuery starts with "/". A network error marks the hub
// offline immediately (spec 5.2) and is returned wrapped in ErrNetwork.
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
		if ctx.Err() == nil || isTimeout(err) {
			c.Monitor.ReportFailure(err)
		}
		return nil, fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	return resp, nil
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
		c.Monitor.ReportFailure(err)
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
	mu        sync.Mutex
	online    bool
	failures  int
	listeners []func(online bool)
	// inOrder are called synchronously, in the order of the switches (notifyMu): for
	// records that need them in order, such as the hub's history of outages.
	inOrder  []func(online bool)
	notifyMu sync.Mutex
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
}

func NewMonitor(interval, timeout time.Duration) *Monitor {
	return &Monitor{Interval: interval, Timeout: timeout}
}

func (m *Monitor) Online() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.forced != nil {
		return *m.forced
	}
	return m.online
}

// OnChange registers a callback for state switches.
func (m *Monitor) OnChange(fn func(online bool)) {
	m.mu.Lock()
	m.listeners = append(m.listeners, fn)
	m.mu.Unlock()
}

// OnChangeInOrder registers a callback called synchronously, in order, on each switch.
func (m *Monitor) OnChangeInOrder(fn func(online bool)) {
	m.mu.Lock()
	m.inOrder = append(m.inOrder, fn)
	m.mu.Unlock()
}

func (m *Monitor) set(online bool) {
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	changed := m.online != online
	m.online = online
	ls := append([]func(bool){}, m.listeners...)
	ordered := append([]func(bool){}, m.inOrder...)
	forced := m.forced != nil
	m.mu.Unlock()
	if changed && !forced {
		slog.Info("connectivity", "online", online)
		for _, fn := range ordered {
			fn(online)
		}
		for _, fn := range ls {
			go fn(online)
		}
	}
}

// ReportFailure switches to offline immediately (any failed upstream request).
func (m *Monitor) ReportFailure(err error) {
	slog.Warn("upstream request failed", "err", err)
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
	ls := append([]func(bool){}, m.listeners...)
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
		for _, fn := range ls {
			go fn(after)
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
// probeClient does not follow redirects: a captive portal redirects to itself.
var probeClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

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

// Run probes every Interval until ctx ends.
func (m *Monitor) Run(ctx context.Context, baseURL func() string) {
	m.Probe(ctx, baseURL())
	t := time.NewTicker(m.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Probe(ctx, baseURL())
		}
	}
}
