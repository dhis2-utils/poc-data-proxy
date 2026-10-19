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

// stateful answers the commands the manager sends (manager_test.go fills it in).
func (f *fakeNM) stateful(args []string) (string, error) {
	return "", fmt.Errorf("fake nmcli: no state for %q", strings.Join(args, " "))
}
