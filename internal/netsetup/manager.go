package netsetup

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	bootGrace   = 60 * time.Second // no network this long after start: setup Wi-Fi
	lostGrace   = 15 * time.Minute // network gone this long: setup Wi-Fi again
	setupIP     = "10.42.0.1"
	captiveFile = "hub-captive.conf"
	// The setup Wi-Fi's password: no letters that look alike (0/O, 1/l/I).
	setupAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
)

// Manager keeps a hub reachable: while it has no network it runs the setup Wi-Fi
// "Hub-<hub ID>", from which an admin joins it to a Wi-Fi or makes it the facility's own
// Wi-Fi (docs/DEPLOYMENT.md, "Setup Wi-Fi"). A cable is left to NetworkManager.
type Manager struct {
	R          Runner
	DataDir    string // keeps the setup password and the chosen networks' names
	HubID      string
	DnsmasqDir string // NetworkManager's dnsmasq-shared.d: the captive answer goes here
	Now        func() time.Time
	// Delay runs a switch a moment later, so the page that asked for it can answer
	// first (tests run it at once).
	Delay func(func())

	mu       sync.Mutex // held through nmcli calls, which can take a minute
	pwMu     sync.Mutex // the setup password file
	snap     atomic.Pointer[snapshot]
	started  time.Time
	lastNet  time.Time // when the hub last had a network (zero: never since start)
	setupAt  time.Time // when the setup Wi-Fi last started
	devs     []Device
	networks []Network
	lastErr  string
}

// snapshot is what Status and Captive read, so the pages and every captive-portal
// request never wait for a switch in progress.
type snapshot struct {
	devs     []Device
	networks []Network
	lastErr  string
}

// publish updates the snapshot; called with mu held, after every change.
func (m *Manager) publish() {
	m.snap.Store(&snapshot{devs: slices.Clone(m.devs), networks: m.networks, lastErr: m.lastErr})
}

func (m *Manager) current() *snapshot {
	if s := m.snap.Load(); s != nil {
		return s
	}
	return &snapshot{}
}

// Status is what the hub's pages show about the network.
type Status struct {
	Mode          string // cable, modem, wifi, facility, setup or none
	Name          string // the Wi-Fi's name (wifi, facility) or the device (cable, modem)
	Addresses     []string
	Networks      []Network // the last scan, for joining
	LastError     string
	SetupSSID     string
	SetupPassword string
}

