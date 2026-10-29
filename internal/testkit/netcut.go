package testkit

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Netcut is the line between hub and central: a reverse proxy that can be cut at the
// connection level, so "offline" is a real network failure on any central, fake or
// live. It keeps the Host header the hub sent, so central builds absolute URLs (pager
// links, redirects) on the proxy's address, which is the hub's central URL.
type Netcut struct {
	srv     *http.Server
	ln      net.Listener
	down    atomic.Bool
	delay   atomic.Int64                  // nanoseconds added to every request
	rate    atomic.Int64                  // bytes per second each way; 0: unlimited
	held    atomic.Pointer[chan struct{}] // half open while set: closed to release
	refused atomic.Int64                  // requests cut while down
	version atomic.Pointer[string]        // central's version as /api/system/info answers it (SetVersion)
	portal  atomic.Pointer[string]        // a captive portal answers instead of central (SetPortal)

	mu       sync.Mutex
	calls    []Call     // what the hub asked central, since the last Calls
	faults   []*fault   // one-shot faults on matching requests
	rewrites []*rewrite // answers changed until removed (Answer, Rewrite)
}

// rewrite changes central's answer to matching requests: with forward false, central
// never sees the request.
type rewrite struct {
	match   func(*http.Request) bool
	forward bool
	change  func(status int, body []byte) (int, []byte)
}

// fault is applied to the next n requests that match.
type fault struct {
	kind  string // "lose", "cutRequest", "cutAnswer"
	match func(*http.Request) bool
	n     int
	hit   chan struct{} // receives once per request the fault hit
}

// Call is one request the hub made to central, with the size of the answer.
type Call struct {
	Method string
	URL    *url.URL
	Status int
	Bytes  int64
	Fault  string // the fault applied, if any
	// At is when the request passed the link's latency; Dur how long central took to
	// answer it from then, transfers included; Sent the request body's size.
	At   time.Time
	Dur  time.Duration
	Sent int64
}

// Calls returns the requests the hub made to central so far, and forgets them.
func (n *Netcut) Calls() []Call {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := n.calls
	n.calls = nil
	return c
}

// meter counts what passes back to the hub.
type meter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (m *meter) WriteHeader(s int) { m.status = s; m.ResponseWriter.WriteHeader(s) }
func (m *meter) Write(b []byte) (int, error) {
	n, err := m.ResponseWriter.Write(b)
	m.bytes += int64(n)
	return n, err
}
func (m *meter) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func NewNetcut(target string) (*Netcut, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	n := &Netcut{ln: ln}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.FlushInterval = -1
	n.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.down.Load() {
			n.refused.Add(1)
			cut(w)
			return
		}
		if p := n.portal.Load(); p != nil {
			n.mu.Lock()
			n.calls = append(n.calls, Call{Method: r.Method, URL: r.URL, Status: portalStatus(*p), Fault: "portal", At: time.Now()})
			n.mu.Unlock()
			io.Copy(io.Discard, r.Body)
			servePortal(w, *p)
			return
		}
		if h := n.held.Load(); h != nil {
			// Half open: no answer and no reset, until the hub gives up or the hold ends.
			select {
			case <-*h:
			case <-r.Context().Done():
			}
			cut(w)
			return
		}
		if d := n.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		asked := *r.URL // the proxy rewrites r.URL
		if rw := n.rewriteFor(r); rw != nil {
			at := time.Now()
			status, body := 0, []byte(nil)
			if rw.forward {
				r.Header.Del("Accept-Encoding") // a plain answer, to rewrite
				b := &buffered{header: http.Header{}, status: 200}
				rp.ServeHTTP(b, r)
				for k, vs := range b.header {
					w.Header()[k] = vs
				}
				status, body = b.status, b.body.Bytes()
			} else {
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
			}
			status, body = rw.change(status, body)
			n.mu.Lock()
			n.calls = append(n.calls, Call{Method: r.Method, URL: &asked, Status: status, Bytes: int64(len(body)), At: at,
				Dur: time.Since(at), Sent: r.ContentLength, Fault: "rewrite"})
			n.mu.Unlock()
			w.Header().Del("Content-Length")
			w.WriteHeader(status)
			w.Write(body)
			return
		}
		f := n.take(r)
		at := time.Now()
		record := func(status int, bytes int64) {
			c := Call{Method: r.Method, URL: &asked, Status: status, Bytes: bytes, At: at, Dur: time.Since(at), Sent: r.ContentLength}
			if f != nil {
				c.Fault = f.kind
			}
			n.mu.Lock()
			n.calls = append(n.calls, c)
			n.mu.Unlock()
		}
		if bps := n.rate.Load(); bps > 0 && r.Body != nil {
			r.Body = io.NopCloser(&slowReader{r: r.Body, bps: bps})
		}
		if v := n.version.Load(); v != nil && f == nil && strings.HasSuffix(r.URL.Path, "/api/system/info") {
			r.Header.Del("Accept-Encoding") // a plain answer, to rewrite
			b := &buffered{header: http.Header{}, status: 200}
			rp.ServeHTTP(b, r)
			record(b.status, int64(b.body.Len()))
			for k, vs := range b.header {
				w.Header()[k] = vs
			}
			w.Header().Del("Content-Length")
			w.WriteHeader(b.status)
			w.Write(versionRe.ReplaceAll(b.body.Bytes(), []byte(`"version":"`+*v+`"`)))
			return
		}
		if f == nil {
			m := &meter{ResponseWriter: w, status: 200}
			if bps := n.rate.Load(); bps > 0 {
				m.ResponseWriter = &slowWriter{ResponseWriter: w, bps: bps}
			}
			rp.ServeHTTP(m, r)
			record(m.status, m.bytes)
			return
		}
		defer func() {
			if f.kind != "lose" {
				f.hit <- struct{}{}
			}
		}()
		if f.kind == "cutRequest" {
			// Part of the request leaves; central never sees it whole.
			io.CopyN(io.Discard, r.Body, r.ContentLength/2)
			record(0, 0)
			cut(w)
			return
		}
		// Central gets the whole request and answers; the hub gets none or half of it.
		b := &buffered{header: http.Header{}, status: 200}
		rp.ServeHTTP(b, r)
		record(b.status, 0)
		if f.kind == "lose" {
			// Tell first, and keep the hub waiting a moment: a test can kill it while
			// central has stored what it is still waiting to hear about.
			f.hit <- struct{}{}
			time.Sleep(time.Second)
			cut(w)
			return
		}
		for k, v := range b.header {
			w.Header()[k] = v
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(b.status)
		w.Write(b.body.Bytes()[:b.body.Len()/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})}
	go n.srv.Serve(ln)
	return n, nil
}

