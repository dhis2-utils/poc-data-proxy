package e2e

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The admin page tells whether devices reach the hub by dhis2.local or only by its IP
// (DEPLOYMENT.md, "Reaching the hub securely": where .local fails): requests from other devices on the LAN count,
// the hub's own and loopback ones do not.
func TestReach(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	h.NoteReach("dhis2.local", "127.0.0.1:5555")
	if n, ip := h.Reach(); !n.IsZero() || !ip.IsZero() {
		t.Fatal("loopback counted")
	}
	h.NoteReach("192.168.1.20", "192.168.1.30:5555")
	if n, ip := h.Reach(); !n.IsZero() || ip.IsZero() {
		t.Fatalf("by IP: %v %v", n, ip)
	}
	h.NoteReach("dhis2.local:443", "192.168.1.30:5555")
	if n, _ := h.Reach(); n.IsZero() {
		t.Fatal("by name not counted")
	}
	a := e.web("hub_admin")
	a.login()
	if _, page := a.do("GET", "/hub/ui", nil); !strings.Contains(string(page), "Devices reach this hub by dhis2.local") {
		t.Fatal("admin page does not say devices reach the hub by name")
	}
}

// Sites that force a web proxy: the admin page's proxy is used for central.
func TestProxyUsedForCentral(t *testing.T) {
	e := newEnv(t)
	e.setup()
	var n atomic.Int32
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		e.central.ServeHTTP(w, r) // a forwarding proxy to the fake central
	}))
	t.Cleanup(px.Close)
	t.Cleanup(func() { e.app.Hub.SetProxy("") })
	if err := e.app.Hub.SetProxy(px.URL); err != nil {
		t.Fatal(err)
	}
	e.web("nurse_a").login()
	e.sync()
	if n.Load() == 0 {
		t.Fatal("nothing went through the proxy")
	}
}

func TestProxyRefusesJunk(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for _, bad := range []string{"ftp://x:21", "proxy.example.org:3128", "http://", "http://a b:1"} {
		if err := e.app.Hub.SetProxy(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := e.app.Hub.SetProxy("http://proxy.example.org:3128"); err != nil {
		t.Error(err)
	}
	e.app.Hub.SetProxy("")
}

// A browser on the PC that opens the hub at an IPv6 link-local address sends it with a
// zone ("fe80::1%eth0"): it is still the PC itself, and another device's is a device.
func TestLinkLocalWithZone(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	if !h.FromThisMachine("[::1%lo]:5555") {
		t.Error("loopback with a zone is not this machine")
	}
	if as, err := net.InterfaceAddrs(); err == nil {
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() == nil && ipn.IP.IsLinkLocalUnicast() {
				if ra := "[" + ipn.IP.String() + "%2]:5555"; !h.FromThisMachine(ra) {
					t.Errorf("%s is not this machine", ra)
				}
			}
		}
	}
	if h.FromThisMachine("[fe80::dead:beef%2]:5555") {
		t.Error("another device's link-local address is this machine")
	}
	h.NoteReach("[fe80::1]", "[fe80::dead:beef%2]:5555")
	if _, ip := h.Reach(); ip.IsZero() {
		t.Error("a device at a link-local address not counted")
	}
}
