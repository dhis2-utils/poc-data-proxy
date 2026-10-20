package main

import (
	"errors"
	"reflect"
	"testing"
)

// Security review S2: restoring from the USB stick asked for a restart that only
// stopped the web server, and the process then waited for a signal nobody sent. The
// restart now stops serving, closes the store and replaces the process (in place on
// Linux and macOS; through the service manager on Windows), as an upgrade does.
func TestRestartStopsClosesAndReplaces(t *testing.T) {
	var calls []string
	restart := restartFunc(
		func() { calls = append(calls, "shutdown") },
		func() error { calls = append(calls, "close"); return nil },
		func() (string, error) { return "/usr/local/lib/hub/hub", nil },
		func(exe string, dk []byte) error {
			calls = append(calls, "reexec "+exe)
			if dk != nil {
				t.Error("a restart hands over no key: the restored hub waits for a login")
			}
			return errors.New("stop here")
		},
		func(code int) { calls = append(calls, "exit") },
	)
	restart()
	want := []string{"shutdown", "close", "reexec /usr/local/lib/hub/hub", "exit"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls %v, want %v", calls, want)
	}
}
