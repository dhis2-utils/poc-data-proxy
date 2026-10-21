// Package config reads the image defaults file (spec 6.1). The file is the same for
// every hub in a country; nothing facility-specific lives here.
package config

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	CentralURL string // optional; asked at setup when empty
	// CentralHosts are the hosts setup may point a hub at ("hmis.example.org",
	// "*.hmis.example.org"). Empty: only CentralURL, or, without that, any https URL
	// with the setup token from the label (REVIEW-2 F1).
	CentralHosts []string
	Listen       string // e.g. ":8443"
	// HTTPListen serves the plain-HTTP start page and the hub's authority (":80" on the
	// image, so http://dhis2.local works); empty: off.
	HTTPListen string
	// Network "networkmanager": the hub manages its network through NetworkManager
	// (the Pi image; internal/netsetup). Empty: the machine's network as it is.
	Network string
	// NMDnsmasqDir is NetworkManager's dnsmasq-shared.d, for the setup Wi-Fi's DNS.
	NMDnsmasqDir string
	// MDNS: the hub answers dhis2.local itself (Windows; the Pi has avahi).
	MDNS bool
	// SetupLocalOnly: set-up only from the machine itself (Windows: being at the PC
	// proves presence at the facility, so no setup code is needed).
	SetupLocalOnly   bool
	DataDir          string        // e.g. /var/lib/hub
	SyncInterval     time.Duration // outbox push + pull cadence
	PingInterval     time.Duration // connectivity probe cadence
	PingTimeout      time.Duration
	PATExpiryDays    int
	TLS              bool // self-signed TLS (spec 12); false for local development
	ReservedPoolSize int  // target number of reserved values per generated attribute
	FullPullInterval time.Duration
	// OwnershipCheckInterval: how often the hub checks that the facility still owns the
	// enrollments it holds (transfers out do not show in incremental pulls).
	OwnershipCheckInterval time.Duration
	// AccessRefreshInterval: how often a sync cycle re-reads every user's access and
	// the program catalog from central (logins refresh the user's own at once).
	// Re-reading them in every cycle was a third of what a hub asked of central
	// (docs/PERFORMANCE.md, "Load on central").
	AccessRefreshInterval time.Duration
	// HeartbeatInterval: how often the heartbeat goes to central (facility-hub-status),
	// at most once per sync cycle, and at the first cycle after a start. Hourly: with
	// its 90-day history it is about 14 KB (docs/reviews/KISS-2026-10-20.md). 0: every
	// cycle (tests).
	HeartbeatInterval time.Duration
	// ReconnectDelay: after an outage, wait a random time up to this before the first
	// sync, so hubs do not all reach central in the same second when it returns.
	ReconnectDelay time.Duration
	LogRequests    bool // log every request with method, path and query (spec 13.1)
	// OfflineSearchFacility answers orgUnitMode=ACCESSIBLE/CAPTURE searches offline from
	// the facility's data (spec 10.1 says refuse; Capture needs it, see FINDINGS.md).
	OfflineSearchFacility bool
	// HubHosts are extra host names or addresses clients may use to reach the hub
	// (comma separated), e.g. a DNS name on the facility router.
	HubHosts []string
	// SessionIdle and SessionMax end hub sessions after inactivity and after login.
	SessionIdle time.Duration
	SessionMax  time.Duration
	// PasswordFallback stores a user's central password (sealed) when the hub cannot
	// create a PAT for them (spec 7.1). Off by default: it was never needed on 2.42,
	// and the password is the most valuable secret the hub could hold (REVIEW-2 F8).
	PasswordFallback bool
	// TPM: seal the hub's device key with the machine's TPM 2.0 where it has one
	// (internal/devkey). Off keeps a new hub off a TPM that misbehaves; a hub that has a
	// device key already needs its TPM, whatever this says.
	TPM bool
	// KeySlotMaxAge removes the key slot of a user who has not logged in for this long.
	KeySlotMaxAge time.Duration
	// USBDir is where the box's USB stick is mounted ("" for none): the setup code
	// before set-up, then what a restore needs (docs/RECOVERY.md, C2).
	// USBRequireMount uses it only when it is a mount point, so a missing stick never
	// fills the hub's own disk.
	USBDir          string
	USBRequireMount bool
	// CentralMoveAfter is how long central's address must have stopped answering
	// before the hub moves to an announced or redirected new one (docs/DEPLOYMENT.md,
	// section 6): a brief outage during a planned move must not move it early.
	CentralMoveAfter time.Duration
	// UplinkSwitch shows "Simulate uplink cut" on the admin page, for demonstrations.
	// Off in the field: a hub that looks offline lets staff type in a new central.
	UplinkSwitch bool
}

