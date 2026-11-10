package core

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/hubtls"
)

// MetaProxy is the web proxy for outbound connections set on the admin page.
const MetaProxy = "proxy"

const metaReach = "reach"

// reach is when another device on the LAN last opened the hub by name and by IP
// address (DEPLOYMENT.md, "Reaching the hub securely": where .local fails): the admin page's set-up check.
type reach struct {
	mu     sync.Mutex
	loaded bool
	ByName time.Time `json:"byName"`
	ByIP   time.Time `json:"byIp"`
}

// NoteReach records a request for host from remoteAddr. Loopback and the hub's own
// addresses are not other devices; a name counts as by name, an address as by IP.
// Saved when a way is first seen.
func (h *Hub) NoteReach(host, remoteAddr string) {
	rip := remoteIP(remoteAddr)
	if rip == nil || rip.IsLoopback() || h.ownAddress(rip.String()) {
		return
	}
	if hh, _, err := net.SplitHostPort(host); err == nil {
		host = hh
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return
	}
	r := &h.reach
	r.mu.Lock()
	defer r.mu.Unlock()
	h.loadReach()
	now := time.Now()
	seen := &r.ByName
	if net.ParseIP(host) != nil {
		seen = &r.ByIP
	}
	first := seen.IsZero()
	*seen = now
	if first {
		// Saved only the first time: it is in the key file, which the USB stick
		// copies whenever it changes (docs/RECOVERY.md, C2). The later times are
		// kept in memory.
		b, _ := json.Marshal(r)
		h.Store.SetMeta(metaReach, string(b))
	}
}

// Reach is when another device last opened the hub by name and by IP address.
func (h *Hub) Reach() (byName, byIP time.Time) {
	h.reach.mu.Lock()
	defer h.reach.mu.Unlock()
	h.loadReach()
	return h.reach.ByName, h.reach.ByIP
}

func (h *Hub) loadReach() {
	if !h.reach.loaded {
		json.Unmarshal([]byte(h.Store.Meta(metaReach)), &h.reach)
		h.reach.loaded = true
	}
}

func (h *Hub) ownAddress(ip string) bool {
	h.hosts.mu.Lock()
	defer h.hosts.mu.Unlock()
	if h.hosts.allowed == nil || time.Since(h.hosts.at) > time.Minute {
		h.hosts.allowed, h.hosts.at = ownHosts(), time.Now()
	}
	return h.hosts.allowed[ip]
}

// Proxy is the web proxy set on the admin page ("" uses HTTPS_PROXY).
func (h *Hub) Proxy() string { return h.Store.Meta(MetaProxy) }

// SetProxy sets the web proxy for every outbound connection: http(s)://host:port, or
// "" for the environment's.
func (h *Hub) SetProxy(p string) error {
	p = strings.TrimSpace(p)
	if p != "" {
		u, err := url.Parse(p)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Port() == "" ||
			(u.Path != "" && u.Path != "/") || u.User != nil {
			return errors.New("a proxy looks like http://proxy.example.org:3128")
		}
		p = u.Scheme + "://" + u.Host
	}
	if err := h.Store.SetMeta(MetaProxy, p); err != nil {
		return err
	}
	central.SetProxy(p)
	return nil
}

// FromThisMachine reports whether a request comes from the machine the hub runs on:
// loopback or one of its own addresses.
func (h *Hub) FromThisMachine(remoteAddr string) bool {
	rip := remoteIP(remoteAddr)
	return rip != nil && (rip.IsLoopback() || h.ownAddress(rip.String()))
}

// remoteIP is a request's address without its port, and without the zone an IPv6
// link-local address comes with ("fe80::1%eth0"), which the hub's own addresses lack.
func remoteIP(remoteAddr string) net.IP {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		ip = remoteAddr
	}
	ip, _, _ = strings.Cut(ip, "%")
	return net.ParseIP(ip)
}

// DeviceAddresses are the addresses other devices open the hub at, for the hub's page
// on the machine itself: dhis2.local, then each private IPv4 address, with the HTTPS
// port when it is not 443.
func (h *Hub) DeviceAddresses() []string {
	_, port, _ := net.SplitHostPort(h.Cfg.Listen)
	withPort := func(host string) string {
		if port == "" || port == "443" {
			return "https://" + host
		}
		return "https://" + net.JoinHostPort(host, port)
	}
	out := []string{withPort("dhis2.local")}
	for _, a := range hubtls.Addresses() {
		out = append(out, withPort(a))
	}
	return out
}
