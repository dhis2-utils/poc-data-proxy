package netsetup

import (
	"context"
	"fmt"
	"strings"
)

// fakeNM answers nmcli commands from canned output (parsing tests) and, in the
// manager's tests, keeps the state of devices and connections.
type fakeNM struct {
	out   map[string]string // "args joined by space" → output
	calls []string
	// Stateful part (manager tests).
	devs    []Device
	scan    []Network
	conns   map[string][]string // connection name → the add arguments
	up      map[string]bool
	failUp  map[string]string // connection → nmcli error message
	upCount map[string]int
	cable   bool
}

func (f *fakeNM) Run(ctx context.Context, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	if o, ok := f.out[cmd]; ok {
		return o, nil
	}
	if f.conns == nil {
		return "", fmt.Errorf("fake nmcli: no answer for %q", cmd)
	}
	return f.stateful(args)
}

// stateful answers the commands the manager sends, from a small model: a cable that
// is plugged in or not, and one Wi-Fi radio running at most one connection.
func (f *fakeNM) stateful(args []string) (string, error) {
	cmd := strings.Join(args, " ")
	switch {
	case cmd == "-t -f DEVICE,TYPE,STATE,CONNECTION device":
		eth := "eth0:ethernet:unavailable:"
		if f.cable {
			eth = "eth0:ethernet:connected:Wired connection 1"
		}
		wl := "wlan0:wifi:disconnected:"
		for c := range f.up {
			wl = "wlan0:wifi:connected:" + c
		}
		return eth + "\n" + wl + "\n", nil
	case cmd == "-t -f GENERAL.DRIVER,IP4.ADDRESS device show eth0":
		if f.cable {
			return "GENERAL.DRIVER:bcmgenet\nIP4.ADDRESS[1]:192.168.1.20/24\n", nil
		}
		return "GENERAL.DRIVER:bcmgenet\n", nil
	case cmd == "-t -f GENERAL.DRIVER,IP4.ADDRESS device show wlan0":
		for c := range f.up {
			if c == WifiConn {
				return "GENERAL.DRIVER:brcmfmac\nIP4.ADDRESS[1]:10.1.1.5/24\n", nil
			}
			return "GENERAL.DRIVER:brcmfmac\nIP4.ADDRESS[1]:10.42.0.1/24\n", nil
		}
		return "GENERAL.DRIVER:brcmfmac\n", nil
	case cmd == "-t -f SSID,SIGNAL,SECURITY device wifi list --rescan yes":
		if len(f.up) > 0 {
			return "", fmt.Errorf("scanning while the radio is busy")
		}
		var b strings.Builder
		for _, n := range f.scan {
			sec := ""
			if n.Secure {
				sec = "WPA2"
			}
			fmt.Fprintf(&b, "%s:%d:%s\n", strings.ReplaceAll(n.SSID, ":", "\\:"), n.Signal, sec)
		}
		return b.String(), nil
	case len(args) >= 3 && args[0] == "connection" && args[1] == "add":
		name := argAfter(args, "con-name")
		f.conns[name] = args
		return "", nil
	case len(args) == 3 && args[0] == "connection" && args[1] == "delete":
		delete(f.conns, args[2])
		delete(f.up, args[2])
		return "", nil
	case len(args) == 3 && args[0] == "connection" && args[1] == "down":
		delete(f.up, args[2])
		return "", nil
	case strings.HasSuffix(cmd, "connection up "+args[len(args)-1]):
		name := args[len(args)-1]
		if f.conns[name] == nil {
			return "", fmt.Errorf("unknown connection %s", name)
		}
		f.upCount[name]++
		if msg := f.failUp[name]; msg != "" {
			return msg, fmt.Errorf("nmcli up: %s", msg)
		}
		for c := range f.up {
			delete(f.up, c) // one radio
		}
		f.up[name] = true
		return "", nil
	}
	return "", fmt.Errorf("fake nmcli: unexpected %q", cmd)
}

func argAfter(args []string, key string) string {
	for i, a := range args {
		if a == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
