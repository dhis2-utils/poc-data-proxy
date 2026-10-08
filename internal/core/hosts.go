package core

import (
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// hostCache holds the names and addresses the hub answers to. Interface addresses
// are re-read at most once a minute, so a DHCP change is picked up.
type hostCache struct {
	mu      sync.Mutex
	at      time.Time
	allowed map[string]bool
}

// HostAllowed reports whether a request's Host names this hub: hub.local, the
// device's host name, one of its own addresses, localhost, or a name listed in
// hub_hosts. Everything else is refused, so a DNS-rebinding page or a forged Host
// header cannot make the hub write a foreign origin into its responses (review M1).
func (h *Hub) HostAllowed(hostport string) bool {
	host := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		host = hh
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" {
		return false
	}
	for _, x := range h.Cfg.HubHosts {
		if strings.EqualFold(x, host) {
			return true
		}
	}
	h.hosts.mu.Lock()
	defer h.hosts.mu.Unlock()
	if h.hosts.allowed == nil || time.Since(h.hosts.at) > time.Minute {
		h.hosts.allowed, h.hosts.at = ownHosts(), time.Now()
	}
	return h.hosts.allowed[host]
}

// SetupMode is how setup chooses the central server (REVIEW-2 F1).
const (
	SetupPinned    = "pinned"    // central_url only: the form has no URL field
	SetupAllowlist = "allowlist" // central_hosts: any https URL on those hosts
	SetupToken     = "token"     // neither: any https URL, with the setup token from the label
)

func (h *Hub) SetupMode() string {
	switch {
	case len(h.Cfg.CentralHosts) > 0:
		return SetupAllowlist
	case h.Cfg.CentralURL != "":
		return SetupPinned
	}
	return SetupToken
}

// CentralAllowed reports whether setup may point the hub at this central URL: the
// image's central_url, or an https URL on a host in central_hosts (or, in token mode,
// any https URL).
func (h *Hub) CentralAllowed(raw string) bool {
	raw = strings.TrimRight(raw, "/")
	if raw != "" && raw == h.Cfg.CentralURL {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return false
	}
	switch h.SetupMode() {
	case SetupPinned:
		return false
	case SetupToken:
		return true
	}
	host := strings.ToLower(u.Hostname())
	for _, x := range h.Cfg.CentralHosts {
		x = strings.ToLower(x)
		if host == x || (strings.HasPrefix(x, "*.") && strings.HasSuffix(host, x[1:])) {
			return true
		}
	}
	return false
}

func ownHosts() map[string]bool {
	m := map[string]bool{"localhost": true, "hub.local": true}
	if name, err := os.Hostname(); err == nil && name != "" {
		name = strings.ToLower(name)
		m[name], m[name+".local"] = true, true
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				m[ipn.IP.String()] = true
			}
		}
	}
	return m
}
