// Package netsetup gives a Pi hub its network without a screen (docs/DEPLOYMENT.md,
// section 4): it drives NetworkManager through nmcli, starts a setup Wi-Fi while the
// hub has no network, joins the Wi-Fi an admin picks, or makes the hub the facility's
// own Wi-Fi. Windows hubs use the PC's network and none of this.
package netsetup

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// Runner runs nmcli with the given arguments and returns its output.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// ExecRunner runs the real nmcli.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "nmcli", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("nmcli %s: %s", args[len(args)-1], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Device is a network device as NetworkManager reports it.
type Device struct {
	Name, Type, State, Connection, Driver string
	Addresses                             []string // IPv4, without prefix length
}

// Network is a Wi-Fi network found by a scan.
type Network struct {
	SSID   string
	Signal int
	Secure bool
}

// fields splits a line of nmcli's terse output on unescaped colons, unescaping "\:"
// and "\\".
func fields(line string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case c == ':':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, cur.String())
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Devices lists the Ethernet and Wi-Fi devices with their drivers and addresses.
func Devices(ctx context.Context, r Runner) ([]Device, error) {
	out, err := r.Run(ctx, "-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device")
	if err != nil {
		return nil, err
	}
	var devs []Device
	for _, l := range lines(out) {
		f := fields(l)
		if len(f) < 4 || (f[1] != "ethernet" && f[1] != "wifi") {
			continue
		}
		d := Device{Name: f[0], Type: f[1], State: f[2], Connection: f[3]}
		show, err := r.Run(ctx, "-t", "-f", "GENERAL.DRIVER,IP4.ADDRESS", "device", "show", d.Name)
		if err != nil {
			return nil, err
		}
		for _, sl := range lines(show) {
			k, v, _ := strings.Cut(sl, ":")
			switch {
			case k == "GENERAL.DRIVER":
				d.Driver = v
			case strings.HasPrefix(k, "IP4.ADDRESS"):
				addr, _, _ := strings.Cut(v, "/")
				d.Addresses = append(d.Addresses, addr)
			}
		}
		devs = append(devs, d)
	}
	return devs, nil
}

// Scan lists the Wi-Fi networks in reach, strongest first, each name once.
func Scan(ctx context.Context, r Runner) ([]Network, error) {
	out, err := r.Run(ctx, "-t", "-f", "SSID,SIGNAL,SECURITY", "device", "wifi", "list", "--rescan", "yes")
	if err != nil {
		return nil, err
	}
	best := map[string]Network{}
	for _, l := range lines(out) {
		f := fields(l)
		if len(f) < 3 || f[0] == "" {
			continue
		}
		sig, _ := strconv.Atoi(f[1])
		n := Network{SSID: f[0], Signal: sig, Secure: strings.TrimSpace(f[2]) != ""}
		if b, ok := best[n.SSID]; !ok || n.Signal > b.Signal {
			best[n.SSID] = n
		}
	}
	nets := make([]Network, 0, len(best))
	for _, n := range best {
		nets = append(nets, n)
	}
	slices.SortFunc(nets, func(a, b Network) int { return b.Signal - a.Signal })
	return nets, nil
}

// Drivers of USB modems that work as a network card ("router in a stick", DEPLOYMENT.md,
// "USB modems"), and of a phone's USB tethering.
var modemDrivers = []string{"cdc_ether", "rndis_host", "cdc_ncm", "ipheth", "qmi_wwan"}

// Uplink is the connection the hub reaches the internet through: a cable, a USB modem,
// or a joined Wi-Fi; "" if none has an address. The hub's own access points are not
// uplinks. name is the device for wired ones and the connection for Wi-Fi.
func Uplink(devs []Device) (kind, name string) {
	ready := func(d Device) bool { return strings.HasPrefix(d.State, "connected") && len(d.Addresses) > 0 }
	for _, want := range []string{"cable", "modem", "wifi"} {
		for _, d := range devs {
			if !ready(d) {
				continue
			}
			modem := slices.Contains(modemDrivers, d.Driver)
			switch {
			case want == "cable" && d.Type == "ethernet" && !modem,
				want == "modem" && d.Type == "ethernet" && modem:
				return want, d.Name
			case want == "wifi" && d.Type == "wifi" && d.Connection != SetupConn && d.Connection != FacilityConn:
				return want, d.Connection
			}
		}
	}
	return "", ""
}

// The connections the hub manages.
const (
	SetupConn    = "hub-setup"
	WifiConn     = "hub-wifi"
	FacilityConn = "hub-facility"
)
