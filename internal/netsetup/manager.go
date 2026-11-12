package netsetup

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dhis2/facility-hub/internal/devkey"
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
	captiveFlag = "captive"
	// The setup Wi-Fi's password: no letters that look alike (0/O, 1/l/I).
	setupAlphabet = "0123456789"
	// The facility Wi-Fi's password: lowercase letters and digits without 0/o, 1/l/i.
	facilityAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"
)

// Manager keeps a hub reachable: while it has no network it runs the setup Wi-Fi
// "Hub-<hub ID>", from which an admin joins it to a Wi-Fi or makes it the facility's own
// Wi-Fi (docs/DEPLOYMENT.md, "Setup Wi-Fi"). A cable is left to NetworkManager.
type Manager struct {
	R       Runner
	DataDir string // keeps the setup password and the chosen networks' names
	HubID   string
	// CaptiveDir holds the file "captive" while the setup Wi-Fi is on. The hub never
	// writes dnsmasq's configuration: NetworkManager runs dnsmasq as root. On the Pi
	// image a root unit (hub-captive.path) puts one fixed line in NetworkManager's
	// dnsmasq-shared.d while the file exists, and removes it when the file goes
	// (docs/DEPLOYMENT.md, "Setup Wi-Fi").
	CaptiveDir string
	// CaptiveConf is the file that unit writes. dnsmasq reads it only when
	// NetworkManager starts a shared Wi-Fi, so the hub waits for it to follow the flag
	// (at most captiveWait) before it starts one. "": no wait.
	CaptiveConf string
	Now         func() time.Time
	// Delay runs a switch a moment later, so the page that asked for it can answer
	// first (tests run it at once).
	Delay func(func())

	mu       sync.Mutex // held through nmcli calls, which can take a minute
	pwMu     sync.Mutex // the password files
	snap     atomic.Pointer[snapshot]
	started  time.Time
	lastNet  time.Time // when the hub last had a network (zero: never since start)
	setupAt  time.Time // when the setup Wi-Fi last started
	devs     []Device
	networks []Network
	lastErr  string

	// A setup Wi-Fi that failed to start is retried at retryAt, after retryWait: 10
	// seconds, doubling to 10 minutes. Each attempt scans, which drops phones.
	retryAt   time.Time
	retryWait time.Duration
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
	// The facility Wi-Fi's password, when the hub made it (not one an admin chose).
	FacilityPassword string
}