// saved is what the admin chose, in <DataDir>/network.json.
type saved struct {
	Wifi     string `json:"wifi,omitempty"`
	Facility string `json:"facility,omitempty"`
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Run ticks every 10 seconds until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		m.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick reads the devices and starts or stops the setup Wi-Fi.
func (m *Manager) Tick(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if m.started.IsZero() {
		m.started = now
	}
	devs, err := Devices(ctx, m.R)
	if err != nil {
		slog.Warn("network: reading devices", "err", err)
		return
	}
	m.devs = devs
	defer m.publish()
	kind, _ := Uplink(devs)
	setupOn, facilityOn := m.active(SetupConn), m.active(FacilityConn)
	switch {
	case kind != "":
		m.lastNet = now
		if setupOn {
			m.stopSetup(ctx)
		}
	case facilityOn:
		m.lastNet = now // the hub's own Wi-Fi is the facility's network
	case setupOn:
		// NetworkManager does not retry a joined Wi-Fi while the radio is an access
		// point: try it every 15 minutes, so a router that restarted is rejoined.
		if m.load().Wifi != "" && now.Sub(m.setupAt) >= lostGrace {
			m.stopSetup(ctx)
			if out, err := m.R.Run(ctx, "-w", "60", "connection", "up", WifiConn); err != nil {
				m.failed(ctx, errors.New(out+" "+err.Error()))
				return
			}
			m.lastErr, m.lastNet = "", now
			m.markUp(WifiConn)
			slog.Info("network: rejoined the Wi-Fi")
		}
	default:
		ref, grace := m.started, bootGrace
		if !m.lastNet.IsZero() {
			ref, grace = m.lastNet, lostGrace
		}
		if now.Sub(ref) >= grace {
			m.startSetup(ctx)
		}
	}
}

func (m *Manager) active(conn string) bool { return activeIn(m.devs, conn) }

func activeIn(devs []Device, conn string) bool {
	for _, d := range devs {
		if d.Connection == conn && strings.HasPrefix(d.State, "connected") {
			return true
		}
	}
	return false
}

// Captive reports whether the setup Wi-Fi is on: the hub then answers every name.
func (m *Manager) Captive() bool { return activeIn(m.current().devs, SetupConn) }

// startSetup scans first (one radio: scanning while it is an access point drops
// phones), then starts the setup Wi-Fi with every DNS name answered by the hub.
func (m *Manager) startSetup(ctx context.Context) {
	if nets, err := Scan(ctx, m.R); err == nil {
		m.networks = nets
	} else {
		slog.Warn("network: scan", "err", err)
	}
	pw, err := m.setupPassword()
	if err != nil {
		slog.Error("network: setup Wi-Fi password", "err", err)
		return
	}
	if err := m.writeCaptive(true); err != nil {
		slog.Warn("network: captive DNS", "err", err)
	}
	m.R.Run(ctx, "connection", "delete", SetupConn)
	if _, err := m.R.Run(ctx, "connection", "add", "type", "wifi", "ifname", "wlan0", "con-name", SetupConn,
		"autoconnect", "no", "ssid", m.setupSSID(), "802-11-wireless.mode", "ap", "802-11-wireless.band", "bg",
		"ipv4.method", "shared", "ipv4.addresses", setupIP+"/24", "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", pw); err != nil {
		slog.Error("network: adding the setup Wi-Fi", "err", err)
		return
	}
	if _, err := m.R.Run(ctx, "connection", "up", SetupConn); err != nil {
		slog.Error("network: starting the setup Wi-Fi", "err", err)
		return
	}
	m.markUp(SetupConn)
	m.setupAt = m.now()
	m.publish()
	slog.Info("network: no network; setup Wi-Fi on", "ssid", m.setupSSID())
}

func (m *Manager) stopSetup(ctx context.Context) {
	m.R.Run(ctx, "connection", "down", SetupConn)
	m.writeCaptive(false)
	m.markDown(SetupConn)
	m.publish()
	slog.Info("network: setup Wi-Fi off")
}

// markUp and markDown keep the device list current until the next Tick reads it.
func (m *Manager) markUp(conn string) {
	for i, d := range m.devs {
		if d.Type == "wifi" {
			m.devs[i].Connection, m.devs[i].State = conn, "connected"
		}
	}
}

func (m *Manager) markDown(conn string) {
	for i, d := range m.devs {
		if d.Connection == conn {
			m.devs[i].Connection, m.devs[i].State = "", "disconnected"
		}
	}
}

func (m *Manager) writeCaptive(on bool) error {
	if m.DnsmasqDir == "" {
		return nil
	}
	p := filepath.Join(m.DnsmasqDir, captiveFile)
	if !on {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.WriteFile(p, []byte("# The hub's setup Wi-Fi: every name leads to its setup page.\naddress=/#/"+setupIP+"\n"), 0o644)
}

// Join switches the hub to an existing Wi-Fi a moment later. If joining fails within
// a minute, the setup Wi-Fi comes back with the reason.
func (m *Manager) Join(ssid, password string) error {
	if strings.TrimSpace(ssid) == "" {
		return errors.New("choose a network")
	}
	m.delay(func() {
		ctx := context.Background()
		m.mu.Lock()
		defer m.mu.Unlock()
		m.stopSetup(ctx)
		// The new network under a temporary name: the Wi-Fi joined before stays until
		// this one works, and is retried if it does not.
		const trying = WifiConn + "-new"
		m.R.Run(ctx, "connection", "delete", trying)
		args := []string{"connection", "add", "type", "wifi", "ifname", "wlan0", "con-name", trying,
			"autoconnect", "yes", "ssid", ssid}
		if password != "" {
			args = append(args, "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", password)
		}
		if _, err := m.R.Run(ctx, args...); err != nil {
			m.failed(ctx, err)
			return
		}
		if out, err := m.R.Run(ctx, "-w", "60", "connection", "up", trying); err != nil {
			m.R.Run(ctx, "connection", "delete", trying)
			m.failed(ctx, errors.New(out+" "+err.Error()))
			return
		}
		m.R.Run(ctx, "connection", "delete", WifiConn)
		if _, err := m.R.Run(ctx, "connection", "modify", trying, "connection.id", WifiConn); err != nil {
			slog.Warn("network: renaming the joined Wi-Fi", "err", err)
		}
		m.lastErr, m.lastNet = "", m.now()
		m.markUp(WifiConn)
		m.publish()
		m.save(func(s *saved) { s.Wifi, s.Facility = ssid, "" })
		m.R.Run(ctx, "connection", "delete", FacilityConn)
		slog.Info("network: joined a Wi-Fi", "ssid", ssid)
	})
	return nil
}

// MakeFacilityWifi makes the hub the facility's Wi-Fi (case C) a moment later.
func (m *Manager) MakeFacilityWifi(ssid, password string) error {
	if strings.TrimSpace(ssid) == "" {
		return errors.New("give the Wi-Fi a name")
	}
	if len(password) < 8 {
		return errors.New("the password needs at least 8 characters")
	}
	m.delay(func() {
		ctx := context.Background()
		m.mu.Lock()
		defer m.mu.Unlock()
		m.stopSetup(ctx)
		m.R.Run(ctx, "connection", "delete", WifiConn)
		m.R.Run(ctx, "connection", "delete", FacilityConn)
		if _, err := m.R.Run(ctx, "connection", "add", "type", "wifi", "ifname", "wlan0", "con-name", FacilityConn,
			"autoconnect", "yes", "ssid", ssid, "802-11-wireless.mode", "ap", "802-11-wireless.band", "bg",
			"ipv4.method", "shared", "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", password); err != nil {
			m.failed(ctx, err)
			return
		}
		if out, err := m.R.Run(ctx, "connection", "up", FacilityConn); err != nil {
			m.failed(ctx, errors.New(out+" "+err.Error()))
			return
		}
		m.lastErr, m.lastNet = "", m.now()
		m.markUp(FacilityConn)
		m.publish()
		m.save(func(s *saved) { s.Facility, s.Wifi = ssid, "" })
		slog.Info("network: the hub is the facility's Wi-Fi", "ssid", ssid)
	})
	return nil
}

func (m *Manager) delay(f func()) {
	if m.Delay != nil {
		m.Delay(f)
		return
	}
	time.AfterFunc(5*time.Second, f)
}

// failed brings the setup Wi-Fi back with the reason, in words an admin understands.
func (m *Manager) failed(ctx context.Context, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Secrets were required"), strings.Contains(msg, "802-1X supplicant"):
		m.lastErr = "wrong password"
	case strings.Contains(msg, "No network with SSID"):
		m.lastErr = "network not found"
	default:
		m.lastErr = strings.TrimSpace(msg)
	}
	slog.Warn("network: switching failed; setup Wi-Fi back", "reason", m.lastErr)
	m.publish()
	m.startSetup(ctx)
}

// Status is the current network for the hub's pages.
func (m *Manager) Status() Status {
	snap := m.current()
	pw, _ := m.setupPassword()
	st := Status{Networks: snap.networks, LastError: snap.lastErr, SetupSSID: m.setupSSID(), SetupPassword: pw, Mode: "none"}
	sv := m.load()
	kind, name := Uplink(snap.devs)
	switch {
	case activeIn(snap.devs, SetupConn):
		st.Mode = "setup"
	case activeIn(snap.devs, FacilityConn):
		st.Mode, st.Name = "facility", sv.Facility
	case kind == "wifi":
		st.Mode, st.Name = kind, sv.Wifi
	case kind != "":
		st.Mode, st.Name = kind, name
	}
	for _, d := range snap.devs {
		st.Addresses = append(st.Addresses, d.Addresses...)
	}
	return st
}

func (m *Manager) setupSSID() string { return "Hub-" + m.HubID }

// setupPassword is made once, at first use, and kept in the data folder.
func (m *Manager) setupPassword() (string, error) {
	m.pwMu.Lock()
	defer m.pwMu.Unlock()
	p := filepath.Join(m.DataDir, "setup-wifi")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) == 12 {
		return strings.TrimSpace(string(b)), nil
	}
	b := make([]byte, 12)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(setupAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = setupAlphabet[n.Int64()]
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	return string(b), nil
}

func (m *Manager) load() saved {
	var s saved
	if b, err := os.ReadFile(filepath.Join(m.DataDir, "network.json")); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (m *Manager) save(f func(*saved)) {
	s := m.load()
	f(&s)
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(m.DataDir, "network.json"), b, 0o600); err != nil {
		slog.Warn("network: saving the choice", "err", err)
	}
}
