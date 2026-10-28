package adversarial

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// perMinute counts the requests the hub sent to central in each minute since start,
// and lists the paths of the busiest minute.
func perMinute(calls []tk.Call, at []time.Time, start time.Time) ([]int, string) {
	var n []int
	paths := map[int]map[string]int{}
	for i, c := range calls {
		m := int(at[i].Sub(start) / time.Minute)
		for len(n) <= m {
			n = append(n, 0)
		}
		n[m]++
		if paths[m] == nil {
			paths[m] = map[string]int{}
		}
		paths[m][c.Method+" "+c.URL.Path]++
	}
	busiest := 0
	for m := range n {
		if n[m] > n[busiest] {
			busiest = m
		}
	}
	return n, fmt.Sprint(paths[busiest])
}

// H1: central down for maintenance. The broker stops the instance (Tomcat and its
// database); the hub's uplink still works, so the hub reaches the address and gets
// 502 from the proxy, as from nginx in front of a stopped Tomcat. Must hold: the hub
// offline after two pings, staff keep working, no storm, and the outbox drains when
// central is back.
func TestH1CentralDownForMaintenance(t *testing.T) {
	r := netStart(t)
	read := r.reader()
	stop := r.write(500 * time.Millisecond)
	defer stop()
	time.Sleep(10 * time.Second)

	r.e.Net.Calls()
	var calls []tk.Call
	var at []time.Time
	collect := func() { // Calls are timed by when they are collected: every second
		for _, c := range r.e.Net.Calls() {
			calls, at = append(calls, c), append(at, time.Now())
		}
	}
	down := time.Now()
	r.e.StopCentral()
	stopped := time.Now()
	r.p.WaitFor("offline", 2*time.Minute, tk.Offline)
	t.Logf("central stopped in %s; the hub offline %s after the stop began, %s after it ended",
		stopped.Sub(down).Round(time.Second), time.Since(down).Round(time.Second), time.Since(stopped).Round(100*time.Millisecond))

	const outage = 10 * time.Minute
	var reads []time.Duration
	failedReads := 0
	for time.Since(stopped) < outage {
		d, st, _ := read()
		reads = append(reads, d)
		if st != 200 {
			failedReads++
		}
		collect()
		time.Sleep(time.Second)
	}
	collect()
	byMinute, busiest := perMinute(calls, at, stopped)
	t.Logf("central down %s: saves waited %s; reads %s, %d not 200", outage, r.waits(stopped, time.Now()), durStats(reads), failedReads)
	t.Logf("requests to central per minute while down: %v; busiest minute: %s", byMinute, busiest)
	t.Logf("hub status while down: %v", r.p.Status())

	up := time.Now()
	r.e.StartCentral()
	started := time.Now()
	r.p.WaitFor("online", 3*time.Minute, tk.Online)
	t.Logf("central started in %s; the hub online %s after it answered", started.Sub(up).Round(time.Second), time.Since(started).Round(100*time.Millisecond))
	time.Sleep(10 * time.Second)
	stop()
	collect()
	var since []tk.Call
	for i, c := range calls {
		if !at[i].Before(up) {
			since = append(since, c)
		}
	}
	t.Logf("from central's start until the outbox drained: %s", callStats(since, time.Since(up)))
	r.finish(5 * time.Minute)

	if failedReads > 0 {
		t.Errorf("%d reads failed while central was down: staff cannot keep working", failedReads)
	}
	for m, n := range byMinute[:len(byMinute)-1] {
		if n > 2*60/2+10 { // a ping every 2 s, and the odd sync attempt
			t.Errorf("I7: %d requests to central in minute %d of the outage", n, m+1)
		}
	}
}

