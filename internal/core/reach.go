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
)

// MetaProxy is the web proxy for outbound connections set on the admin page.
const MetaProxy = "proxy"

const metaReach = "reach"

// reach is when another device on the LAN last opened the hub by name and by IP
// address (DEPLOYMENT.md, "Where .local fails"): the admin page's set-up check.
type reach struct {
	mu      sync.Mutex
	loaded  bool
	ByName  time.Time `json:"byName"`
	ByIP    time.Time `json:"byIp"`
	savedAt time.Time
}

// NoteReach records a request for host from remoteAddr. Loopback and the hub's own
// addresses are not other devices; a name counts as by name, an address as by IP.
// Saved at most once a minute.
func (h *Hub) NoteReach(host, remoteAddr string) {
	ip, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		ip = remoteAddr
	}
	rip := net.ParseIP(ip)
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
	if net.ParseIP(host) != nil {
		r.ByIP = now
	} else {
		r.ByName = now
	}
	if now.Sub(r.savedAt) > time.Minute {
		b, _ := json.Marshal(r)
		h.Store.SetMeta(metaReach, string(b))
		r.savedAt = now
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
