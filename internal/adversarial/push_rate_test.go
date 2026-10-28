package adversarial

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestPushOverSlowUplink measures how fast the hub pushes a backlog to central once the
// uplink returns, over a link of a given speed and latency, with or without staff
// saving meanwhile (docs/PERFORMANCE.md, "Push over a slow uplink"). It is a
// measurement, not a check, and runs only with HUB_PUSH=1:
//
//	HUB_PUSH_LINK     "bytes/s,latency" each way, e.g. "375000,100ms" (default: unlimited)
//	HUB_PUSH_BACKLOG  objects saved offline before the link returns (1500)
//	HUB_PUSH_SAVES    saves a second the clients make while the hub pushes (0: none)
//	HUB_PUSH_WATCH    how long to watch the push at most (10m)
func TestPushOverSlowUplink(t *testing.T) {
	if os.Getenv("HUB_PUSH") == "" {
		t.Skip("a measurement: HUB_PUSH=1 runs it")
	}
	var rate int
	var delay time.Duration
	if l := os.Getenv("HUB_PUSH_LINK"); l != "" {
		a, b, _ := strings.Cut(l, ",")
		rate, _ = strconv.Atoi(a)
		delay, _ = time.ParseDuration(b)
	}
	backlog := 1500
	if v, err := strconv.Atoi(os.Getenv("HUB_PUSH_BACKLOG")); err == nil {
		backlog = v
	}
	saves, _ := strconv.ParseFloat(os.Getenv("HUB_PUSH_SAVES"), 64)
	watch := 10 * time.Minute
	if d, err := time.ParseDuration(os.Getenv("HUB_PUSH_WATCH")); err == nil {
		watch = d
	}

	r := netStart(t)
	r.e.Net.SetRate(rate)
	r.e.Net.SetDelay(delay)
	writers := len(r.writers())
	t.Logf("link: %d bytes/s each way (0: unlimited), latency %s; %d writers", rate, delay, writers)

	// The backlog: the writers save as fast as the hub answers while the link is down.
	fill := r.write(0)
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	r.p.WaitFor(fmt.Sprintf("%d objects pending", backlog), 15*time.Minute, func(s map[string]any) bool {
		n, _ := s["pending"].(float64)
		return int(n) >= backlog
	})
	fill()

	// The load while the hub pushes: each writer saves, then waits. Saves answered by
	// the hub take milliseconds, so the pause sets the rate.
	stopLoad := func() {}
	if saves > 0 {
		stopLoad = r.write(time.Duration(float64(writers) / saves * float64(time.Second)))
		defer stopLoad()
		time.Sleep(5 * time.Second) // the writers log in while the link is down
	}
	pending0 := pendingOf(r.p.Status())
	r.e.Net.Calls()
	t0 := time.Now()
	r.e.Net.SetDown(false)

	type sample struct {
		at      time.Duration
		pending int
		online  bool
	}
	var series []sample
	var online, drained time.Duration
	for time.Since(t0) < watch {
		s := r.p.Status()
		if s == nil {
			time.Sleep(time.Second)
			continue
		}
		at := time.Since(t0)
		series = append(series, sample{at, pendingOf(s), tk.Online(s)})
		if online == 0 && tk.Online(s) {
			online = at
		}
		if tk.Drained(s) {
			drained = at
			break
		}
		time.Sleep(time.Second)
	}
	end := time.Since(t0)
	stopLoad()
	calls := r.e.Net.Calls()

	// What came in while the hub pushed, from the clients' ledger: a registration is
	// three objects (person, enrollment, event), an update one.
	var inSaves, inObjects int
	for _, s := range r.ledger.all() {
		if s.At.Before(t0) || s.At.After(t0.Add(end)) || !s.Confirmed || s.Online {
			continue
		}
		inSaves++
		inObjects += map[bool]int{true: 1, false: 3}[s.Update]
	}
	pendingEnd := series[len(series)-1].pending
	pushedObjects := pending0 + inObjects - pendingEnd

	var pushes []tk.Call
	other := map[string]int{}
	for _, c := range calls {
		if c.Method == "POST" && c.URL.Path == "/api/tracker" && c.URL.Query().Get("atomicMode") == "OBJECT" {
			pushes = append(pushes, c)
		} else {
			other[kindOf(c)]++
		}
	}
	slices.SortFunc(pushes, func(a, b tk.Call) int { return a.At.Compare(b.At) })

	t.Logf("backlog %d objects; link back at 0 s, hub online at %.1f s", pending0, online.Seconds())
	if drained > 0 {
		t.Logf("DRAINED at %.1f s", drained.Seconds())
	} else {
		t.Logf("NOT DRAINED in %s: %d objects pending", end.Round(time.Second), pendingEnd)
	}
	t.Logf("incoming while pushing: %d saves (%.2f/s), %d objects (%.2f/s)", inSaves, float64(inSaves)/end.Seconds(),
		inObjects, float64(inObjects)/end.Seconds())
	t.Logf("pushed: %d entries (%.2f/s), %d objects (%.2f/s) in %.1f s", len(pushes), float64(len(pushes))/end.Seconds(),
		pushedObjects, float64(pushedObjects)/end.Seconds(), end.Seconds())
	for _, s := range series {
		if int(s.at.Seconds())%30 == 0 || s.at == series[len(series)-1].at {
			t.Logf("  %4.0f s: %5d pending, online=%v", s.at.Seconds(), s.pending, s.online)
		}
	}
	if len(pushes) == 0 {
		return
	}

	// Where the time went: before the first push, in central's answers (link latency
	// added per request), and between pushes.
	var durs, gaps []time.Duration
	var sent, got []int64
	var inPush, between time.Duration
	nBetween := 0
	for i, c := range pushes {
		durs = append(durs, c.Dur)
		sent = append(sent, c.Sent)
		got = append(got, c.Bytes)
		if i > 0 {
			prev := pushes[i-1]
			g := c.At.Sub(prev.At.Add(prev.Dur)) - delay // the hub's own time between two pushes
			gaps = append(gaps, g)
			if g > time.Second {
				between += g
				nBetween++
			} else {
				inPush += g
			}
		}
	}
	first := pushes[0].At.Add(-delay).Sub(t0)
	last := pushes[len(pushes)-1]
	span := last.At.Add(last.Dur).Sub(t0)
	var central time.Duration
	for _, d := range durs {
		central += d
	}
	t.Logf("per push: central answered in %s; request %s bytes, answer %s bytes", durStats(durs), int64Stats(sent), int64Stats(got))
	t.Logf("time from the link's return to the last push's answer, %.1f s: %.1f s before the first push, "+
		"%.1f s latency (%d × %s), %.1f s central's answers, %.1f s the hub between pushes of one cycle (%s), "+
		"%.1f s in %d waits over 1 s (between cycles)",
		span.Seconds(), first.Seconds(), (time.Duration(len(pushes)) * delay).Seconds(), len(pushes), delay,
		central.Seconds(), inPush.Seconds(), durStats(gaps), between.Seconds(), nBetween)
	t.Logf("other requests to central meanwhile: %v", other)
	t.Logf("hub's sync cycles: %s", cyclesIn(r.p.Log, t0, t0.Add(end)))
}

