package netsetup

import (
	"context"
	"github.com/dhis2/facility-hub/internal/netsetup/nmfake"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type rig struct {
	nm  *nmfake.NM
	m   *Manager
	now time.Time
	t   *testing.T
}

func newRig(t *testing.T) *rig {
	nm := nmfake.New(nmfake.Net{SSID: "Clinic", Signal: 70, Secure: true}, nmfake.Net{SSID: "Open", Signal: 40})
	r := &rig{nm: nm, now: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC), t: t}
	r.m = &Manager{R: nm, DataDir: t.TempDir(), HubID: "hub-abc123", DnsmasqDir: t.TempDir(),
		Now: func() time.Time { return r.now }, Delay: func(f func()) { f() }}
	return r
}

func (r *rig) tick(after time.Duration) {
	r.now = r.now.Add(after)
	r.m.Tick(context.Background())
}

func (r *rig) captiveFile() bool {
	_, err := os.Stat(filepath.Join(r.m.DnsmasqDir, "hub-captive.conf"))
	return err == nil
}

func TestBootWithoutNetworkStartsSetup(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(30 * time.Second)
	if r.nm.Up[SetupConn] {
		t.Fatal("setup Wi-Fi before 60 seconds")
	}
	r.tick(31 * time.Second)
	if !r.nm.Up[SetupConn] || !r.captiveFile() || !r.m.Captive() {
		t.Fatalf("no setup Wi-Fi after 61 s: %v", r.nm.Calls)
	}
	add := strings.Join(r.nm.Conns[SetupConn], " ")
	for _, want := range []string{"ssid Hub-hub-abc123", "802-11-wireless.mode ap", "ipv4.method shared",
		"ipv4.addresses 10.42.0.1/24", "wifi-sec.key-mgmt wpa-psk", "autoconnect no"} {
		if !strings.Contains(add, want) {
			t.Errorf("setup connection lacks %q: %s", want, add)
		}
	}
	st := r.m.Status()
	if st.Mode != "setup" || st.SetupSSID != "Hub-hub-abc123" || len(st.Networks) != 2 || st.Networks[0].SSID != "Clinic" {
		t.Fatalf("status %+v", st)
	}
	if !strings.Contains(add, "wifi-sec.psk "+st.SetupPassword) {
		t.Error("setup Wi-Fi without its password")
	}
}

func TestCableStopsSetupNotFacility(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(61 * time.Second)
	r.nm.Cable = true
	r.tick(10 * time.Second)
	if r.nm.Up[SetupConn] || r.captiveFile() {
		t.Fatal("setup Wi-Fi still on with a cable")
	}
	if st := r.m.Status(); st.Mode != "cable" || len(st.Addresses) == 0 {
		t.Fatalf("status %+v", st)
	}
	if err := r.m.MakeFacilityWifi("Clinic Staff", "longenough"); err != nil {
		t.Fatal(err)
	}
	r.tick(10 * time.Second)
	if !r.nm.Up[FacilityConn] {
		t.Fatal("a cable stopped the facility's Wi-Fi")
	}
}

func TestJoinSucceeds(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(61 * time.Second)
	if err := r.m.Join("Clinic", "secret123"); err != nil {
		t.Fatal(err)
	}
	if r.nm.Up[SetupConn] || !r.nm.Up[WifiConn] || r.captiveFile() {
		t.Fatalf("not joined: up %v", r.nm.Up)
	}
	add := strings.Join(r.nm.Conns[WifiConn], " ")
	if !strings.Contains(add, "ssid Clinic") || !strings.Contains(add, "wifi-sec.psk secret123") {
		t.Errorf("join: %s", add)
	}
	r.tick(10 * time.Second)
	if st := r.m.Status(); st.Mode != "wifi" || st.Name != "Clinic" || st.LastError != "" {
		t.Fatalf("status %+v", st)
	}
}

