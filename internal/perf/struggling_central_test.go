package perf

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestStrugglingCentral measures what one hub sends to a central that is overloaded:
// a sync cycle against a central that answers every request with 503, half of them,
// or every one slowly, next to a cycle against a healthy central. Users' access is
// refreshed in every cycle here (in a deployed hub, hourly), as in TestCentralLoad.
// The hub's sync sends one request at a time, and two failed pings (30 s) take it
// offline, which stops its syncs (docs/PERFORMANCE.md, "Load on central").
func TestStrugglingCentral(t *testing.T) {
	s, users := scale(t)
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Generate(s)
	e.Setup()
	for i := 0; i < users; i++ {
		if st, b := e.AndroidUser(fc.GenUser(i)).Do("GET", "/api/me?fields=id", nil); st != 200 {
			t.Fatalf("%s: %d %s", fc.GenUser(i), st, b)
		}
	}
	e.Sync()
	e.Net.Calls()

	report := []string{fmt.Sprintf("scale: %d TEs, %d users", s.TEs, users)}
	cases := []struct {
		name  string
		delay time.Duration
		fail  float64
		pings int // connectivity probes before the cycle (one every 15 s on a hub)
	}{
		{"healthy", 0, 0, 0},
		{"every request 503, before the hub notices", 0, 1, 0},
		{"every request 503, after two failed pings", 0, 1, 2},
		{"half the requests 503", 0, 0.5, 0},
		{"every answer 200 ms late", 200 * time.Millisecond, 0, 0},
	}
	for _, c := range cases {
		e.Fake.Struggle(c.delay, c.fail)
		for range c.pings {
			e.App.Hub.Monitor.Probe(context.Background(), e.App.Hub.Central.BaseURL())
		}
		start := time.Now()
		err := e.App.Worker.Cycle(context.Background())
		took := time.Since(start)
		e.Fake.Struggle(0, 0)
		calls := e.Net.Calls()
		failed := 0
		for _, x := range calls {
			if x.Status >= 500 {
				failed++
			}
		}
		report = append(report, "", fmt.Sprintf("%s: %d requests (%d answered 5xx) in %s; online after: %v; cycle error: %v",
			c.name, len(calls), failed, took.Round(time.Millisecond), e.App.Hub.Online(), err))
		report = append(report, table(calls)...)
		e.Online() // a hub that went offline comes back for the next case
		e.Sync()
		e.Net.Calls()
	}
	t.Log("\n" + strings.Join(report, "\n"))
}