// H3, the part the sandbox can do: central now runs a major this hub release does not
// support (central's version, as the hub reads it, says 2.44). Must hold: the hub says
// "needs upgrade" and passes clients through to central; its queued saves are kept,
// not pushed under rules it does not know, and pushed once the hub supports central
// again (here: central's version back to what it is, as after the hub's upgrade).
// The other part, a hub that supports the new major pushing as before, needs central
// really upgraded (see the plan's Progress).
func TestH3CentralVersionNotSupported(t *testing.T) {
	r := netStart(t)
	read := r.reader()
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(20 * time.Second)

	r.e.Net.SetVersion("2.44.0")
	r.e.Net.SetDown(false)
	r.e.Net.Calls()
	start := time.Now()
	queued := r.p.WaitFor("needs upgrade", 2*time.Minute, func(s map[string]any) bool { return s["needsUpgrade"] == true && tk.Online(s) })["pending"]
	var reads []time.Duration
	answered := map[string]int{}
	for time.Since(start) < 40*time.Second {
		d, st, mode := read()
		reads = append(reads, d)
		answered[fmt.Sprintf("%d %s", st, mode)]++
		time.Sleep(500 * time.Millisecond)
	}
	s := r.p.Status()
	calls := r.e.Net.Calls()
	pushes := 0
	for _, c := range calls {
		if c.Method == "POST" && c.URL.Path == "/api/tracker" && c.URL.Query().Get("atomicMode") == "OBJECT" {
			pushes++
		}
	}
	t.Logf("unsupported, online: reads %s, answered %v (status and X-Hub-Mode); saves waited %s", durStats(reads), answered,
		r.waits(start, time.Now()))
	t.Logf("unsupported, online: %v queued before, %v pending now, %d pushes; uplink %s; lastError %v", queued, s["pending"], pushes,
		callStats(calls, time.Since(start)), s["lastError"])
	if pushes > 0 {
		t.Errorf("the hub pushed %d times to a central it does not support", pushes)
	}
	if s["pending"] != queued {
		t.Errorf("pending saves %v, %v before: the queue changed while the hub does not support central", s["pending"], queued)
	}
	for k := range answered {
		if k != "200 " {
			t.Errorf("online reads must pass through to central: %d answered %q", answered[k], k)
		}
	}

	// Offline as well: the hub cannot answer for a central it does not support.
	off := time.Now()
	r.outage(15 * time.Second)
	c := r.e.Web(tk.RoleNurse)
	c.Login()
	st, h, b, _ := c.Try("GET", "/api/tracker/trackedEntities?program="+r.prog.ID+"&orgUnits="+r.e.M.Facility.ID+"&pageSize=5", nil)
	t.Logf("unsupported, offline: a read answers %d %q %.200s; saves waited %s", st, h.Get("X-Hub-Mode"), b, r.waits(off, time.Now()))

	// The hub is upgraded: it supports central again.
	r.e.Net.SetVersion("")
	back := time.Now()
	r.e.Net.SetDown(false)
	r.p.WaitFor("supported", 2*time.Minute, func(s map[string]any) bool { return s["needsUpgrade"] == nil && tk.Online(s) })
	time.Sleep(5 * time.Second)
	stop()
	t.Logf("supported again %s after the uplink returned", time.Since(back).Round(time.Second))
	r.finish(5 * time.Minute)
}

// pulls waits for n pulls to end after now.
func (r *netRun) pulls(n int) {
	r.t.Helper()
	for range n {
		before := r.p.Status()["lastPull"]
		r.p.WaitFor("a pull", 3*time.Minute, func(s map[string]any) bool { return s["lastPull"] != before })
	}
}

// centralPerson registers a person with one event of value v on central directly, as
// staff elsewhere would. Not in the ledger, and not named with the run's tag.
func (r *netRun) centralPerson(v string) netSave {
	r.t.Helper()
	pr := r.e.M.NewPerson(r.prog, "Zqcen"+strings.ToLower(tk.UID()[:6]))
	s := netSave{TE: pr.TE, Enr: pr.Enrollment, Event: tk.UID(), Value: v, At: time.Now()}
	te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
	te["enrollments"].([]any)[0].(map[string]any)["events"] = []any{r.event(s)}
	r.e.CentralImport(pr.Payload)
	return s
}

// centralEdit changes the event's value on central directly.
func (r *netRun) centralEdit(s netSave, v string) {
	r.t.Helper()
	s.Value = v
	r.e.CentralImport(map[string]any{"events": []any{r.event(s)}})
}

// offlineValues reads events through the hub, offline, as a nurse: each event's value
// of r.de, or its status if the hub does not answer 200.
func (r *netRun) offlineValues(events ...string) []string {
	r.t.Helper()
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	defer func() {
		r.e.Net.SetDown(false)
		r.p.WaitFor("online", 2*time.Minute, tk.Online)
	}()
	c := r.e.Web(tk.RoleNurse)
	c.Login()
	var out []string
	for _, ev := range events {
		st, _, b, err := c.Try("GET", "/api/tracker/events/"+ev+"?fields=event,dataValues", nil)
		if err != nil || st != 200 {
			out = append(out, fmt.Sprintf("%d %v", st, err))
			continue
		}
		var o map[string]any
		json.Unmarshal(b, &o)
		out = append(out, value(o, r.de))
	}
	return out
}

