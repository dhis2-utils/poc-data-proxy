package core

import (
	"net"
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
		if x == "*" || strings.EqualFold(x, host) {
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
