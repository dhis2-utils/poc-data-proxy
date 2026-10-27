package adversarial

import (
	"os"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// outage cuts the uplink for d while the writers save, so the hub holds an outbox.
func (r *netRun) outage(d time.Duration) {
	r.t.Helper()
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	time.Sleep(d)
}

// findings skips a scenario that fails on a known finding, unless HUB_ADV_FINDINGS is
// set: the scenario documents the bug until it is fixed.
func findings(t *testing.T, finding string) {
	if os.Getenv("HUB_ADV_FINDINGS") == "" {
		t.Skip("FINDING: " + finding)
	}
}

// D1: the uplink drops in the middle of a push, while the request is on its way:
// central never sees it whole, the hub must send it again.
func TestD1UplinkDropsMidPushRequest(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(20 * time.Second)
	cuts := r.e.Net.CutRequest(3, tk.IsPush)
	r.e.Net.Calls()
	start := time.Now()
	r.e.Net.SetDown(false)
	for range 3 {
		select {
		case <-cuts:
		case <-time.After(2 * time.Minute):
			t.Fatal("no push was cut on its way")
		}
	}
	time.Sleep(10 * time.Second)
	stop()
	r.finish(3 * time.Minute)
	t.Logf("uplink: %s", callStats(r.e.Net.Calls(), time.Since(start)))
}

// D1, the other half: the uplink drops while central's answer to a push comes back.
// Central stored the objects; the hub has half a report.
func TestD1UplinkDropsMidPushAnswer(t *testing.T) {
	findings(t, "a push whose answer is cut midway is read as central's refusal: push.go ignores the "+
		"error reading the body, TrackerResult fails on the half report, and every object of the entry is "+
		"rejected with \"central returned 200\" although central stored them; later edits of them are "+
		"\"blocked by\" the rejected parent and never reach central (aggregate.go has the same read)")
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(20 * time.Second)
	cuts := r.e.Net.CutAnswer(2, tk.IsPush)
	r.e.Net.Calls()
	start := time.Now()
	r.e.Net.SetDown(false)
	for range 2 {
		select {
		case <-cuts:
		case <-time.After(2 * time.Minute):
			t.Fatal("no push answer was cut")
		}
	}
	time.Sleep(10 * time.Second)
	stop()
	r.finish(3 * time.Minute)
	t.Logf("uplink: %s", callStats(r.e.Net.Calls(), time.Since(start)))
	if n := r.p.Grep(`reason="central returned 200"`); n > 0 {
		t.Errorf("%d objects central stored were rejected by the hub", n)
	}
}
