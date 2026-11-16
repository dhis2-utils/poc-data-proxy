package adversarial

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/hubaccount"
	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// M1, the sandbox part: a hub in its own process against the real DHIS2 in
// HUB_MANIFEST for HUB_SOAK (e.g. 10h), with staff saving and reading through it at a
// few times a busy HC IV's daily rate, the uplink cut every ~2 hours for 10–30 minutes,
// the hub killed (kill -9, a power cut) every ~3 hours, and its housekeeping (sync,
// access refresh, ownership check, full pull, retention, token renewal, session
// expiry) at shortened intervals so each runs several times. Every 15 minutes the
// hub's process, data folder, outbox and the clients' latencies are sampled; at the
// end every confirmed save is looked up on central (I1, I2) and the trends are
// summarised. Shorter durations scale the schedule down (a 20m trial has an outage
// every 4 minutes). Results: testresults/soak/<tag>/.
//
//	HUB_SOAK=10h HUB_SOAK_PG='host=… user=dhis password=dhis dbname=dhis2' \
//	  HUB_MANIFEST=… go test ./internal/adversarial -run TestM1Soak -timeout 14h -v
//
// HUB_SOAK_PG (optional) lets the sampler count API tokens on central's database.
// HUB_SOAK_CHECK=<results folder> only re-runs the final check of a soak's ledger.
func TestM1Soak(t *testing.T) {
	if dir := os.Getenv("HUB_SOAK_CHECK"); dir != "" {
		soakRecheck(t, dir)
		return
	}
	d, err := time.ParseDuration(os.Getenv("HUB_SOAK"))
	if err != nil || d <= 0 {
		t.Skip("HUB_SOAK=<duration> runs the soak")
	}
	pl := soakPlanFor(d)
	r := netPrepare(t)
	e := r.e
	e.ProcConfig = pl.config()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pprof := ln.Addr().String()
	ln.Close()
	t.Setenv("HUB_PPROF", pprof) // the hub process inherits it

	// Retention on (the hub runs it every cycle; with a window it prunes), and the
	// token lifetime the hub starts with: signed, so set after set-up (a hub not approved
	// uses the defaults) and before the first pull.
	e.SetupOnly(tk.RoleAdmin)
	e.CentralSetRetention(`{"defaultMonths": 12}`) // gives the hub tk.TestMoveKey
	hubID := e.App.Hub.HubID()
	soakSetTokens(t, e, hubID, 90)
	t.Cleanup(func() { e.Central.Do("DELETE", "/api/dataStore/facility-hub-config/tokens", nil) })
	e.Sync()
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if _, ok := e.M.User(role); ok {
			e.Web(role).Login()
		}
	}
	e.Sync()
	r.p = e.Detach()
	t.Cleanup(r.keepLog)
	r.p.WaitFor("online", 2*time.Minute, tk.Online)

	out := filepath.Join("..", "..", "testresults", "soak", time.Now().Format("20060102-1504")+"-"+r.tag)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	so := &soak{r: r, pl: pl, out: out, pprof: pprof, start: time.Now(), hubID: hubID}
	so.writeJSON("run.json", map[string]any{"tag": r.tag, "program": r.prog.ID, "stage": r.stage, "dataElement": r.de,
		"duration": d.String(), "plan": pl.describe(), "config": e.ProcConfig, "central": e.M.CentralURL, "hubLog": r.p.Log})
	t.Logf("run tag %s; hub log %s; results %s", r.tag, r.p.Log, out)
	t.Logf("plan: %s", pl.describe())
	t.Cleanup(func() { so.writeLedger() })

	stopW := so.write()
	stopR := so.read()
	events := pl.schedule()
	for _, ev := range events {
		t.Logf("planned: %s at %s", ev.What, fmtDur(ev.At))
	}
	so.sample() // the baseline
	nextSample := pl.SampleEvery
	for i := 0; ; {
		el := time.Since(so.start)
		if el >= d {
			break
		}
		if i < len(events) && el >= events[i].At {
			so.act(events[i])
			i++
			continue
		}
		if el >= nextSample {
			so.sample()
			nextSample += pl.SampleEvery
			continue
		}
		time.Sleep(time.Second)
	}
	stopW()
	stopR()
	e.Net.SetDown(false)
	so.sample()
	so.writeLedger()
	so.writeSamples()

	// Every confirmed save on central, nothing twice (I1, I2): r.finish waits for the
	// outbox to drain, then reads central.
	c := r.finish(30 * time.Minute)
	so.sample()
	so.writeSamples()
	so.summary(c)
}

// soakPlan is the schedule, scaled to the duration: a 10h soak has its outages every
// 2 hours, a 20m trial every 4 minutes.
type soakPlan struct {
	D, SampleEvery, OutageEvery, OutageMin, OutageMax, KillEvery, TokenEvery, Down time.Duration
	Pause, ReadEvery                                                               time.Duration
	s                                                                              float64
}

func soakPlanFor(d time.Duration) soakPlan {
	s := min(1, d.Hours()/10)
	sc := func(x time.Duration) time.Duration { return time.Duration(float64(x) * s) }
	return soakPlan{D: d, s: s, SampleEvery: sc(15 * time.Minute), OutageEvery: sc(2 * time.Hour),
		OutageMin: sc(10 * time.Minute), OutageMax: sc(30 * time.Minute), KillEvery: sc(3 * time.Hour),
		TokenEvery: sc(150 * time.Minute), Down: 20 * time.Second,
		// The pace (not scaled): 3 writers, each a save every ~10 s, ~0.3 saves/s, about
		// 4 times a busy HC IV's ~3 000 saves over a 12-hour day; one reader every 3 s.
		Pause: 10 * time.Second, ReadEvery: 3 * time.Second}
}