func TestJoinOpenNetwork(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(61 * time.Second)
	if err := r.m.Join("Open", ""); err != nil {
		t.Fatal(err)
	}
	if add := strings.Join(r.nm.Conns[WifiConn], " "); strings.Contains(add, "wifi-sec") {
		t.Errorf("open network with security: %s", add)
	}
}

func TestJoinFailsBringsSetupBack(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(61 * time.Second)
	r.nm.FailUp[WifiConn+"-new"] = "Error: Connection activation failed: Secrets were required, but not provided."
	r.m.Join("Clinic", "wrong")
	if !r.nm.Up[SetupConn] || !r.captiveFile() {
		t.Fatalf("no setup Wi-Fi after a failed join: up %v", r.nm.Up)
	}
	if st := r.m.Status(); st.LastError != "wrong password" {
		t.Fatalf("last error %q", st.LastError)
	}
	if r.nm.Conns[WifiConn+"-new"] != nil || r.nm.Conns[WifiConn] != nil {
		t.Error("a failed Wi-Fi is kept and would be retried")
	}
	r.nm.FailUp[WifiConn+"-new"] = "Error: No network with SSID 'Gone' found."
	r.m.Join("Gone", "x")
	if st := r.m.Status(); st.LastError != "network not found" {
		t.Fatalf("last error %q", st.LastError)
	}
}

func TestJoinedNetworkLostFor15Minutes(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(61 * time.Second)
	r.m.Join("Clinic", "secret123")
	r.tick(10 * time.Second)
	delete(r.nm.Up, WifiConn) // the router is gone
	r.tick(14 * time.Minute)
	if r.nm.Up[SetupConn] {
		t.Fatal("setup Wi-Fi before 15 minutes")
	}
	r.tick(61 * time.Second)
	if !r.nm.Up[SetupConn] {
		t.Fatal("no setup Wi-Fi after 15 minutes without a network")
	}
}

func TestFacilityWifi(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(61 * time.Second)
	if err := r.m.MakeFacilityWifi("Clinic Staff", "short"); err == nil {
		t.Fatal("password of 5 characters accepted")
	}
	if err := r.m.MakeFacilityWifi("", "longenough"); err == nil {
		t.Fatal("empty name accepted")
	}
	if err := r.m.MakeFacilityWifi("Clinic Staff", "longenough"); err != nil {
		t.Fatal(err)
	}
	if !r.nm.Up[FacilityConn] || r.nm.Up[SetupConn] || r.captiveFile() || r.m.Captive() {
		t.Fatalf("up %v captive %v", r.nm.Up, r.captiveFile())
	}
	add := strings.Join(r.nm.Conns[FacilityConn], " ")
	for _, want := range []string{"ssid Clinic Staff", "802-11-wireless.mode ap", "ipv4.method shared", "wifi-sec.psk longenough", "autoconnect yes"} {
		if !strings.Contains(add, want) {
			t.Errorf("facility connection lacks %q: %s", want, add)
		}
	}
	r.tick(20 * time.Minute) // no uplink: the facility's Wi-Fi is the network, no setup Wi-Fi
	if r.nm.Up[SetupConn] || !r.nm.Up[FacilityConn] {
		t.Fatalf("up %v", r.nm.Up)
	}
	if st := r.m.Status(); st.Mode != "facility" || st.Name != "Clinic Staff" {
		t.Fatalf("status %+v", st)
	}
}

func TestSetupPasswordStable(t *testing.T) {
	r := newRig(t)
	p1 := r.m.Status().SetupPassword
	m2 := &Manager{R: r.nm, DataDir: r.m.DataDir, HubID: "hub-abc123", Now: r.m.Now}
	if p2 := m2.Status().SetupPassword; p1 != p2 || len(p1) != 12 {
		t.Fatalf("%q then %q", p1, p2)
	}
	for _, c := range p1 {
		if !strings.ContainsRune(setupAlphabet, c) {
			t.Fatalf("%q outside the alphabet", c)
		}
	}
	if setupAlphabet != "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789" {
		t.Fatal("alphabet changed")
	}
}

