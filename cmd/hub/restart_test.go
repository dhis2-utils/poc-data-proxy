package main

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
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

// A per-user hub on Windows starts its successor (after an upgrade or a restore) before
// it exits: the successor waits for the ports instead of failing to start.
func TestListenWaitsForPorts(t *testing.T) {
	old, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := old.Addr().String()
	if _, err := listen(addr, 300*time.Millisecond); err == nil {
		t.Fatal("bound a port another listener holds")
	}
	go func() { time.Sleep(300 * time.Millisecond); old.Close() }()
	ln, err := listen(addr, 5*time.Second)
	if err != nil {
		t.Fatalf("the port was released, still: %v", err)
	}
	ln.Close()
}

// A hub whose device key does not open exits after a while, so it is started again and
// tries the TPM again; stopped meanwhile, it ends without an error.
func TestRetryLater(t *testing.T) {
	if err := retryLater(context.Background(), 10*time.Millisecond); err == nil {
		t.Fatal("no error to be restarted with")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := retryLater(ctx, time.Hour); err != nil {
		t.Fatalf("stopped: %v", err)
	}
}