// config is the hub's housekeeping at about 4 times its field cadence (sync, access,
// ownership, sessions), and the weekly full pull every 2 hours.
func (pl soakPlan) config() []string {
	sc := func(x, floor time.Duration) string { return max(floor, time.Duration(float64(x)*pl.s)).String() }
	return []string{
		"sync_interval: " + sc(5*time.Minute, 30*time.Second),
		"ping_interval: 15s",
		"ping_timeout: 5s",
		"reconnect_delay: " + sc(time.Minute, 5*time.Second),
		"ownership_check_interval: " + sc(15*time.Minute, time.Minute),
		"access_refresh_interval: " + sc(15*time.Minute, time.Minute),
		"full_pull_interval: " + sc(2*time.Hour, 5*time.Minute),
		"session_max: " + sc(3*time.Hour, 10*time.Minute),
		"reserved_pool_size: 20",
	}
}

func (pl soakPlan) describe() string {
	return fmt.Sprintf("%s; samples every %s; an outage of %s–%s every %s; a kill every %s (down %s); token lifetime flipped every %s; "+
		"3 writers pausing %s, a reader every %s", pl.D, pl.SampleEvery, pl.OutageMin, pl.OutageMax, pl.OutageEvery, pl.KillEvery,
		pl.Down, pl.TokenEvery, pl.Pause, pl.ReadEvery)
}

type soakEvent struct {
	At   time.Duration
	What string // down, up, kill, tokens
	N    int    // tokens: expiry days
}

// schedule: outages at 1h, 3h, 5h … (± a sixth of the gap); kills at 2h, 8h, … online,
// and every second one in the middle of an outage, a power cut with an outbox; token
// lifetimes flipped 7 ↔ 365 days so the hub renews its token each time.
func (pl soakPlan) schedule() []soakEvent {
	var evs []soakEvent
	type win struct{ from, to time.Duration }
	var outs []win
	for at := pl.OutageEvery / 2; at < pl.D; at += pl.OutageEvery {
		jit := time.Duration(rand.Int64N(int64(pl.OutageEvery/6)+1)) - pl.OutageEvery/12
		from := at + jit
		to := from + pl.OutageMin + time.Duration(rand.Int64N(int64(pl.OutageMax-pl.OutageMin)+1))
		if to > pl.D-pl.OutageMin {
			break
		}
		outs = append(outs, win{from, to})
		evs = append(evs, soakEvent{At: from, What: "down"}, soakEvent{At: to, What: "up"})
	}
	k := 0
	for at := pl.KillEvery - pl.OutageEvery/2; at < pl.D-pl.OutageMin; at += pl.KillEvery {
		k++
		if k%2 == 0 && len(outs) > 0 {
			best := outs[0]
			for _, o := range outs {
				if absDur(o.from-at) < absDur(best.from-at) {
					best = o
				}
			}
			at = best.from + (best.to-best.from)/3
		}
		evs = append(evs, soakEvent{At: at, What: "kill"})
	}
	days := []int{7, 365}
	for i, at := 0, pl.TokenEvery*3/5; at < pl.D; i, at = i+1, at+pl.TokenEvery {
		evs = append(evs, soakEvent{At: at, What: "tokens", N: days[i%2]})
	}
	slices.SortStableFunc(evs, func(a, b soakEvent) int { return int(a.At - b.At) })
	return evs
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func fmtDur(d time.Duration) string { return d.Round(time.Second).String() }

// soak is a running soak: the run, the reads, the samples.
type soak struct {
	r     *netRun
	pl    soakPlan
	out   string
	pprof string
	start time.Time
	hubID string // the detached hub's, for signing settings

	mu      sync.Mutex
	reads   []soakRead
	samples []soakSample
	events  []string

	lastSample time.Time
	lastPID    int
	lastCPU    float64 // seconds of CPU of lastPID at lastSample
	logOff     int64
}

type soakRead struct {
	At     time.Time
	Kind   string // list, person
	Dur    time.Duration
	Status int
	Local  bool
	Err    string
}

func (so *soak) note(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	so.r.t.Logf("%s %s", fmtDur(time.Since(so.start)), s)
	so.mu.Lock()
	so.events = append(so.events, fmt.Sprintf("%s +%s %s", time.Now().Format("15:04:05"), fmtDur(time.Since(so.start)), s))
	so.mu.Unlock()
}

// act runs one planned fault.
func (so *soak) act(ev soakEvent) {
	r := so.r
	switch ev.What {
	case "down":
		r.e.Net.SetDown(true)
		so.note("uplink down (%v objects pending)", r.p.Status()["pending"])
		go func() { so.note("offline after %s", so.waitFor(3*time.Minute, tk.Offline)) }()
	case "up":
		r.e.Net.SetDown(false)
		so.note("uplink up (%v objects pending)", r.p.Status()["pending"])
		go func() {
			start := time.Now()
			online := so.waitFor(10*time.Minute, tk.Online)
			drained := so.waitFor(30*time.Minute, tk.Drained)
			so.note("online after %s; outbox empty %s later (%s after the uplink returned)", online, drained, fmtDur(time.Since(start)))
		}()
	case "kill":
		pending := r.p.Status()["pending"]
		r.p.Kill()
		so.note("hub killed (uplink down %v, %v objects pending)", r.e.Net.Down(), pending)
		time.Sleep(so.pl.Down)
		so.restart()
	case "tokens":
		soakSetTokens(r.t, r.e, so.hubID, ev.N)
		so.note("token lifetime on central set to %d days", ev.N)
	}
}

// waitFor polls the hub's status until ok, and reports how long it took ("never"
// after timeout); unlike Proc.WaitFor it does not end the soak.
func (so *soak) waitFor(timeout time.Duration, ok func(map[string]any) bool) string {
	start := time.Now()
	for time.Since(start) < timeout {
		if s := so.r.p.Status(); s != nil && ok(s) {
			return fmtDur(time.Since(start))
		}
		time.Sleep(time.Second)
	}
	return "never (" + timeout.String() + ")"
}

// restart starts the killed hub and unlocks it with the first login, as staff arriving
// after a power cut would; retried rather than failing the soak.
func (so *soak) restart() {
	r := so.r
	start := time.Now()
	r.p.Start()
	c := r.e.Web(tk.RoleNurse)
	c.HTTP.Timeout = time.Minute
	var st int
	var err error
	for range 20 {
		st, _, _, err = c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": r.e.M.Password})
		if err == nil && st == 200 {
			break
		}
		time.Sleep(5 * time.Second)
	}
	so.note("hub restarted; login %d %v; unlocked after %s (%s after the start)", st, err,
		so.waitFor(3*time.Minute, func(s map[string]any) bool { return s["locked"] == false }), fmtDur(time.Since(start)))
}

// write starts the writers with a timeout on each request.
func (so *soak) write() (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i, w := range so.r.writers() {
		w.c.HTTP.Timeout = 2 * time.Minute
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(i) * so.pl.Pause / 3) // spread the writers
			for {
				select {
				case <-done:
					return
				default:
				}
				w.once()
				select {
				case <-done:
					return
				case <-time.After(so.pl.Pause/2 + rand.N(so.pl.Pause)):
				}
			}
		}()
	}
	var once sync.Once
	return func() { once.Do(func() { close(done); wg.Wait() }) }
}