func pendingOf(s map[string]any) int {
	n, _ := s["pending"].(float64)
	return int(n)
}

func int64Stats(v []int64) string {
	if len(v) == 0 {
		return "none"
	}
	s := slices.Clone(v)
	slices.Sort(s)
	return fmt.Sprintf("min %d, median %d, p90 %d, max %d", s[0], s[len(s)/2], s[len(s)*9/10], s[len(s)-1])
}

// kindOf names a request to central by its path, for counting.
func kindOf(c tk.Call) string {
	p := c.URL.Path
	if i := strings.Index(p, "/api/"); i >= 0 {
		p = p[i+5:]
	}
	parts := strings.SplitN(p, "/", 3)
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return c.Method + " " + strings.Join(parts, "/")
}

// cyclesIn summarises the hub's "sync cycle done" lines between from and to.
func cyclesIn(log string, from, to time.Time) string {
	f, err := os.Open(log)
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	var ds []time.Duration
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		if !strings.Contains(l, `msg="sync cycle done"`) {
			continue
		}
		ts, _, _ := strings.Cut(strings.TrimPrefix(l, "time="), " ")
		at, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil || at.Before(from) || at.After(to) {
			continue
		}
		_, ms, _ := strings.Cut(l, " ms=")
		ms, _, _ = strings.Cut(ms, " ")
		n, _ := strconv.Atoi(ms)
		ds = append(ds, time.Duration(n)*time.Millisecond)
	}
	return durStats(ds)
}
