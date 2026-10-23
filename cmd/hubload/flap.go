package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"
)

// A flapping uplink: the internet going up and down while staff keep working, as at
// a facility on a poor line. The load starts with the uplink down; each restore is
// timed while the users go on (the hub going online, then its outbox emptying), and
// the users' requests pass through to the fake central while it is up.
var flapF = flag.String("flap", "", `cut and restore the uplink during the load, "down/up" (for example "10m/5m")`)

func parseFlap(s string) (down, up time.Duration, err error) {
	a, b, ok := strings.Cut(s, "/")
	if ok {
		down, err = time.ParseDuration(a)
	}
	if ok && err == nil {
		up, err = time.ParseDuration(b)
	}
	if !ok || err != nil || down <= 0 || up <= 0 {
		return 0, 0, fmt.Errorf(`-flap %q: want "down/up", for example "10m/5m"`, s)
	}
	return down, up, nil
}

// flapUplink alternates the uplink, down for down then up for up, until done is
// closed or the next change would come after stop. It leaves the uplink as it is.
func flapUplink(note func(string, ...any), done <-chan struct{}, stop time.Time, down, up time.Duration) {
	wait := func(d time.Duration) bool {
		if time.Now().Add(d).After(stop) {
			return false
		}
		select {
		case <-done:
			return false
		case <-time.After(d):
			return true
		}
	}
	for {
		if !wait(down) {
			return
		}
		restored := time.Now()
		post(*central+"/fake/up", nil)
		upUntil := restored.Add(up)
		online, ok := within(upUntil, func(s map[string]any) bool { return s["mode"] == "online" })
		drained, ok2 := time.Duration(0), false
		if ok {
			drained, ok2 = within(upUntil, func(s map[string]any) bool { n, _ := s["pending"].(float64); return n == 0 })
		}
		switch {
		case !ok:
			note("uplink up %s: the hub did not go online", up)
		case !ok2:
			note("uplink up: hub online after %s; outbox not empty when the uplink went down again (%s)", online.Round(time.Second), pendingNow())
		default:
			note("uplink up: hub online after %s, outbox empty after %s", online.Round(time.Second), drained.Round(time.Second))
		}
		if !wait(time.Until(upUntil)) {
			return
		}
		post(*central+"/fake/down", nil)
		note("uplink cut")
	}
}

// within polls the hub's status until ok or the deadline, and returns how long it
// took. Unlike waitStatus it gives up quietly: a slow hub is a result here.
func within(deadline time.Time, ok func(map[string]any) bool) (time.Duration, bool) {
	start := time.Now()
	for time.Now().Before(deadline) {
		var s map[string]any
		if json.Unmarshal([]byte(status()), &s) == nil && ok(s) {
			return time.Since(start), true
		}
		time.Sleep(2 * time.Second)
	}
	return time.Since(start), false
}

func pendingNow() string {
	var s map[string]any
	if json.Unmarshal([]byte(status()), &s) != nil {
		return "status unreadable"
	}
	return fmt.Sprintf("%v pending", s["pending"])
}