func Default() Config {
	return Config{
		Listen:                 ":8443",
		NMDnsmasqDir:           "/etc/NetworkManager/dnsmasq-shared.d",
		DataDir:                "/var/lib/hub",
		SyncInterval:           15 * time.Minute,
		PingInterval:           15 * time.Second,
		PingTimeout:            5 * time.Second,
		PATExpiryDays:          90,
		HeartbeatInterval:      time.Hour,
		TPM:                    true,
		TLS:                    true,
		ReservedPoolSize:       150,
		FullPullInterval:       7 * 24 * time.Hour,
		USBRequireMount:        true,
		CentralMoveAfter:       time.Hour,
		OwnershipCheckInterval: time.Hour,
		AccessRefreshInterval:  time.Hour,
		ReconnectDelay:         5 * time.Minute,
		OfflineSearchFacility:  true,
		SessionIdle:            time.Hour,
		SessionMax:             12 * time.Hour,
		KeySlotMaxAge:          90 * 24 * time.Hour,
	}
}

// Load parses a flat "key: value" YAML file. The image defaults file has no nesting,
// so a full YAML parser is not worth the dependency.
func Load(path string) (Config, error) {
	c := Default()
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		s := stripComment(sc.Text())
		if strings.TrimSpace(s) == "" {
			continue
		}
		k, v, ok := strings.Cut(s, ":")
		if !ok {
			return c, fmt.Errorf("%s:%d: expected key: value", path, line)
		}
		k = strings.TrimSpace(k)
		v = unquote(strings.TrimSpace(v))
		if err := c.set(k, v); err != nil {
			return c, fmt.Errorf("%s:%d: %w", path, line, err)
		}
	}
	return c, sc.Err()
}

func (c *Config) set(k, v string) error {
	var err error
	switch k {
	case "central_url":
		c.CentralURL = strings.TrimRight(v, "/")
	case "central_hosts":
		c.CentralHosts = splitList(v)
	case "listen":
		c.Listen = v
	case "http_listen":
		c.HTTPListen = v
	case "network":
		if v != "" && v != "networkmanager" {
			return fmt.Errorf("network: %q (only networkmanager)", v)
		}
		c.Network = v
	case "mdns":
		c.MDNS, err = strconv.ParseBool(v)
	case "setup_local_only":
		c.SetupLocalOnly, err = strconv.ParseBool(v)
	case "data_dir":
		c.DataDir = v
	case "sync_interval":
		c.SyncInterval, err = time.ParseDuration(v)
	case "ping_interval":
		c.PingInterval, err = time.ParseDuration(v)
	case "ping_timeout":
		c.PingTimeout, err = time.ParseDuration(v)
	case "usb_dir":
		c.USBDir = v
	case "full_pull_interval":
		c.FullPullInterval, err = time.ParseDuration(v)
	case "ownership_check_interval":
		c.OwnershipCheckInterval, err = time.ParseDuration(v)
	case "access_refresh_interval":
		c.AccessRefreshInterval, err = time.ParseDuration(v)
	case "reconnect_delay":
		c.ReconnectDelay, err = time.ParseDuration(v)
	case "pat_expiry_days":
		c.PATExpiryDays, err = strconv.Atoi(v)
	case "reserved_pool_size":
		c.ReservedPoolSize, err = strconv.Atoi(v)
	case "tls":
		c.TLS, err = strconv.ParseBool(v)
	case "offline_search_facility":
		c.OfflineSearchFacility, err = strconv.ParseBool(v)
	case "session_idle":
		c.SessionIdle, err = time.ParseDuration(v)
	case "session_max":
		c.SessionMax, err = time.ParseDuration(v)
	case "tpm":
		c.TPM, err = strconv.ParseBool(v)
	case "password_fallback":
		c.PasswordFallback, err = strconv.ParseBool(v)
	case "key_slot_max_age":
		c.KeySlotMaxAge, err = time.ParseDuration(v)
	case "hub_hosts":
		c.HubHosts = splitList(v)
		if slices.Contains(c.HubHosts, "*") {
			// It would switch off the Host check on every hub with this image (REVIEW-2 F7).
			return fmt.Errorf("hub_hosts: list names; \"*\" is not allowed")
		}
	case "log_requests":
		c.LogRequests, err = strconv.ParseBool(v)
	case "uplink_switch":
		c.UplinkSwitch, err = strconv.ParseBool(v)
	default:
		return fmt.Errorf("unknown key %q", k)
	}
	return err
}

func splitList(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// stripComment removes a trailing "# ..." comment that is not inside quotes.
func stripComment(s string) string {
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case inQuote != 0:
			if ch == inQuote {
				inQuote = 0
			}
		case ch == '"' || ch == '\'':
			inQuote = ch
		case ch == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		}
	}
	return s
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