// read starts a nurse's reads: a working list (a page of 50 of the program at the
// facility), or one person the writers saved (a dashboard), alternately.
func (so *soak) read() (stop func()) {
	r := so.r
	c := r.e.Web(tk.RoleNurse)
	c.HTTP.Timeout = 2 * time.Minute
	login := func() {
		c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": r.e.M.Password})
	}
	login()
	list := url.Values{"program": {r.prog.ID}, "orgUnits": {r.e.M.Facility.ID}, "fields": {"trackedEntity,attributes"}, "pageSize": {"50"}}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			case <-time.After(so.pl.ReadEvery):
			}
			rd := soakRead{Kind: "list", At: time.Now()}
			path := "/api/tracker/trackedEntities?" + list.Encode()
			if i%2 == 1 {
				var te string
				for _, s := range slices.Backward(r.ledger.all()) {
					if s.Confirmed && !s.Update && s.User == c.User {
						te = s.TE
						break
					}
				}
				if te != "" {
					rd.Kind = "person"
					path = "/api/tracker/trackedEntities/" + te + "?program=" + r.prog.ID + "&fields=*"
				}
			}
			st, h, _, err := c.Try("GET", path, nil)
			if err == nil && (st == 401 || st == 302) {
				login()
				rd.At = time.Now()
				st, h, _, err = c.Try("GET", path, nil)
			}
			rd.Dur, rd.Status = time.Since(rd.At), st
			if err != nil {
				rd.Err = err.Error()
			} else {
				rd.Local = h.Get("X-Hub-Mode") != ""
			}
			so.mu.Lock()
			so.reads = append(so.reads, rd)
			so.mu.Unlock()
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done); wg.Wait() }) }
}

// soakSample is one sample; the client and log figures cover the time since the one
// before.
type soakSample struct {
	At      time.Time `json:"at"`
	Hours   float64   `json:"hours"`
	Running bool      `json:"running"`
	PID     int       `json:"pid"`
	// The hub's process.
	RSSMB      float64 `json:"rssMB"`
	PeakRSSMB  float64 `json:"peakRssMB"` // VmHWM, since the process started
	CPUPct     float64 `json:"cpuPct"`    // share of one core over the window
	Threads    int     `json:"threads"`
	FDs        int     `json:"fds"`
	Goroutines int     `json:"goroutines"`
	HeapInuse  float64 `json:"heapInuseMB"`
	HeapSys    float64 `json:"heapSysMB"`
	// Its data folder.
	DataMB  float64            `json:"dataMB"`
	Files   map[string]float64 `json:"filesMB"` // top-level entries
	LogMB   float64            `json:"logMB"`
	Mode    string             `json:"mode"`
	Locked  bool               `json:"locked"`
	Pending float64            `json:"pending"`
	Refused float64            `json:"refused"`
	// The clients over the window.
	Saves         int            `json:"saves"`
	Confirmed     int            `json:"confirmed"`
	ByCentral     int            `json:"byCentral"`
	SaveP50       float64        `json:"saveP50ms"`
	SaveP90       float64        `json:"saveP90ms"`
	SaveP99       float64        `json:"saveP99ms"`
	SaveMax       float64        `json:"saveMaxms"`
	Reads         int            `json:"reads"`
	ReadErrors    int            `json:"readErrors"`
	ReadsLocal    int            `json:"readsLocal"`
	ListP50       float64        `json:"listP50ms"`
	ListP90       float64        `json:"listP90ms"`
	PersonP50     float64        `json:"personP50ms"`
	PersonP90     float64        `json:"personP90ms"`
	ReadMax       float64        `json:"readMaxms"`
	TotalSaves    int            `json:"totalSaves"`
	TotalConfirm  int            `json:"totalConfirmed"`
	CentralCalls  int            `json:"centralCalls"`
	CentralKinds  map[string]int `json:"centralKinds"`
	CallsFaulted  int            `json:"callsFaulted"`
	Cycles        int            `json:"cycles"`
	CycleP50      float64        `json:"cycleP50ms"`
	CycleMax      float64        `json:"cycleMaxms"`
	CycleErrors   int            `json:"cycleErrors"`
	LogWarn       int            `json:"logWarn"`
	LogError      int            `json:"logError"`
	LogNotes      map[string]int `json:"logNotes"`
	CentralTokens string         `json:"centralTokens,omitempty"`
}