// cut drops the connection without an answer, as a dead uplink would.
func cut(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
			return
		}
	}
	panic(http.ErrAbortHandler)
}

func (n *Netcut) URL() string              { return "http://" + n.ln.Addr().String() }
func (n *Netcut) SetDown(down bool)        { n.down.Store(down) }
func (n *Netcut) Down() bool               { return n.down.Load() }
func (n *Netcut) SetDelay(d time.Duration) { n.delay.Store(int64(d)) }
func (n *Netcut) Close() error             { return n.srv.Close() }

// Refused counts the requests the hub made while the uplink was down.
func (n *Netcut) Refused() int64 { return n.refused.Load() }

// SetVersion makes central's /api/system/info say it runs version v, as after an
// upgrade of central the hub's release may not support; "" passes central's own.
func (n *Netcut) SetVersion(v string) {
	if v == "" {
		n.version.Store(nil)
		return
	}
	n.version.Store(&v)
}

// Captive portals, as SetPortal names them.
const (
	PortalPage     = "page"     // 200 with the portal's login page for every request
	PortalRedirect = "redirect" // 302 to the portal's login page on another host
)

// SetPortal puts a captive portal in front of the internet: every request is answered
// by it ("" removes it). Nothing reaches central.
func (n *Netcut) SetPortal(kind string) {
	if kind == "" {
		n.portal.Store(nil)
		return
	}
	n.portal.Store(&kind)
}

func portalStatus(kind string) int {
	if kind == PortalRedirect {
		return http.StatusFound
	}
	return http.StatusOK
}

const portalHTML = `<!DOCTYPE html><html><head><title>Hotspot login</title></head><body>` +
	`<h1>Welcome to the hotspot</h1><form method="post" action="/login"><input name="voucher"><button>Connect</button></form></body></html>`

