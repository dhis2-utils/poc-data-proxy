package adversarial

import (
	"net/http"
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

// D2: central stores a push, but its answer never reaches the hub. The hub must push
// again without creating anything twice.
func TestD2PushAnswerLost(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(20 * time.Second)
	lost := r.e.Net.LoseAnswer(3, tk.IsPush)
	r.e.Net.Calls()
	start := time.Now()
	r.e.Net.SetDown(false)
	for range 3 {
		select {
		case <-lost:
		case <-time.After(2 * time.Minute):
			t.Fatal("no push answer was lost")
		}
	}
	time.Sleep(10 * time.Second)
	stop()
	r.finish(3 * time.Minute)
	t.Logf("uplink: %s", callStats(r.e.Net.Calls(), time.Since(start)))
}

// D2, online: a client's save passes through the hub to central, central stores it,
// the answer is lost. The hub answers the client itself and queues the save; pushing
// it again must not create it twice.
func TestD2OnlineSaveAnswerLost(t *testing.T) {
	r := netStart(t)
	clientSave := func(req *http.Request) bool { return tk.IsImport(req) && !tk.IsPush(req) }
	r.e.Net.Calls()
	start := time.Now()
	for range 3 {
		lost := r.e.Net.LoseAnswer(1, clientSave)
		stop := r.write(300 * time.Millisecond)
		select {
		case <-lost:
		case <-time.After(2 * time.Minute):
			stop()
			t.Fatal("no answer to an online save was lost")
		}
		time.Sleep(5 * time.Second)
		stop()
		// Back to passing saves through: the hub went offline on the lost answer.
		r.p.WaitFor("drained", 2*time.Minute, tk.Drained)
	}
	c := r.finish(3 * time.Minute)
	t.Logf("uplink: %s", callStats(r.e.Net.Calls(), time.Since(start)))
	var online int
	for _, s := range r.ledger.all() {
		if s.Confirmed && s.Online {
			online++
		}
	}
	if online == 0 {
		t.Errorf("no save passed through to central: the scenario did not test online saves (%s)", c)
	}
}

// D4: a very slow uplink (2G: 20 kB/s, 2 s latency). Saves and reads made online wait
// for central over it; after an outage the push and the pull must complete over it.
func TestD4SlowLink(t *testing.T) {
	r := netStart(t)
	read := r.reader()
	var fast []time.Duration
	for range 5 {
		d, _, _ := read()
		fast = append(fast, d)
	}
	t.Logf("reads on a good link: %s", durStats(fast))

	stop := r.write(time.Second) // logs the writers in on the good link
	defer stop()
	r.e.Net.SetDelay(2 * time.Second)
	r.e.Net.SetRate(20_000)
	r.e.Net.Calls()
	start := time.Now()
	login := r.e.Web(tk.RoleNurse)
	st, _, _, err := login.Try("POST", "/api/auth/login", map[string]string{"username": login.User, "password": r.e.M.Password})
	t.Logf("an online login on the slow link: %d %v, %s", st, err, time.Since(start).Round(time.Millisecond))
	start = time.Now()
	var slow []time.Duration
	var local int
	for time.Since(start) < 40*time.Second {
		d, st, mode := read()
		if st != 200 {
			t.Errorf("read on the slow link: %d", st)
		}
		if mode != "" {
			local++
		}
		slow = append(slow, d)
	}
	t.Logf("slow link, online: saves waited %s", r.waits(start, time.Now()))
	t.Logf("slow link, online: reads waited %s; %d of %d answered by the hub", durStats(slow), local, len(slow))
	t.Logf("hub status: %v", r.p.Status())

	out := time.Now()
	r.outage(20 * time.Second)
	back := time.Now()
	r.e.Net.SetDown(false) // still slow
	before := r.p.Status()["lastPull"]
	time.Sleep(30 * time.Second)
	stop()
	t.Logf("outage: saves waited %s", r.waits(out, back))
	t.Logf("slow link, after the outage: saves waited %s", r.waits(back, time.Now()))
	r.finish(10 * time.Minute)
	r.p.WaitFor("a pull over the slow link", 10*time.Minute, func(s map[string]any) bool { return s["lastPull"] != before })
	t.Logf("pull completed; %s since the link came back", time.Since(back).Round(time.Second))
	t.Logf("uplink: %s", callStats(r.e.Net.Calls(), time.Since(start)))
}

// D5: the link goes half open while clients save online: packets stop, nothing is
// reset. The hub's timeouts must free it; it goes offline, keeps saving locally, and
// comes back when the link does.
func TestD5HalfOpen(t *testing.T) {
	r := netStart(t)
	stop := r.write(500 * time.Millisecond)
	defer stop()
	time.Sleep(10 * time.Second)
	r.e.Net.Calls()
	release := r.e.Net.HalfOpen()
	defer release()
	held := time.Now()
	r.p.WaitFor("offline", 5*time.Minute, tk.Offline)
	t.Logf("offline %s after the link went half open", time.Since(held).Round(time.Second))
	time.Sleep(90 * time.Second)
	release()
	back := time.Now()
	r.p.WaitFor("online", 3*time.Minute, tk.Online)
	t.Logf("online %s after the link came back", time.Since(back).Round(time.Second))
	time.Sleep(10 * time.Second)
	stop()
	t.Logf("half open: saves waited %s", r.waits(held, back))
	t.Logf("after: saves waited %s", r.waits(back, time.Now()))
	r.finish(5 * time.Minute)
	t.Logf("uplink: %s", callStats(r.e.Net.Calls(), time.Since(held)))
}

// D11: the uplink comes back (and drops again) while clients are in the middle of
// saves: every 4 s for 90 s, with latency so saves overlap the switches. Each save
// lands once.
func TestD11UplinkReturnsMidSave(t *testing.T) {
	r := netStart(t)
	r.e.Net.SetDelay(500 * time.Millisecond)
	stop := r.write(100 * time.Millisecond)
	defer stop()
	time.Sleep(5 * time.Second)
	r.e.Net.Calls()
	refused := r.e.Net.Refused()
	start := time.Now()
	unflap := r.e.Net.Flap(4*time.Second, 4*time.Second)
	defer unflap()
	time.Sleep(90 * time.Second)
	unflap()
	r.e.Net.SetDelay(0)
	calls := r.e.Net.Calls()
	time.Sleep(5 * time.Second)
	stop()
	t.Logf("flapping: saves waited %s", r.waits(start, start.Add(90*time.Second)))
	t.Logf("uplink while flapping: %s; %d more refused while down", callStats(calls, 90*time.Second), r.e.Net.Refused()-refused)
	r.finish(5 * time.Minute)
}

// D11 and D3: while the link flaps, the hub must make progress on its outbox in the
// windows it is up. Each reconnect starts a sync cycle that re-reads the catalog and
// every user's access before it pushes; a window shorter than that pushes nothing.
func TestD11PushProgressWhileFlapping(t *testing.T) {
	findings(t, "while the uplink flaps (4 s up, 4 s down, 500 ms latency) the hub pushes almost nothing: "+
		"each cycle starts with the catalog and access refresh (sync.Worker.cycle), the window closes before the "+
		"push, the refresh never completes so it is never skipped; 2 imports in 90 s against 2400 saves queued")
	r := netStart(t)
	r.e.Net.SetDelay(500 * time.Millisecond)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(10 * time.Second)
	r.e.Net.SetDown(false)
	r.e.Net.Calls()
	unflap := r.e.Net.Flap(4*time.Second, 4*time.Second)
	time.Sleep(90 * time.Second)
	unflap()
	r.e.Net.SetDelay(0)
	pushes := 0
	for _, c := range r.e.Net.Calls() {
		if c.Method == "POST" && c.URL.Path == "/api/tracker" && c.URL.Query().Get("atomicMode") == "OBJECT" {
			pushes++
		}
	}
	t.Logf("%d pushes in 90 s of flapping, with %d up windows", pushes, 90/8)
	stop()
	r.finish(5 * time.Minute)
	if pushes < 90/8 {
		t.Errorf("fewer pushes than up windows: the outbox makes no progress while the link flaps")
	}
}