// sample records one sample, and writes the samples so far.
func (so *soak) sample() {
	r := so.r
	now := time.Now()
	s := soakSample{At: now, Hours: now.Sub(so.start).Hours(), Files: map[string]float64{}, CentralKinds: map[string]int{}, LogNotes: map[string]int{}}
	since := so.lastSample
	if since.IsZero() {
		since = so.start
	}
	if pid := r.p.Pid(); pid > 0 {
		s.Running, s.PID = true, pid
		st, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		for _, ln := range strings.Split(string(st), "\n") {
			f := strings.Fields(ln)
			if len(f) < 2 {
				continue
			}
			n, _ := strconv.ParseFloat(f[1], 64)
			switch f[0] {
			case "VmRSS:":
				s.RSSMB = n / 1024
			case "VmHWM:":
				s.PeakRSSMB = n / 1024
			case "Threads:":
				s.Threads = int(n)
			}
		}
		fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		s.FDs = len(fds)
		cpu := procCPU(pid)
		prev := 0.0
		if pid == so.lastPID {
			prev = so.lastCPU
		}
		if w := now.Sub(since).Seconds(); w > 0 {
			s.CPUPct = 100 * (cpu - prev) / w
		}
		so.lastPID, so.lastCPU = pid, cpu
		s.Goroutines, s.HeapInuse, s.HeapSys = so.goStats()
	}
	filepath.Walk(r.e.Dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			mb := float64(fi.Size()) / 1e6
			s.DataMB += mb
			rel, _ := filepath.Rel(r.e.Dir, p)
			s.Files[strings.SplitN(rel, string(filepath.Separator), 2)[0]] += mb
		}
		return nil
	})
	if fi, err := os.Stat(r.p.Log); err == nil {
		s.LogMB = float64(fi.Size()) / 1e6
	}
	if st := r.p.Status(); st != nil {
		s.Mode, _ = st["mode"].(string)
		s.Locked, _ = st["locked"].(bool)
		s.Pending, _ = st["pending"].(float64)
		s.Refused, _ = st["rejected"].(float64)
	}
	var saves []time.Duration
	for _, x := range r.ledger.all() {
		s.TotalSaves++
		if x.Confirmed {
			s.TotalConfirm++
		}
		if x.At.Before(since) || !x.At.Before(now) {
			continue
		}
		s.Saves++
		if x.Confirmed {
			s.Confirmed++
			saves = append(saves, x.Dur)
			if x.Online {
				s.ByCentral++
			}
		}
	}
	s.SaveP50, s.SaveP90, s.SaveP99, s.SaveMax = pct(saves, 50), pct(saves, 90), pct(saves, 99), pct(saves, 100)
	var lists, persons, all []time.Duration
	so.mu.Lock()
	for _, rd := range so.reads {
		if rd.At.Before(since) || !rd.At.Before(now) {
			continue
		}
		s.Reads++
		if rd.Err != "" || rd.Status != 200 {
			s.ReadErrors++
			continue
		}
		if rd.Local {
			s.ReadsLocal++
		}
		all = append(all, rd.Dur)
		if rd.Kind == "list" {
			lists = append(lists, rd.Dur)
		} else {
			persons = append(persons, rd.Dur)
		}
	}
	so.mu.Unlock()
	s.ListP50, s.ListP90, s.PersonP50, s.PersonP90, s.ReadMax = pct(lists, 50), pct(lists, 90), pct(persons, 50), pct(persons, 90), pct(all, 100)
	for _, c := range r.e.Net.Calls() {
		s.CentralCalls++
		s.CentralKinds[callKind(c)]++
		if c.Fault != "" {
			s.CallsFaulted++
		}
	}
	so.readLog(&s)
	s.CentralTokens = centralTokens()
	so.mu.Lock()
	so.samples = append(so.samples, s)
	so.mu.Unlock()
	so.lastSample = now
	r.t.Logf("%s sample: hub %s pid %d rss %.0f MB (peak %.0f) heap %.0f MB cpu %.1f%% goroutines %d fds %d; data %.1f MB log %.0f MB; %s pending %.0f refused %.0f; "+
		"saves %d (%d confirmed, %d by central) p50 %.0f p90 %.0f max %.0f ms; reads %d (%d errors, %d local) list p50 %.0f person p50 %.0f ms; "+
		"central calls %d; cycles %d (p50 %.0f, max %.0f ms, %d with errors); log warn %d error %d %v; tokens %s",
		fmtDur(time.Since(so.start)), map[bool]string{true: "up", false: "DOWN"}[s.Running], s.PID, s.RSSMB, s.PeakRSSMB, s.HeapInuse, s.CPUPct,
		s.Goroutines, s.FDs, s.DataMB, s.LogMB, s.Mode, s.Pending, s.Refused, s.Saves, s.Confirmed, s.ByCentral, s.SaveP50, s.SaveP90,
		s.SaveMax, s.Reads, s.ReadErrors, s.ReadsLocal, s.ListP50, s.PersonP50, s.CentralCalls, s.Cycles, s.CycleP50, s.CycleMax,
		s.CycleErrors, s.LogWarn, s.LogError, s.LogNotes, s.CentralTokens)
	so.writeSamples()
	so.writeLedger()
}

// procCPU is the process's CPU time in seconds (user and system).
func procCPU(pid int) float64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+2:])
	if len(f) < 13 {
		return 0
	}
	u, _ := strconv.ParseFloat(f[11], 64)
	k, _ := strconv.ParseFloat(f[12], 64)
	return (u + k) / 100 // USER_HZ
}

