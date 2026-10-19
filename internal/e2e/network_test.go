package e2e

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/netsetup"
	"github.com/dhis2/facility-hub/internal/netsetup/nmfake"
)

// netEnv is a Pi hub with no network: NetworkManager is the fake, and the setup Wi-Fi
// is on (the hub has been up 61 seconds).
func netEnv(t *testing.T) (*env, string, *nmfake.NM, *netsetup.Manager) {
	e, usb := stickEnv(t, false)
	nm := nmfake.New(nmfake.Net{SSID: "Clinic", Signal: 70, Secure: true})
	now := time.Now()
	m := &netsetup.Manager{R: nm, DataDir: e.dir, HubID: e.app.Hub.HubID(), DnsmasqDir: t.TempDir(),
		Now: func() time.Time { return now }, Delay: func(f func()) { f() }}
	e.app.SetNet(m)
	m.Tick(context.Background())
	now = now.Add(61 * time.Second)
	m.Tick(context.Background())
	if !m.Captive() {
		t.Fatal("no setup Wi-Fi")
	}
	return e, usb, nm, m
}

func body(t *testing.T, e *env, user, path string, form url.Values) (string, string) {
	t.Helper()
	resp, err := e.web(user).postForm(path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.Header.Get("Location")
}

// DEPLOYMENT.md, "Setup Wi-Fi": on the setup Wi-Fi the setup page lists the networks
// in reach; joining one tells the admin where to go next.
func TestNetworkSectionJoin(t *testing.T) {
	e, _, nm, m := netEnv(t)
	_, page := e.web("nurse_a").do("GET", "/hub/setup", nil)
	for _, want := range []string{"Setup Wi-Fi", "Clinic", "Join this Wi-Fi", "Make this hub the facility's Wi-Fi"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("setup page lacks %q", want)
		}
	}
	got, _ := body(t, e, "nurse_a", "/hub/network/join", url.Values{"ssid": {"Clinic"}, "password": {"secret123"}})
	if !strings.Contains(got, "Switch your phone to Clinic and open http://dhis2.local") {
		t.Fatalf("join answered: %.400s", got)
	}
	if !nm.Up[netsetup.WifiConn] || m.Captive() {
		t.Fatalf("not joined: %v", nm.Up)
	}
}

func TestNetworkSectionFacilityTooShort(t *testing.T) {
	e, _, nm, _ := netEnv(t)
	got, _ := body(t, e, "nurse_a", "/hub/network/facility", url.Values{"ssid": {"Clinic Staff"}, "password": {"short"}})
	if !strings.Contains(got, "at least 8 characters") || nm.Up[netsetup.FacilityConn] {
		t.Fatalf("answered: %.300s", got)
	}
}

// After set-up, changing the network needs a staff login.
func TestNetworkNeedsLoginAfterSetup(t *testing.T) {
	e, _, nm, _ := netEnv(t)
	e.setup()
	_, loc := body(t, e, "nobody", "/hub/network/join", url.Values{"ssid": {"Clinic"}, "password": {"secret123"}})
	if loc != "/hub/login" || nm.Up[netsetup.WifiConn] {
		t.Fatalf("sent to %q, up %v", loc, nm.Up)
	}
}

// hub.txt on the USB stick: what anyone holding the box needs, kept current.
func TestHubTxtOnStick(t *testing.T) {
	e, usb, _, m := netEnv(t)
	m.Tick(context.Background()) // the setup Wi-Fi's address
	e.app.Stick.Tick(context.Background())
	id := e.app.Hub.HubID()
	st := m.Status()
	b, err := os.ReadFile(filepath.Join(usb, "facility-hub", "hub.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := "Facility hub " + id + "\nOpen: http://dhis2.local\nAddresses: 10.42.0.1\nSetup Wi-Fi: Hub-" + id +
		"\nSetup Wi-Fi password: " + st.SetupPassword + "\n"
	if string(b) != want {
		t.Fatalf("hub.txt:\n%s\nwant:\n%s", b, want)
	}
	m.MakeFacilityWifi("Clinic Staff", "longenough")
	m.Tick(context.Background())
	e.app.Stick.Tick(context.Background())
	b, _ = os.ReadFile(filepath.Join(usb, "facility-hub", "hub.txt"))
	if !strings.HasSuffix(string(b), "Facility Wi-Fi: Clinic Staff\n") || strings.Contains(string(b), "longenough") {
		t.Fatalf("hub.txt in case C:\n%s", b)
	}
}
