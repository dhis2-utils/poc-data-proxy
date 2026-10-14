package testkit

import (
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
	srv   *http.Server
	ln    net.Listener
	down  atomic.Bool
	delay atomic.Int64 // nanoseconds added to every request

	mu    sync.Mutex
	calls []Call // what the hub asked central, since the last Calls
}

// Call is one request the hub made to central, with the size of the answer.
type Call struct {
	Method string
	URL    *url.URL
	Status int
	Bytes  int64
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
			cut(w)
			return
		}
		if d := n.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		m := &meter{ResponseWriter: w, status: 200}
		asked := *r.URL // the proxy rewrites r.URL
		rp.ServeHTTP(m, r)
		n.mu.Lock()
		n.calls = append(n.calls, Call{Method: r.Method, URL: &asked, Status: m.status, Bytes: m.bytes})
		n.mu.Unlock()
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