// goStats reads the hub's goroutines and heap from its profiler (HUB_PPROF).
func (so *soak) goStats() (goroutines int, inuse, sys float64) {
	c := http.Client{Timeout: 10 * time.Second}
	get := func(path string) string {
		resp, err := c.Get("http://" + so.pprof + path)
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if m := regexp.MustCompile(`goroutine profile: total (\d+)`).FindStringSubmatch(get("/debug/pprof/goroutine?debug=1")); m != nil {
		goroutines, _ = strconv.Atoi(m[1])
	}
	heap := get("/debug/pprof/heap?debug=1&gc=1") // after a collection: what is live
	num := func(name string) float64 {
		if m := regexp.MustCompile(`# ` + name + ` = (\d+)`).FindStringSubmatch(heap); m != nil {
			n, _ := strconv.ParseFloat(m[1], 64)
			return n / 1e6
		}
		return 0
	}
	return goroutines, num("HeapInuse"), num("HeapSys")
}

// callKind names a request the hub made to central, for the per-window counts.
func callKind(c tk.Call) string {
	p := c.URL.Path
	switch {
	case strings.HasSuffix(p, "/api/system/ping"):
		return "ping"
	case c.Method == "POST" && p == "/api/tracker" && c.URL.Query().Get("atomicMode") == "OBJECT":
		return "push"
	case c.Method == "POST" && p == "/api/tracker":
		return "import"
	case strings.HasPrefix(p, "/api/tracker/"):
		if c.URL.Query().Get("fields") == "trackedEntity" {
			return "owned"
		}
		return "tracker read"
	case strings.HasPrefix(p, "/api/dataStore/"):
		return "dataStore " + c.Method
	case strings.HasPrefix(p, "/api/apiToken"):
		return "apiToken " + c.Method
	case strings.Contains(p, "/reserve"):
		return "reserve"
	case strings.HasPrefix(p, "/api/me"):
		return "me"
	case strings.HasPrefix(p, "/api/auth") || strings.HasPrefix(p, "/dhis-web-commons-security"):
		return "login"
	}
	parts := strings.Split(strings.TrimPrefix(p, "/api/"), "/")
	return c.Method + " " + parts[0]
}

var (
	soakCycle = regexp.MustCompile(`msg="sync cycle done" ms=(\d+) err=(.*)$`)
	soakNotes = map[string]string{
		"hub token renewed":       "token renewed",
		"retention pruned":        "retention pruned",
		"full=true":               "full pull",
		"reserved values":         "reserved values",
		"ownership moved back":    "ownership back",
		"removed sent outbox":     "sent outbox pruned",
		"removed unused key slot": "key slots pruned",
		"profiler listening":      "profiler",
		"store unlocked":          "unlocked",
		"msg=\"pushed outbox":     "pushed",
		"heartbeat":               "heartbeat",
	}
)

// readLog counts what the hub logged since the last sample.
func (so *soak) readLog(s *soakSample) {
	f, err := os.Open(so.r.p.Log)
	if err != nil {
		return
	}
	defer f.Close()
	f.Seek(so.logOff, io.SeekStart)
	br := bufio.NewReaderSize(f, 1<<20)
	var cycles []time.Duration
	for {
		ln, err := br.ReadString('\n')
		if err != nil {
			break // a partial last line is read next time
		}
		so.logOff += int64(len(ln))
		if strings.Contains(ln, "msg=request ") {
			continue
		}
		if m := soakCycle.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
			ms, _ := strconv.Atoi(m[1])
			cycles = append(cycles, time.Duration(ms)*time.Millisecond)
			if m[2] != "<nil>" {
				s.CycleErrors++
			}
		}
		switch {
		case strings.Contains(ln, "level=WARN"):
			s.LogWarn++
		case strings.Contains(ln, "level=ERROR"):
			s.LogError++
		}
		for pat, name := range soakNotes {
			if strings.Contains(ln, pat) {
				s.LogNotes[name]++
			}
		}
	}
	s.Cycles = len(cycles)
	s.CycleP50, s.CycleMax = pct(cycles, 50), pct(cycles, 100)
}