// H4: central restored from a backup. The backup is taken while the hub works; the hub
// then pushes its outbox and passes saves through, and pulls an edit made on central;
// then central's database is restored from the backup and central restarted, and
// someone edits on central again. Must hold: what the hub pushed is not lost; its
// watermarks skip nothing.
//
// What the hub sent after the backup is gone from central. The recovery step in
// RECOVERY.md, a resend instruction with since set to the backup's time, sends the
// hub's outbox entries again (FINDINGS.md, H4). Saves passed through online are not
// in the outbox: they are counted apart. Two things are logged, not checked: the hub
// does not notice central went back in time, and an edit made on central and lost in
// the restore stays on the hub.
func TestH4CentralRestoredFromBackup(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	time.Sleep(15 * time.Second)
	a, b := r.centralPerson("41"), r.centralPerson("41")
	r.pulls(2)

	t0 := time.Now() // a save confirmed during the dump may be in it or not
	dump := r.e.BackupCentral()
	t.Logf("backup taken from %s to %s", t0.Format("15:04:05"), time.Now().Format("15:04:05"))
	r.outage(20 * time.Second)
	r.e.Net.SetDown(false)
	r.p.WaitFor("drained", 3*time.Minute, tk.Drained)
	time.Sleep(10 * time.Second) // saves pass through to central
	r.centralEdit(a, "42")
	r.pulls(2)
	stop()
	r.p.WaitFor("drained", 3*time.Minute, tk.Drained)
	t.Logf("before the restore, the hub offline shows a %v", r.offlineValues(a.Event))

	restore := time.Now()
	r.e.RestoreCentral(dump)
	t.Logf("central restored to %s and restarted in %s", t0.Format("15:04:05"), time.Since(restore).Round(time.Second))
	r.p.WaitFor("online", 3*time.Minute, tk.Online)
	r.centralEdit(b, "43")
	r.pulls(2)
	r.p.WaitFor("drained", 3*time.Minute, tk.Drained)
	t.Logf("ledger: %s", r.ledger.summary())
	lost := r.lostSince(t0)
	t.Logf("after the restore, registrations confirmed but not on central: %s", lost)
	if len(lost.byHub) > 0 {
		t.Logf("the hub, offline, for the first of them: %v", first(r.offlineValues(lost.byHub...)))
	}
	t.Logf("hub log lines about the restore: %d", r.p.Grep("back in time"))

	// The recovery step: a resend instruction with since, the backup's time.
	r.e.Net.Calls()
	retries := map[string]any{"retries": []any{map[string]any{"id": "adv-h4-" + tk.UID(), "since": t0.UTC().Format(time.RFC3339),
		"at": time.Now().UTC().Format(time.RFC3339), "by": "adversarial"}}}
	if err := r.e.Central.JSON("PUT", "/api/dataStore/facility-hub-admin/retries", retries, nil); err != nil {
		if err := r.e.Central.JSON("POST", "/api/dataStore/facility-hub-admin/retries", retries, nil); err != nil {
			t.Fatal(err)
		}
	}
	r.pulls(3)
	r.p.WaitFor("drained", 3*time.Minute, tk.Drained)
	r.pulls(1)
	pushes := 0
	for _, c := range r.e.Net.Calls() {
		if c.Method == "POST" && c.URL.Path == "/api/tracker" && c.URL.Query().Get("atomicMode") == "OBJECT" {
			pushes++
		}
	}
	c := r.check()
	t.Logf("central after the resend: %s", c)
	left := r.lostSince(t0)
	t.Logf("after the resend (%d pushes), registrations confirmed but not on central: %s", pushes, left)
	if len(left.byHub) > 0 {
		t.Errorf("I1: %d registrations the hub confirmed offline are not on central after the resend", len(left.byHub))
	}
	if len(left.before) > 0 {
		t.Errorf("I1: %d registrations confirmed before the backup are not on central", len(left.before))
	}
	if len(left.passed) > 0 {
		t.Logf("%d registrations passed through to central online after the backup are lost: they were never in the outbox", len(left.passed))
	}
	if len(lost.byHub) > 0 {
		vals := r.offlineValues(lost.byHub...)
		hidden := 0
		for _, v := range vals {
			if strings.Contains(v, " ") { // a status, not a value
				hidden++
			}
		}
		t.Logf("the hub, offline, for the resent ones: %v", first(vals))
		if hidden > 0 {
			t.Errorf("%d of the %d resent registrations are not shown offline", hidden, len(vals))
		}
	}
	vals := r.offlineValues(a.Event, b.Event)
	t.Logf("event edited on central after the backup (central now %q): the hub shows %q; edited after the restore (central %q): the hub shows %q",
		value(r.central("events", a.Event, "event,dataValues"), r.de), vals[0], value(r.central("events", b.Event, "event,dataValues"), r.de), vals[1])
	if vals[1] != "43" {
		t.Errorf("the hub holds %q for an event edited on central after the restore (43): the pull skipped it", vals[1])
	}
}

// missing is the events of registrations confirmed but not on central: confirmed before
// t0, after it by the hub (from its outbox), and after it by central (passed through).
type missing struct{ before, byHub, passed []string }

func (l missing) String() string {
	return fmt.Sprintf("%d made before the backup, %d after it confirmed by the hub, %d after it passed through to central",
		len(l.before), len(l.byHub), len(l.passed))
}

func (r *netRun) lostSince(t0 time.Time) missing {
	var l missing
	for _, s := range r.ledger.all() {
		if !s.Confirmed || s.Update || r.central("events", s.Event, "event") != nil {
			continue
		}
		switch {
		case s.At.Before(t0):
			l.before = append(l.before, s.Event)
		case s.Online:
			l.passed = append(l.passed, s.Event)
		default:
			l.byHub = append(l.byHub, s.Event)
		}
	}
	return l
}
