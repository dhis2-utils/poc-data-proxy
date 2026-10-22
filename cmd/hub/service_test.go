package main

import (
	"testing"
	"time"
)

func TestServiceArgs(t *testing.T) {
	for _, c := range []struct {
		args             []string
		action, cfg, exe string
		bad              bool
	}{
		{[]string{"run", "-config", `C:\ProgramData\DHIS2 Facility Hub\hub.yaml`}, "run", `C:\ProgramData\DHIS2 Facility Hub\hub.yaml`, "", false},
		{[]string{"install", "-config", "hub.yaml", "-exe", `C:\x\hub.exe`}, "install", "hub.yaml", `C:\x\hub.exe`, false},
		{[]string{"uninstall"}, "uninstall", "", "", false},
		{[]string{"stop"}, "stop", "", "", false},
		{[]string{"install", "-config", "hub.yaml", "-exe", "hub.exe", "-nostart"}, "install", "hub.yaml", "hub.exe", false},
		{[]string{"install", "-config", "hub.yaml"}, "", "", "", true}, // no exe
		{[]string{"run"}, "", "", "", true},                            // no config
		{[]string{"restart"}, "", "", "", true},
		{nil, "", "", "", true},
	} {
		a, err := parseServiceArgs(c.args)
		if (err != nil) != c.bad || (!c.bad && (a.action != c.action || a.config != c.cfg || a.exe != c.exe)) {
			t.Errorf("%q: %+v %v", c.args, a, err)
		}
	}
	if a, _ := parseServiceArgs([]string{"install", "-config", "c", "-exe", "e", "-nostart"}); !a.noStart {
		t.Error("-nostart not read")
	}
}

// Stopping reports progress (a rising checkpoint) until the hub has stopped, so the
// service manager waits instead of giving up on it, and gives up itself at the limit.
// The hub "stops" at the 4th checkpoint, not after a sleep: Windows' timers tick every
// 15.6 ms, and a busy runner reported only 2 checkpoints in 55 ms.
func TestStopReportsProgress(t *testing.T) {
	done := make(chan error, 1)
	var cps []uint32
	report := func(cp uint32) {
		cps = append(cps, cp)
		if cp == 4 {
			done <- nil
		}
	}
	if !waitReporting(done, 10*time.Second, 10*time.Millisecond, report) {
		t.Fatal("not stopped")
	}
	if len(cps) < 4 || cps[0] != 1 {
		t.Fatalf("checkpoints %v", cps)
	}
	for i := 1; i < len(cps); i++ {
		if cps[i] != cps[i-1]+1 {
			t.Fatalf("checkpoints %v", cps)
		}
	}
	if waitReporting(make(chan error), 30*time.Millisecond, 10*time.Millisecond, func(uint32) {}) {
		t.Fatal("a hub that never stops reported stopped")
	}
}