// centralTokens counts the API tokens on central's database, by owner, when
// HUB_SOAK_PG names it: tokens the hub leaves behind would pile up there.
func centralTokens() string {
	conn := os.Getenv("HUB_SOAK_PG")
	if conn == "" {
		return ""
	}
	out, err := exec.Command("psql", conn, "-At", "-F", "=", "-c",
		"select coalesce(u.username, '?'), count(*) from api_token t left join userinfo u on u.userinfoid = t.createdby group by 1 order by 1").CombinedOutput()
	if err != nil {
		return "error: " + strings.TrimSpace(string(out))
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

func pct(ds []time.Duration, p int) float64 {
	if len(ds) == 0 {
		return 0
	}
	ds = slices.Clone(ds)
	slices.Sort(ds)
	i := min(len(ds)-1, (len(ds)*p)/100)
	return float64(ds[i].Microseconds()) / 1000
}

// soakSetTokens writes the token lifetime as the Facility Hub app does, signed with the
// move key CentralSetRetention gave the hub (the hub may run in its own process).
func soakSetTokens(t *testing.T, e *tk.Env, hubID string, days int) {
	t.Helper()
	const path = "/api/dataStore/facility-hub-config/tokens"
	v, err := hubaccount.Sign(map[string][]byte{hubID: tk.TestMoveKey}, "facility-hub-config/tokens",
		json.RawMessage(fmt.Sprintf(`{"expiryDays": %d}`, days)), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	st, b, err := e.Central.Do("PUT", path, v)
	if err == nil && st == 404 {
		st, b, err = e.Central.Do("POST", path, v)
	}
	if err != nil || st >= 300 {
		t.Logf("setting the token lifetime on central: %d %v %.300s", st, err, b)
	}
}

func (so *soak) writeJSON(name string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	os.WriteFile(filepath.Join(so.out, name), b, 0o644)
}

func (so *soak) writeSamples() {
	so.mu.Lock()
	defer so.mu.Unlock()
	var sb strings.Builder
	for _, s := range so.samples {
		b, _ := json.Marshal(s)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	os.WriteFile(filepath.Join(so.out, "samples.jsonl"), []byte(sb.String()), 0o644)
	os.WriteFile(filepath.Join(so.out, "events.txt"), []byte(strings.Join(so.events, "\n")+"\n"), 0o644)
	sb.Reset()
	for _, rd := range so.reads {
		b, _ := json.Marshal(rd)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	os.WriteFile(filepath.Join(so.out, "reads.jsonl"), []byte(sb.String()), 0o644)
}

func (so *soak) writeLedger() {
	var sb strings.Builder
	for _, s := range so.r.ledger.all() {
		b, _ := json.Marshal(s)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	os.WriteFile(filepath.Join(so.out, "ledger.jsonl"), []byte(sb.String()), 0o644)
}

// summary writes the trends: each measure's first, last, highest and slope per hour
// over the samples after the first hour (the hub warms up in it).
func (so *soak) summary(c netCheck) {
	t := so.r.t
	var lines []string
	add := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
		t.Logf(format, args...)
	}
	add("M1 soak %s, tag %s: %s", so.pl.D, so.r.tag, so.pl.describe())
	add("ledger: %s", so.r.ledger.summary())
	add("central: %s", c)
	ss := so.samples
	trend := func(name string, get func(soakSample) float64, onlyRunning bool) {
		var xs, ys []float64
		first, last, peak := math.NaN(), math.NaN(), math.Inf(-1)
		for _, s := range ss {
			if onlyRunning && !s.Running {
				continue
			}
			y := get(s)
			if math.IsNaN(first) {
				first = y
			}
			last, peak = y, max(peak, y)
			if s.Hours >= min(1, so.pl.D.Hours()/10) {
				xs, ys = append(xs, s.Hours), append(ys, y)
			}
		}
		add("%-28s first %10.1f  last %10.1f  highest %10.1f  slope after the first hour %+9.2f /h", name, first, last, peak, slope(xs, ys))
	}
	trend("hub RSS, MB", func(s soakSample) float64 { return s.RSSMB }, true)
	trend("hub heap in use, MB", func(s soakSample) float64 { return s.HeapInuse }, true)
	trend("goroutines", func(s soakSample) float64 { return float64(s.Goroutines) }, true)
	trend("open files", func(s soakSample) float64 { return float64(s.FDs) }, true)
	trend("threads", func(s soakSample) float64 { return float64(s.Threads) }, true)
	trend("hub CPU, % of a core", func(s soakSample) float64 { return s.CPUPct }, true)
	trend("data folder, MB", func(s soakSample) float64 { return s.DataMB }, false)
	for _, f := range soakFiles(ss) {
		trend("  "+f+", MB", func(s soakSample) float64 { return s.Files[f] }, false)
	}
	trend("hub log, MB", func(s soakSample) float64 { return s.LogMB }, false)
	trend("pending objects", func(s soakSample) float64 { return s.Pending }, false)
	trend("refused objects", func(s soakSample) float64 { return s.Refused }, false)
	trend("save p50, ms", func(s soakSample) float64 { return s.SaveP50 }, false)
	trend("save p90, ms", func(s soakSample) float64 { return s.SaveP90 }, false)
	trend("save p99, ms", func(s soakSample) float64 { return s.SaveP99 }, false)
	trend("working list p50, ms", func(s soakSample) float64 { return s.ListP50 }, false)
	trend("working list p90, ms", func(s soakSample) float64 { return s.ListP90 }, false)
	trend("person p50, ms", func(s soakSample) float64 { return s.PersonP50 }, false)
	trend("person p90, ms", func(s soakSample) float64 { return s.PersonP90 }, false)
	trend("sync cycle p50, ms", func(s soakSample) float64 { return s.CycleP50 }, false)
	trend("sync cycle max, ms", func(s soakSample) float64 { return s.CycleMax }, false)
	trend("requests to central / window", func(s soakSample) float64 { return float64(s.CentralCalls) }, false)
	notes := map[string]int{}
	var warn, errs, cycles, cycleErrs int
	for _, s := range ss {
		for k, v := range s.LogNotes {
			notes[k] += v
		}
		warn, errs, cycles, cycleErrs = warn+s.LogWarn, errs+s.LogError, cycles+s.Cycles, cycleErrs+s.CycleErrors
	}
	add("hub log: %d sync cycles (%d with errors), %d warnings, %d errors; %v", cycles, cycleErrs, warn, errs, notes)
	if len(ss) > 0 {
		add("API tokens on central: start %s; end %s", ss[0].CentralTokens, ss[len(ss)-1].CentralTokens)
	}
	add("events:\n%s", strings.Join(so.events, "\n"))
	os.WriteFile(filepath.Join(so.out, "summary.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func soakFiles(ss []soakSample) []string {
	seen := map[string]bool{}
	for _, s := range ss {
		for f := range s.Files {
			seen[f] = true
		}
	}
	var out []string
	for f := range seen {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

// slope is the least-squares slope of ys over xs.
func slope(xs, ys []float64) float64 {
	n := float64(len(xs))
	if n < 2 {
		return math.NaN()
	}
	var sx, sy, sxx, sxy float64
	for i := range xs {
		sx, sy, sxx, sxy = sx+xs[i], sy+ys[i], sxx+xs[i]*xs[i], sxy+xs[i]*ys[i]
	}
	d := n*sxx - sx*sx
	if d == 0 {
		return math.NaN()
	}
	return (n*sxy - sx*sy) / d
}

// soakRecheck runs the final check again on a soak's results folder: its ledger
// against central as it is now.
func soakRecheck(t *testing.T, dir string) {
	var run struct {
		Tag, Program, Stage, DataElement string
	}
	b, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil || json.Unmarshal(b, &run) != nil {
		t.Fatalf("reading %s/run.json: %v", dir, err)
	}
	r := netPrepare(t)
	if r.prog.ID != run.Program {
		t.Fatalf("the manifest's primary program is %s, the soak's %s", r.prog.ID, run.Program)
	}
	r.tag, r.stage, r.de = run.Tag, run.Stage, run.DataElement
	f, err := os.Open(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var s netSave
		if json.Unmarshal(sc.Bytes(), &s) == nil {
			r.ledger.add(s)
		}
	}
	t.Logf("ledger: %s", r.ledger.summary())
	c := r.check()
	t.Logf("central: %s", c)
	if !c.ok() {
		t.Errorf("invariants broken: %s", c)
	}
}

// Found in M1: when one user logs in online through several clients at once (after a
// restart, a web app, its reader and an Android app all log in again), each login
// ran core.EnsurePAT on the same stored user with no lock per user, and each created
// a token on central. The hub kept the last; the others stayed on central, unlisted
// in the hub's registry entry (so the Facility Hub app could not revoke them) until
// they expired. In the soak, three logins of the nurse in the same second after a
// restart made two tokens; reproduced 29 Oct on synthetic 2.42, twice: 4 concurrent
// logins, 2 tokens each time. Fixed: EnsurePAT runs under a lock per user, so
// concurrent logins of one user make one token.
func TestM1ConcurrentLoginsMakeOneToken(t *testing.T) {
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("adversarial scenarios run against a real DHIS2 (HUB_MANIFEST)")
	}
	e.Setup()
	nurse, _ := e.M.User(tk.RoleNurse)
	cen := e.Central.As(nurse.Username, e.M.Password)
	tokens := func() []string {
		var res struct {
			Tokens []struct{ ID string } `json:"apiToken"`
		}
		if err := cen.JSON("GET", "/api/apiToken?fields=id&paging=false", nil, &res); err != nil {
			t.Fatalf("listing the nurse's tokens on central: %v", err)
		}
		var out []string
		for _, x := range res.Tokens {
			out = append(out, x.ID)
		}
		return out
	}
	before := tokens()
	const logins = 4
	var wg sync.WaitGroup
	statuses := make([]int, logins)
	for i := range logins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := e.Web(tk.RoleNurse)
			statuses[i], _, _, _ = c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": e.M.Password})
		}()
	}
	wg.Wait()
	created := slices.DeleteFunc(tokens(), func(id string) bool { return slices.Contains(before, id) })
	t.Cleanup(func() {
		for _, id := range created {
			cen.Do("DELETE", "/api/apiToken/"+id, nil)
		}
	})
	t.Logf("%d concurrent online logins of %s (answers %v): %d tokens created on central %v", logins, nurse.Username, statuses, len(created), created)
	if len(created) != 1 {
		t.Errorf("%d tokens created for one user; the hub holds one, the other %d stay on central", len(created), len(created)-1)
	}
}

// Seen in M1: each full pull "removed visibility" of a few objects (2–7) for a user
// while staff kept saving. A save made online while a full pull lists its program is
// marked visible to its author, but is not in the listing taken before it; the full
// pull then removed what it did not list (sync.pullGroup, RemoveVisibilityExcept).
// Like every set-up here, it registers a hub for the facility, which retires any
// other hub of the facility on that central: not while a soak runs there.
// Checks, offline, that the author still sees each person they registered online
// during a full pull, and again after the next incremental pull.
//
// Reproduced 29 Oct on synthetic 2.42: 6 of 107 people were hidden from her (503
// offline) after the full pull, all visible again after the next pull; in the field
// that was up to a sync interval (15 minutes) after each weekly full pull. With
// nothing saved during the full pull (TestM1FullPullKeepsSavedVisible) none was
// hidden. Fixed: RemoveVisibilityExcept compares with the listing's start time and
// keeps what the hub wrote or passed on to central after it (the hub also stamps
// online writes it records with its own time, until the next pull gives central's).
func TestM1SaveDuringFullPullStaysVisible(t *testing.T) {
	r := netPrepare(t)
	e := r.e
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	e.Net.SetDelay(150 * time.Millisecond) // a full pull long enough to save during it
	e.App.Hub.Cfg.FullPullInterval = time.Nanosecond
	done := make(chan error, 1)
	go func() { done <- e.App.Worker.Cycle(t.Context()) }()
	w := &netWriter{r: r, c: nurse, web: true}
	for running := true; running; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("full pull: %v", err)
			}
			running = false
		default:
			w.once()
			time.Sleep(200 * time.Millisecond)
		}
	}
	e.Net.SetDelay(0)
	e.App.Hub.Cfg.FullPullInterval = 7 * 24 * time.Hour
	var during []netSave
	for _, s := range r.ledger.all() {
		if s.Confirmed && !s.Update && s.Online {
			during = append(during, s)
		}
	}
	if len(during) == 0 {
		t.Fatal("no save passed through to central during the full pull")
	}
	hidden := func() []string {
		e.Offline()
		defer e.Online()
		var out []string
		for _, s := range during {
			if st, _ := nurse.Do("GET", "/api/tracker/trackedEntities/"+s.TE+"?program="+r.prog.ID+"&fields=trackedEntity", nil); st != 200 {
				out = append(out, fmt.Sprintf("%s (%d)", s.TE, st))
			}
		}
		return out
	}
	afterFull := hidden()
	e.Sync() // incremental
	afterNext := hidden()
	t.Logf("%d people registered online by %s during a full pull; offline, hidden from them after it: %d %v; after the next incremental pull: %d %v",
		len(during), nurse.User, len(afterFull), afterFull, len(afterNext), afterNext)
	if len(afterFull) > 0 {
		t.Errorf("I1/I8: %d of %d people the nurse registered online during a full pull are hidden from her offline after it (%d still after the next pull)",
			len(afterFull), len(during), len(afterNext))
	}
}

// The same check with nothing saved during the full pull: the soak's writers save
// online, then offline (pushed), then a full pull. Tells a race with the listing apart from
// a full pull that drops what the hub itself stored.
func TestM1FullPullKeepsSavedVisible(t *testing.T) {
	r := netPrepare(t)
	e := r.e
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	ws := r.writers() // as in the soak: the nurse and the restricted user on the web, the nurse on Android
	for range 20 {
		for _, w := range ws {
			w.once()
		}
	}
	e.Offline()
	for range 20 {
		for _, w := range ws {
			w.once()
		}
	}
	e.Online()
	e.Sync() // pushes the offline ones, incremental pull
	e.Sync()
	e.App.Hub.Cfg.FullPullInterval = time.Nanosecond
	e.Sync()
	e.App.Hub.Cfg.FullPullInterval = 7 * 24 * time.Hour
	var saved []netSave
	for _, s := range r.ledger.all() {
		if s.Confirmed && !s.Update {
			saved = append(saved, s)
		}
	}
	e.Offline()
	var hidden []string
	clients := map[string]*tk.Client{}
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		c := e.Web(role)
		c.Login()
		clients[c.User] = c
	}
	for _, s := range saved {
		if st, _ := clients[s.User].Do("GET", "/api/tracker/trackedEntities/"+s.TE+"?program="+r.prog.ID+"&fields=trackedEntity", nil); st != 200 {
			hidden = append(hidden, fmt.Sprintf("%s by %s (online=%v, %d)", s.TE, s.User, s.Online, st))
		}
	}
	e.Online()
	t.Logf("%d registrations, none during the full pull; hidden offline from their author after it: %d %v", len(saved), len(hidden), hidden)
	if len(hidden) > 0 {
		t.Errorf("a full pull with no concurrent saves hid %d of %d registrations from their author", len(hidden), len(saved))
	}
}

// The open soak item (the session records (private notes, docs/sessions/FINDINGS-SESSIONS.md), M1): the soak's last full pull "removed
// visibility" of 51 objects for its three users, though its writers were thought to
// have stopped. A probe: the soak's work in miniature (the three writers online,
// offline, online again, with updates), two syncs, then a full pull with nothing
// saved during it. Every object a full pull takes from a user is logged with what the
// hub holds of it and whether central still shows it to that user, so a removal
// central asked for can be told from a fault.
func TestM1FullPullRemovesOnlyWhatCentralHides(t *testing.T) {
	fullPullProbe(t, false)
}

// The same with the writers saving through the full pull (the uplink slowed so it
// lasts), as they were during the soak's last one: 56 saves in its 3½ minutes.
func TestM1FullPullDuringSavesRemovesOnlyWhatCentralHides(t *testing.T) {
	fullPullProbe(t, true)
}

func fullPullProbe(t *testing.T, saving bool) {
	r := netPrepare(t)
	e := r.e
	e.Setup()
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		e.Web(role).Login()
	}
	e.Sync()
	ws := r.writers()
	work := func(n int) {
		for range n {
			for _, w := range ws {
				w.once()
			}
		}
	}
	work(20)
	e.Offline()
	work(20)
	e.Online()
	work(10)
	e.Sync()
	e.Sync()
	h := e.App.Hub
	users, err := h.Store.Users()
	if err != nil {
		t.Fatal(err)
	}
	type obj struct{ uid, kind, program, origin, updated string }
	objects := func() map[uint32]obj {
		rows, err := h.Store.DB().Query(`SELECT slot, uid, kind, program, origin, updated_at FROM objects`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[uint32]obj{}
		for rows.Next() {
			var s int64
			var o obj
			rows.Scan(&s, &o.uid, &o.kind, &o.program, &o.origin, &o.updated)
			out[uint32(s)] = o
		}
		return out
	}
	before := map[int64]map[uint32]bool{}
	for _, u := range users {
		before[u.ID] = map[uint32]bool{}
		for _, s := range h.Index.VisibleSlots(u.ID) {
			before[u.ID][s] = true
		}
	}
	saves := len(r.ledger.all())
	h.Cfg.FullPullInterval = time.Nanosecond
	if saving {
		e.Net.SetDelay(150 * time.Millisecond)
		done := make(chan error, 1)
		go func() { done <- e.App.Worker.Cycle(t.Context()) }()
		for running := true; running; {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("full pull: %v", err)
				}
				running = false
			default:
				work(1)
				time.Sleep(300 * time.Millisecond)
			}
		}
		e.Net.SetDelay(0)
	} else {
		e.Sync()
	}
	h.Cfg.FullPullInterval = 7 * 24 * time.Hour
	during := len(r.ledger.all()) - saves
	objs := objects()
	kinds := map[string]string{store.KindTrackedEntity: "trackedEntities", store.KindEnrollment: "enrollments", store.KindEvent: "events", store.KindRelationship: "relationships"}
	total, faults := 0, 0
	for _, u := range users {
		now := map[uint32]bool{}
		for _, s := range h.Index.VisibleSlots(u.ID) {
			now[s] = true
		}
		var gone []string
		for s := range before[u.ID] {
			if now[s] {
				continue
			}
			o := objs[s]
			st := 0
			if k := kinds[o.kind]; k != "" {
				path := "/api/tracker/" + k + "/" + o.uid + "?fields=updatedAt"
				if k == "trackedEntities" && o.program != "" {
					path += "&program=" + o.program
				}
				st, _, _ = e.Central.As(u.Username, e.M.Password).Do("GET", path, nil)
			}
			if st == 200 {
				faults++
			}
			gone = append(gone, fmt.Sprintf("%s %s program=%q origin=%s updated=%s (central to %s: %d)", o.kind, o.uid, o.program, o.origin, o.updated, u.Username, st))
		}
		total += len(gone)
		slices.Sort(gone)
		t.Logf("%s: %d visible before the full pull, %d after; removed %d: %v", u.Username, len(before[u.ID]), len(now), len(gone), gone)
	}
	// Objects saved during the full pull were not visible before it: whether a user
	// lost one shows only against central. Each person of the run and its event, per
	// user: held as central's, not visible to the user, and central shows it them.
	var lost []string
	for _, u := range users {
		cen := e.Central.As(u.Username, e.M.Password)
		for _, sv := range r.ledger.all() {
			if !sv.Confirmed || sv.Update {
				continue
			}
			for _, x := range [][2]string{{"trackedEntities", sv.TE}, {"events", sv.Event}} {
				o := h.Index.Get(x[1])
				if o == nil || o.Origin != store.OriginCentral || h.Index.VisibleTo(o, u.ID) {
					continue
				}
				path := "/api/tracker/" + x[0] + "/" + x[1] + "?fields=updatedAt"
				if x[0] == "trackedEntities" {
					path += "&program=" + r.prog.ID
				}
				if st, _, _ := cen.Do("GET", path, nil); st == 200 {
					lost = append(lost, fmt.Sprintf("%s %s saved by %s at %s (online=%v), hidden from %s", x[0], x[1], sv.User,
						sv.At.Format("15:04:05.000"), sv.Online, u.Username))
				}
			}
		}
	}
	faults += len(lost)
	t.Logf("the run's people and events central shows a user but the hub does not: %d %v", len(lost), first(lost))
	t.Logf("%s; %d saves during the full pull; it removed %d objects from users, %d of them still shown by central", r.ledger.summary(), during, total, faults)
	if faults > 0 {
		t.Errorf("a full pull (%d saves during it) took %d objects from users that central still shows them", during, faults)
	}
}