func servePortal(w http.ResponseWriter, kind string) {
	if kind == PortalRedirect {
		w.Header().Set("Location", "http://hotspot.portal.example/login?dst=central")
		w.WriteHeader(http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, portalHTML)
}

var versionRe = regexp.MustCompile(`"version"\s*:\s*"[^"]*"`)

// SetRate limits the link to bps bytes per second each way (0: unlimited), as a slow
// mobile uplink; SetDelay adds its latency.
func (n *Netcut) SetRate(bps int) { n.rate.Store(int64(bps)) }

// HalfOpen holds every new request without an answer or a reset, as a link whose
// packets stop: the hub's own timeouts must free it. Requests already being answered
// go on. Calling the returned function ends the hold; held requests are then cut.
func (n *Netcut) HalfOpen() (release func()) {
	ch := make(chan struct{})
	n.held.Store(&ch)
	var once sync.Once
	return func() {
		once.Do(func() {
			n.held.Store(nil)
			close(ch)
		})
	}
}

// LoseAnswer lets the next n requests that match reach central, then drops the
// connection to the hub instead of passing on central's answer: central acted, the
// hub cannot know. The channel receives once per lost answer, a second before the
// connection drops.
func (n *Netcut) LoseAnswer(count int, match func(*http.Request) bool) <-chan struct{} {
	return n.add("lose", count, match)
}

// CutAnswer is LoseAnswer with half of central's answer passed on before the cut.
func (n *Netcut) CutAnswer(count int, match func(*http.Request) bool) <-chan struct{} {
	return n.add("cutAnswer", count, match)
}

// CutRequest drops the connection while the next n matching requests are half sent:
// central never sees them.
func (n *Netcut) CutRequest(count int, match func(*http.Request) bool) <-chan struct{} {
	return n.add("cutRequest", count, match)
}

func (n *Netcut) add(kind string, count int, match func(*http.Request) bool) <-chan struct{} {
	f := &fault{kind: kind, match: match, n: count, hit: make(chan struct{}, count)}
	n.mu.Lock()
	n.faults = append(n.faults, f)
	n.mu.Unlock()
	return f.hit
}

// Answer answers every request that matches with status and body itself, without
// passing it to central, until the returned function is called: central failing some
// requests (a 500 from a deadlock), or an endpoint gone in a new release.
func (n *Netcut) Answer(match func(*http.Request) bool, status int, body string) (stop func()) {
	return n.addRewrite(&rewrite{match: match, change: func(int, []byte) (int, []byte) { return status, []byte(body) }})
}

// Rewrite passes matching requests to central and changes its answer with change,
// until the returned function is called: central's answers as a new release might
// shape them.
func (n *Netcut) Rewrite(match func(*http.Request) bool, change func(status int, body []byte) (int, []byte)) (stop func()) {
	return n.addRewrite(&rewrite{match: match, forward: true, change: change})
}

func (n *Netcut) addRewrite(rw *rewrite) func() {
	n.mu.Lock()
	n.rewrites = append(n.rewrites, rw)
	n.mu.Unlock()
	return func() {
		n.mu.Lock()
		defer n.mu.Unlock()
		for i, x := range n.rewrites {
			if x == rw {
				n.rewrites = append(n.rewrites[:i], n.rewrites[i+1:]...)
				return
			}
		}
	}
}

func (n *Netcut) rewriteFor(r *http.Request) *rewrite {
	n.mu.Lock()
	rws := append([]*rewrite(nil), n.rewrites...)
	n.mu.Unlock()
	for _, rw := range rws {
		if rw.match(r) {
			return rw
		}
	}
	return nil
}

// BodyHas matches a request whose body contains s; the body is kept for central.
func BodyHas(s string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		if r.Body == nil {
			return false
		}
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		return bytes.Contains(b, []byte(s))
	}
}

// All matches a request every one of the matchers matches.
func All(ms ...func(*http.Request) bool) func(*http.Request) bool {
	return func(r *http.Request) bool {
		for _, m := range ms {
			if !m(r) {
				return false
			}
		}
		return true
	}
}

// take returns the fault for this request, if any, and counts it.
func (n *Netcut) take(r *http.Request) *fault {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, f := range n.faults {
		if f.match(r) {
			if f.n--; f.n <= 0 {
				n.faults = append(n.faults[:i], n.faults[i+1:]...)
			}
			return f
		}
	}
	return nil
}

// Flap cuts the uplink for down, then restores it for up, over and over until the
// returned function is called (which leaves the uplink up).
func (n *Netcut) Flap(up, down time.Duration) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			n.SetDown(true)
			select {
			case <-done:
				n.SetDown(false)
				return
			case <-time.After(down):
			}
			n.SetDown(false)
			select {
			case <-done:
				return
			case <-time.After(up):
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); <-finished }) }
}

// IsPush matches the hub's pushes of its outbox (the clients' own imports pass
// through with their own flags).
func IsPush(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/tracker" && r.URL.Query().Get("atomicMode") == "OBJECT"
}

// IsImport matches every tracker import.
func IsImport(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/api/tracker"
}

// buffered holds central's whole answer.
type buffered struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *buffered) Header() http.Header         { return b.header }
func (b *buffered) WriteHeader(s int)           { b.status = s }
func (b *buffered) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *buffered) Flush()                      {}

// slowReader and slowWriter pass bps bytes per second, in tenths of a second.
type slowReader struct {
	r   io.Reader
	bps int64
}

func (s *slowReader) Read(p []byte) (int, error) {
	if max := s.bps/10 + 1; int64(len(p)) > max {
		p = p[:max]
	}
	n, err := s.r.Read(p)
	time.Sleep(time.Duration(int64(n) * int64(time.Second) / s.bps))
	return n, err
}

type slowWriter struct {
	http.ResponseWriter
	bps int64
}

func (s *slowWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		k := min(int64(len(p)), s.bps/10+1)
		n, err := s.ResponseWriter.Write(p[:k])
		written += n
		if err != nil {
			return written, err
		}
		if f, ok := s.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(time.Duration(int64(n) * int64(time.Second) / s.bps))
		p = p[k:]
	}
	return written, nil
}

func (s *slowWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
