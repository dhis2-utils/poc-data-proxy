// Package nmfake is a stand-in for nmcli in tests: canned answers, and a small model
// of a cable and one Wi-Fi radio running at most one connection.
package nmfake

import (
	"context"
	"fmt"
	"strings"
)

// NM answers nmcli commands from canned output (parsing tests) and, in the
// manager's tests, keeps the state of devices and connections.
// NM answers nmcli commands.
type NM struct {
	Out   map[string]string // "args joined by space" → output
	Calls []string
	// Stateful part (manager tests).
	Scan    []Net
	Conns   map[string][]string // connection name → the add arguments
	Up      map[string]bool
	FailUp  map[string]string // connection → nmcli error message
	UpCount map[string]int
	Cable   bool
	// Block holds "connection up <name>" until the channel is closed; Blocked hears
	// the name when it starts waiting.
	Block   map[string]chan struct{}
	Blocked chan string
}

func (f *NM) Run(ctx context.Context, args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	f.Calls = append(f.Calls, cmd)
	if o, ok := f.Out[cmd]; ok {
		return o, nil
	}
	if f.Conns == nil {
		return "", fmt.Errorf("fake nmcli: no answer for %q", cmd)
	}
	return f.stateful(args)
}

// stateful answers the commands the manager sends, from a small model: a cable that
// is plugged in or not, and one Wi-Fi radio running at most one connection.
func (f *NM) stateful(args []string) (string, error) {
	cmd := strings.Join(args, " ")
	switch {
	case cmd == "-t -f DEVICE,TYPE,STATE,CONNECTION device":
		eth := "eth0:ethernet:unavailable:"
		if f.Cable {
			eth = "eth0:ethernet:connected:Wired connection 1"
		}
		wl := "wlan0:wifi:disconnected:"
		for c := range f.Up {
			wl = "wlan0:wifi:connected:" + c
		}
		return eth + "\n" + wl + "\n", nil
	case cmd == "-t -f GENERAL.DRIVER,IP4.ADDRESS device show eth0":
		if f.Cable {
			return "GENERAL.DRIVER:bcmgenet\nIP4.ADDRESS[1]:192.168.1.20/24\n", nil
		}
		return "GENERAL.DRIVER:bcmgenet\n", nil
	case cmd == "-t -f GENERAL.DRIVER,IP4.ADDRESS device show wlan0":
		for c := range f.Up {
			if c == "hub-wifi" {
				return "GENERAL.DRIVER:brcmfmac\nIP4.ADDRESS[1]:10.1.1.5/24\n", nil
			}
			return "GENERAL.DRIVER:brcmfmac\nIP4.ADDRESS[1]:10.42.0.1/24\n", nil
		}
		return "GENERAL.DRIVER:brcmfmac\n", nil
	case cmd == "-t -f SSID,SIGNAL,SECURITY device wifi list --rescan yes":
		if len(f.Up) > 0 {
			return "", fmt.Errorf("scanning while the radio is busy")
		}
		var b strings.Builder
		for _, n := range f.Scan {
			sec := ""
			if n.Secure {
				sec = "WPA2"
			}
			fmt.Fprintf(&b, "%s:%d:%s\n", strings.ReplaceAll(n.SSID, ":", "\\:"), n.Signal, sec)
		}
		return b.String(), nil
	case len(args) >= 3 && args[0] == "connection" && args[1] == "add":
		name := argAfter(args, "con-name")
		f.Conns[name] = args
		return "", nil
	case len(args) == 3 && args[0] == "connection" && args[1] == "delete":
		delete(f.Conns, args[2])
		delete(f.Up, args[2])
		return "", nil
	case len(args) == 5 && args[0] == "connection" && args[1] == "modify" && args[3] == "connection.id":
		if f.Conns[args[2]] == nil {
			return "", fmt.Errorf("unknown connection %s", args[2])
		}
		f.Conns[args[4]] = f.Conns[args[2]]
		delete(f.Conns, args[2])
		if f.Up[args[2]] {
			delete(f.Up, args[2])
			f.Up[args[4]] = true
		}
		return "", nil
	case len(args) == 3 && args[0] == "connection" && args[1] == "down":
		delete(f.Up, args[2])
		return "", nil
	case strings.HasSuffix(cmd, "connection up "+args[len(args)-1]):
		name := args[len(args)-1]
		if f.Conns[name] == nil {
			return "", fmt.Errorf("unknown connection %s", name)
		}
		f.UpCount[name]++
		if ch := f.Block[name]; ch != nil {
			if f.Blocked != nil {
				f.Blocked <- name
			}
			<-ch
		}
		if msg := f.FailUp[name]; msg != "" {
			return msg, fmt.Errorf("nmcli up: %s", msg)
		}
		for c := range f.Up {
			delete(f.Up, c) // one radio
		}
		f.Up[name] = true
		return "", nil
	}
	return "", fmt.Errorf("fake nmcli: unexpected %q", cmd)
}

// Net is a Wi-Fi network in reach.
type Net struct {
	SSID   string
	Signal int
	Secure bool
}

// New is a fake with no cable, nothing running and these networks in reach.
func New(scan ...Net) *NM {
	return &NM{Conns: map[string][]string{}, Up: map[string]bool{}, FailUp: map[string]string{},
		UpCount: map[string]int{}, Scan: scan}
}

func argAfter(args []string, key string) string {
	for i, a := range args {
		if a == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
