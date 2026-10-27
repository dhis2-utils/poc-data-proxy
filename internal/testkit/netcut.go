package testkit

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
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

	mu     sync.Mutex
	calls  []Call   // what the hub asked central, since the last Calls
	faults []*fault // one-shot faults on matching requests
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
		f := n.take(r)
		record := func(status int, bytes int64) {
			c := Call{Method: r.Method, URL: &asked, Status: status, Bytes: bytes}
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