// A router that restarts: the hub falls back to the setup Wi-Fi, then tries the joined
// Wi-Fi again every 15 minutes (NetworkManager does not, while the radio is an access
// point), so nobody has to come back to rejoin it.
func TestSavedWifiRetried(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(61 * time.Second)
	r.m.Join("Clinic", "secret123")
	r.tick(10 * time.Second)
	delete(r.nm.Up, WifiConn)
	r.nm.FailUp[WifiConn] = "Error: No network with SSID 'Clinic' found."
	r.tick(15*time.Minute + time.Second)
	if !r.nm.Up[SetupConn] {
		t.Fatal("no setup Wi-Fi")
	}
	n := r.nm.UpCount[WifiConn]
	r.tick(15*time.Minute + time.Second) // still gone: tried, setup Wi-Fi back
	if r.nm.UpCount[WifiConn] != n+1 || !r.nm.Up[SetupConn] {
		t.Fatalf("retries %d → %d, up %v", n, r.nm.UpCount[WifiConn], r.nm.Up)
	}
	delete(r.nm.FailUp, WifiConn) // the router is back
	r.tick(15*time.Minute + time.Second)
	if !r.nm.Up[WifiConn] || r.nm.Up[SetupConn] || r.captiveFile() {
		t.Fatalf("not rejoined: up %v", r.nm.Up)
	}
}

// Review: a Wi-Fi password never reaches the pages or the logs through an error.
func TestErrorHidesSecrets(t *testing.T) {
	err := nmcliError([]string{"connection", "add", "type", "wifi", "ssid", "Clinic", "wifi-sec.psk", "hunter22"}, "Error: invalid psk")
	if strings.Contains(err.Error(), "hunter22") || !strings.Contains(err.Error(), "connection add") {
		t.Fatalf("%v", err)
	}
}

// Review: a failed join keeps the Wi-Fi joined before, which the hub keeps retrying.
func TestFailedJoinKeepsPreviousWifi(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(61 * time.Second)
	r.m.Join("Clinic", "secret123")
	r.tick(10 * time.Second)
	delete(r.nm.Up, WifiConn) // the router is gone
	r.tick(15*time.Minute + time.Second)
	r.nm.FailUp[WifiConn+"-new"] = "Error: Connection activation failed: Secrets were required, but not provided."
	r.m.Join("Other", "wrong")
	if !strings.Contains(strings.Join(r.nm.Conns[WifiConn], " "), "ssid Clinic") {
		t.Fatalf("the Wi-Fi joined before is gone: %v", r.nm.Conns)
	}
	if r.nm.Conns[WifiConn+"-new"] != nil {
		t.Error("the failed profile is kept")
	}
	n := r.nm.UpCount[WifiConn]
	r.tick(15*time.Minute + time.Second)
	if !r.nm.Up[WifiConn] || r.nm.UpCount[WifiConn] != n+1 {
		t.Fatalf("the Wi-Fi joined before was not retried: up %v", r.nm.Up)
	}
}

// Review: the pages (and the captive check on every phone request) do not wait for a
// switch, which can take a minute.
func TestStatusDoesNotWaitForSwitch(t *testing.T) {
	r := newRig(t)
	r.tick(0)
	r.tick(61 * time.Second)
	release := make(chan struct{})
	r.nm.Block = map[string]chan struct{}{WifiConn + "-new": release}
	r.nm.Blocked = make(chan string, 1)
	done := make(chan struct{})
	r.m.Delay = func(f func()) { go func() { f(); close(done) }() }
	r.m.Join("Clinic", "secret123")
	<-r.nm.Blocked
	got := make(chan Status, 1)
	go func() { r.m.Captive(); got <- r.m.Status() }()
	select {
	case st := <-got:
		if st.SetupSSID == "" {
			t.Errorf("status %+v", st)
		}
	case <-time.After(time.Second):
		t.Error("Status waited for the switch")
	}
	close(release)
	<-done
}