// saved is what the admin chose, in <DataDir>/network.json.
type saved struct {
	Wifi        string `json:"wifi,omitempty"`
	Facility    string `json:"facility,omitempty"`
	HubPassword bool   `json:"hubPassword,omitempty"` // the facility Wi-Fi has facilityPassword
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
	devs, err := Devices(ctx, m.R)
	if err != nil {
		slog.Warn("network: reading devices", "err", err)
		return
	}
	m.devs = devs
	defer m.publish()
	kind, _ := Uplink(devs)
	setupOn, facilityOn := m.active(SetupConn), m.active(FacilityConn)
	if m.started.IsZero() {
		m.started = now
		// A hub stopped while its setup Wi-Fi ran left every name answered by itself.
		if !setupOn {
			if err := m.writeCaptive(false); err != nil {
				slog.Warn("network: captive DNS", "err", err)
			}
		}
	}
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
		if now.Sub(ref) >= grace && !now.Before(m.retryAt) {
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
// phones), then starts the setup Wi-Fi with every DNS name answered by the hub. If
// that fails, Tick tries again at retryAt.
func (m *Manager) startSetup(ctx context.Context) {
	if !m.setupOnce(ctx) {
		m.retryWait = min(max(2*m.retryWait, 10*time.Second), 10*time.Minute)
		m.retryAt = m.now().Add(m.retryWait)
		return
	}
	m.retryWait, m.retryAt = 0, time.Time{}
}

func (m *Manager) setupOnce(ctx context.Context) bool {
	if nets, err := Scan(ctx, m.R); err == nil {
		m.networks = nets
	} else {
		slog.Warn("network: scan", "err", err)
	}
	pw, err := m.setupPassword()
	if err != nil {
		slog.Error("network: setup Wi-Fi password", "err", err)
		return false
	}
	if err := m.writeCaptive(true); err != nil {
		slog.Warn("network: captive DNS", "err", err)
	}
	m.R.Run(ctx, "connection", "delete", SetupConn)
	if _, err := m.R.Run(ctx, "connection", "add", "type", "wifi", "ifname", "wlan0", "con-name", SetupConn,
		"autoconnect", "no", "ssid", m.setupSSID(), "802-11-wireless.mode", "ap", "802-11-wireless.band", "bg",
		"ipv4.method", "shared", "ipv4.addresses", setupIP+"/24", "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", pw); err != nil {
		slog.Error("network: adding the setup Wi-Fi", "err", err)
		return false
	}
	if _, err := m.R.Run(ctx, "connection", "up", SetupConn); err != nil {
		slog.Error("network: starting the setup Wi-Fi", "err", err)
		return false
	}
	m.markUp(SetupConn)
	m.setupAt = m.now()
	m.publish()
	slog.Info("network: no network; setup Wi-Fi on", "ssid", m.setupSSID())
	return true
}

func (m *Manager) stopSetup(ctx context.Context) {
	m.R.Run(ctx, "connection", "down", SetupConn)
	if err := m.writeCaptive(false); err != nil {
		slog.Warn("network: captive DNS", "err", err)
	}
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
	if m.CaptiveDir == "" {
		return nil
	}
	p := filepath.Join(m.CaptiveDir, captiveFlag)
	if on {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			return err
		}
	} else if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if m.CaptiveConf == "" {
		return nil
	}
	deadline := time.Now().Add(captiveWait)
	for {
		if _, err := os.Stat(m.CaptiveConf); (err == nil) == on {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the captive DNS answer did not follow the hub within %s (hub-captive.path)", captiveWait)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// captiveWait bounds the wait for the image's root unit to follow the flag.
var captiveWait = 5 * time.Second

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
		m.save(func(s *saved) { *s = saved{Wifi: ssid} })
		m.R.Run(ctx, "connection", "delete", FacilityConn)
		slog.Info("network: joined a Wi-Fi", "ssid", ssid)
	})
	return nil
}

// MakeFacilityWifi makes the hub the facility's Wi-Fi (case C) a moment later, under
// the setup Wi-Fi's name and the hub's own password (facilityPassword).
func (m *Manager) MakeFacilityWifi() (ssid, password string, err error) {
	return m.startFacility(false)
}

// NewFacilityPassword starts the facility's Wi-Fi again a moment later with a new
// password: every device joins again. A Wi-Fi an admin named (until 2026-10-27) takes
// the hub's name.
func (m *Manager) NewFacilityPassword() (ssid, password string, err error) {
	if m.load().Facility == "" {
		return "", "", errors.New("the hub is not the facility's Wi-Fi")
	}
	return m.startFacility(true)
}

func (m *Manager) startFacility(renew bool) (string, string, error) {
	ssid := m.setupSSID()
	pw, err := m.facilityPassword(renew)
	if err != nil {
		return "", "", err
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
			"ipv4.method", "shared", "wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", pw); err != nil {
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
		m.save(func(s *saved) { *s = saved{Facility: ssid, HubPassword: true} })
		slog.Info("network: the hub is the facility's Wi-Fi", "ssid", ssid)
	})
	return ssid, pw, nil
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
		if sv.HubPassword {
			st.FacilityPassword, _ = m.facilityPassword(false)
		}
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

// setupSSID is "Hub-" and the hub ID without its own "hub-" (Hub-Zvc7xMnFcVg, not
// Hub-hub-Zvc7xMnFcVg).
func (m *Manager) setupSSID() string { return "Hub-" + strings.TrimPrefix(m.HubID, "hub-") }

// setupPassword is made once, at first use, and kept in the data folder: 8 digits, the
// least Wi-Fi allows, typed on a phone's number pad. It only leads to the setup page,
// which needs the setup code or a staff login. One of another form (12 letters and
// digits, until 2026-10-22) is replaced; hub.txt on the stick follows.
func (m *Manager) setupPassword() (string, error) {
	return m.password("setup-wifi", "xxxxxxxx", setupAlphabet, false)
}

// facilityPassword is the facility Wi-Fi's, made once (or anew on renew) and kept in
// the data folder: 4 groups of 4 lowercase letters and digits with no look-alikes,
// about 80 bits, as "k7mp-x2qd-9wtr-b4nz". The admin page and hub.txt show it.
func (m *Manager) facilityPassword(renew bool) (string, error) {
	return m.password("facility-wifi", "xxxx-xxxx-xxxx-xxxx", facilityAlphabet, renew)
}

// password reads the file, or makes one of form: each "x" a character from alphabet,
// anything else as it is. One of another form is replaced.
func (m *Manager) password(name, form, alphabet string, renew bool) (string, error) {
	m.pwMu.Lock()
	defer m.pwMu.Unlock()
	p := filepath.Join(m.DataDir, name)
	if b, err := devkey.ReadFile(p); err == nil && !renew {
		if s := strings.TrimSpace(string(b)); fits(s, form, alphabet) {
			return s, nil
		}
	}
	b := []byte(form)
	for i := range b {
		if b[i] == 'x' {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			b[i] = alphabet[n.Int64()]
		}
	}
	if err := devkey.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	return string(b), nil
}

func fits(s, form, alphabet string) bool {
	if len(s) != len(form) {
		return false
	}
	for i := range s {
		if form[i] == 'x' && !strings.ContainsRune(alphabet, rune(s[i])) || form[i] != 'x' && s[i] != form[i] {
			return false
		}
	}
	return true
}

func (m *Manager) load() saved {
	var s saved
	if b, err := devkey.ReadFile(filepath.Join(m.DataDir, "network.json")); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (m *Manager) save(f func(*saved)) {
	s := m.load()
	f(&s)
	b, _ := json.Marshal(s)
	if err := devkey.WriteFile(filepath.Join(m.DataDir, "network.json"), b, 0o600); err != nil {
		slog.Warn("network: saving the choice", "err", err)
	}
}
