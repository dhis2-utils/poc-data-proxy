package perf

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
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

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// TestWeekOffline is a large facility working a whole week offline, then reconnecting
// (docs/PERFORMANCE.md, "A week offline at HC IV size"). A hub filled from the
// generated central at HUB_PERF_SCALE (default the HC IV size) runs as its own process;
// its uplink is cut, and each day of HUB_WEEK_DAYS (7), compressed into HUB_WEEK_DAY
// (30m), brings one day's saves of a busy HC IV (HUB_WEEK_SAVES, 3 000: registrations,
// enrollments of people registered that week, new events, edits) spread evenly.
// Throughout, staff read (working lists, dashboards, searches) at HUB_WEEK_READ_RATE
// a second (2) and each Android device syncs every HUB_WEEK_SYNC_EVERY (5m): field
// rates, not compressed (24 times the field's reads and syncs saturate the hub at
// 100 000 people). The process, its data folder and the answers are sampled
// throughout. Then the uplink returns over each link of HUB_WEEK_LINKS
// ("4g,unlimited"; 4g is 3 Mbit/s and 100 ms), each from the same copy of the week's
// data folder, while staff go on working (also 0.4 saves a second), and the drain is
// timed. Every save the hub confirmed is then read back on central. A measurement:
// HUB_WEEK=1 runs it.
//
//	HUB_WEEK=1 go test ./internal/perf -run TestWeekOffline -timeout 12h -v
//
// Other settings: HUB_PERF_USERS (20), HUB_WEEK_ANDROID (Android devices among them,
// 6), HUB_WEEK_WATCH (longest wait for a drain, 2h). Results go to
// testresults/week/<time>/ (samples.jsonl, ops.jsonl, ledger.jsonl, summary.txt, the
// hub's log).
func TestWeekOffline(t *testing.T) {
	if os.Getenv("HUB_WEEK") == "" {
		t.Skip("a measurement: HUB_WEEK=1 runs it")
	}
	pl := weekPlanFromEnv()
	w := &week{t: t, pl: pl, sent: map[string]int{}, skipped: map[string]int{}, pickedNew: map[string]bool{}}
	w.out = filepath.Join("..", "..", "testresults", "week", time.Now().Format("20060102-1504"))
	if err := os.MkdirAll(w.out, 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w.pprof = ln.Addr().String()
	ln.Close()
	t.Setenv("HUB_PPROF", w.pprof) // the hub process inherits it

	e := tk.Start(t)
	e.NeedFake()
	w.e = e
	e.Fake.Generate(pl.Scale)
	// Field intervals, apart from the sync interval: the test runs a cycle after each
	// push itself. The weekly full pull is not due: the fake's listings hold only the
	// generated people, so a full pull would hide the week's registrations.
	e.ProcConfig = []string{"sync_interval: 5m", "ownership_check_interval: 1h", "access_refresh_interval: 1h",
		"full_pull_interval: 168h", "session_idle: 12h", "session_max: 24h"}
	w.note("plan: %s", pl)
	start := time.Now()
	e.Setup()
	for i := range pl.Users {
		c := w.client(i)
		if st, b, _, err := c.Try("GET", "/api/me?fields=id", nil); err != nil || st != 200 {
			t.Fatalf("%s: %d %s %v", fc.GenUser(i), st, b, err)
		}
	}
	w.note("set up, %d users registered in %s", pl.Users, time.Since(start).Round(time.Second))
	start = time.Now()
	e.Sync()
	w.note("first pull: %s, %d objects; data folder %.0f MB", time.Since(start).Round(time.Second), e.App.Hub.Index.Len(), dirMB(e.Dir))
	w.p = e.Detach()
	w.p.WaitFor("online", 5*time.Minute, tk.Online)
	w.unlock()
	w.note("hub process %d online; hub log %s", w.p.Pid(), w.p.Log)
	t.Cleanup(w.keepLog)
	e.Net.Rewrite(w.countPush, nil) // never matches: it only counts what each push sends

	stopSampler := w.sampler()
	defer stopSampler()

	// The week.
	e.Net.SetDown(true)
	w.p.WaitFor("offline", 2*time.Minute, tk.Offline)
	w.setPhase("day 1")
	stopField := w.fieldLoad(0)
	for d := 1; d <= pl.Days; d++ {
		w.setPhase(fmt.Sprintf("day %d", d))
		start := time.Now()
		w.day()
		w.sample()
		s := w.p.Status()
		w.note("day %d done in %s: %s; %v objects pending; data folder %.0f MB; rss %.0f MB (peak %.0f)", d,
			time.Since(start).Round(time.Second), w.savesIn(w.phase), s["pending"], dirMB(e.Dir), w.p.RSSMB(), w.p.PeakRSSMB())
	}
	stopField()
	w.setPhase("after the week")
	weekPending := pendingOf(w.p.Status())
	w.note("after the week: %s; %d objects pending", w.savesIn(""), weekPending)

	// The week's data folder, so each link drains the same backlog.
	snap := filepath.Join(t.TempDir(), "week")
	w.p.Stop()
	if b, err := exec.Command("cp", "-a", e.Dir, snap).CombinedOutput(); err != nil {
		t.Fatalf("copying the data folder: %v %s", err, b)
	}
	w.note("data folder copied (%.0f MB)", dirMB(snap))
	for i, link := range pl.Links {
		if i > 0 {
			e.Net.SetDown(true)
			w.p.Stop()
			ents, _ := os.ReadDir(e.Dir)
			for _, d := range ents {
				os.RemoveAll(filepath.Join(e.Dir, d.Name()))
			}
			if b, err := exec.Command("cp", "-a", snap+"/.", e.Dir).CombinedOutput(); err != nil {
				t.Fatalf("restoring the data folder: %v %s", err, b)
			}
		}
		start := time.Now()
		w.p.Start()
		w.unlock()
		w.note("hub restarted offline and unlocked in %s; %v objects pending", time.Since(start).Round(time.Second), w.p.Status()["pending"])
		w.drain(link)
	}
	stopSampler()
	w.writeAll()
	w.summary()
}

// weekPlan is the run's shape: the facility, the users, a day's volume and the links.
type weekPlan struct {
	Scale                       fc.Scale
	Users, Android, Days        int
	Day                         time.Duration
	Saves                       int     // a day's saves
	ReadRate, DrainSaves        float64 // a second
	AndroidEvery, Watch, Sample time.Duration
	Links                       []weekLink
}

type weekLink struct {
	Name  string
	Rate  int // bytes a second each way; 0 unlimited
	Delay time.Duration
}

func (pl weekPlan) String() string {
	return fmt.Sprintf("%d people, %d enrollments, %d events, %d programs; %d users (%d Android); %d days of %s, each %d saves; "+
		"throughout %.1f reads/s and each device syncing every %s; while draining also %.1f saves/s; links %v",
		pl.Scale.TEs, pl.Scale.Enrollments, pl.Scale.Events, pl.Scale.Programs, pl.Users, pl.Android, pl.Days, pl.Day,
		pl.Saves, pl.ReadRate, pl.AndroidEvery, pl.DrainSaves, pl.Links)
}

func weekPlanFromEnv() weekPlan {
	num := func(k string, d int) int {
		if n, err := strconv.Atoi(os.Getenv(k)); err == nil {
			return n
		}
		return d
	}
	dur := func(k string, d time.Duration) time.Duration {
		if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
			return v
		}
		return d
	}
	s := fc.Scale{TEs: 300000, Enrollments: 500000, Events: 1200000}
	if v := os.Getenv("HUB_PERF_SCALE"); v != "" {
		var n [3]int
		for i, f := range strings.SplitN(v, ",", 3) {
			n[i], _ = strconv.Atoi(strings.TrimSpace(f))
		}
		s = fc.Scale{TEs: n[0], Enrollments: n[1], Events: n[2]}
	}
	pl := weekPlan{Users: num("HUB_PERF_USERS", 20), Android: num("HUB_WEEK_ANDROID", 6), Days: num("HUB_WEEK_DAYS", 7),
		Day: dur("HUB_WEEK_DAY", 30*time.Minute), Saves: num("HUB_WEEK_SAVES", 3000), ReadRate: 2, DrainSaves: 0.4,
		AndroidEvery: dur("HUB_WEEK_SYNC_EVERY", 5*time.Minute), Watch: dur("HUB_WEEK_WATCH", 2*time.Hour)}
	if v, err := strconv.ParseFloat(os.Getenv("HUB_WEEK_READ_RATE"), 64); err == nil && v > 0 {
		pl.ReadRate = v
	}
	pl.Sample = max(pl.Day/6, 10*time.Second)
	s.Programs, s.Years, s.Users = 6, 5, pl.Users
	// Active enrollments changed at generation time: 1 %, so that an incremental pull
	// finds a few hundred people changed on central, not every active enrollment.
	s.Changed = 0.01
	pl.Scale = s
	links := map[string]weekLink{"unlimited": {Name: "unlimited"}, "4g": {"4g", 375000, 100 * time.Millisecond},
		"poor": {"poor", 125000, 300 * time.Millisecond}}
	for _, n := range strings.Split(cmp.Or(os.Getenv("HUB_WEEK_LINKS"), "4g,unlimited"), ",") {
		if l, ok := links[strings.TrimSpace(n)]; ok {
			pl.Links = append(pl.Links, l)
		}
	}
	return pl
}

// week is the run: the hub process, its clients and everything they did.
type week struct {
	t     *testing.T
	pl    weekPlan
	e     *tk.Env
	p     *tk.Proc
	out   string
	pprof string

	mu        sync.Mutex
	phase     string
	notes     []string
	saves     []*weekSave
	ops       []weekOp
	samples   []weekSample
	regs      []*weekSave     // confirmed registrations, for enrollments and dashboards
	events    []*weekSave     // confirmed new events, to edit
	pickedNew map[string]bool // people already enrolled in a second program
	sent      map[string]int  // uid -> times a push carried it, this drain
	skipped   map[string]int  // ticks of the field load skipped: its requests all open
	clients   []*tk.Client
	lastSync  map[int]time.Time
	lastCPU   float64
	lastPID   int
	lastAt    time.Time
}

// weekSave is one save a client attempted.
type weekSave struct {
	Kind      string // "register", "enrol", "event", "edit"
	Phase     string
	User      string
	Android   bool
	TE, Enr   string
	Event     string
	Program   int
	Value     string
	Confirmed bool
	Online    bool
	Status    int
	Err       string
	At        time.Time
	Dur       time.Duration
}

// weekOp is one timed operation: a save, a read or an Android sync.
type weekOp struct {
	Phase  string
	Kind   string
	At     time.Time
	Dur    time.Duration
	Status int
	Local  bool   // the hub answered itself
	Err    string `json:",omitempty"` // the start of an answer other than 200
	N      int    `json:",omitempty"` // people an Android sync received
}

type weekSample struct {
	At                      time.Time
	Phase                   string
	PID                     int
	RSSMB, PeakRSSMB        float64
	CPUPct                  float64
	HeapMB                  float64
	Goroutines              int
	DataMB                  float64
	Files                   map[string]float64
	Mode                    string
	Pending, Refused        float64
	Saves, Confirmed, Reads int
}

func (w *week) note(f string, a ...any) {
	s := fmt.Sprintf(f, a...)
	w.t.Log(s)
	w.mu.Lock()
	w.notes = append(w.notes, time.Now().Format("15:04:05 ")+s)
	w.mu.Unlock()
}

func (w *week) setPhase(p string) {
	w.mu.Lock()
	w.phase = p
	w.mu.Unlock()
}

func (w *week) curPhase() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.phase
}

// client is user i's device: the first pl.Android users are Android devices; all use
// Basic auth, as cmd/hubload's users do.
func (w *week) client(i int) *tk.Client {
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.clients) <= i {
		c := w.e.AndroidUser(fc.GenUser(len(w.clients)))
		c.HTTP.Timeout = 5 * time.Minute
		w.clients = append(w.clients, c)
	}
	return w.clients[i]
}

func (w *week) android(i int) bool { return i < w.pl.Android }

// unlock logs a user in until the hub's store is open (after a start).
func (w *week) unlock() {
	for range 60 {
		w.client(0).Try("GET", "/api/me?fields=id", nil)
		if s := w.p.Status(); s != nil && s["locked"] == false {
			return
		}
		time.Sleep(2 * time.Second)
	}
	w.t.Fatalf("the hub stays locked: %v", w.p.Status())
}

// --- the work ---------------------------------------------------------------------

// day makes one day's saves, one every Day/Saves; at most 64 run at once (a slower
// hub makes the day longer rather than piling up requests).
func (w *week) day() {
	every := w.pl.Day / time.Duration(max(w.pl.Saves, 1))
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup
	next := time.Now()
	for i := range w.pl.Saves {
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
		next = next.Add(every)
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			w.save(i % w.pl.Users)
		}()
	}
	wg.Wait()
}

// save makes one save of the HC IV mix: of 3 000 a day, 560 registrations (person,
// enrollment and first event), 360 enrollments of a person registered earlier in the
// week into another program (with an event), 1 680 new events in an enrollment (half
// the generated facility's active ones, half the week's), 400 edits of an event saved
// earlier in the week.
func (w *week) save(user int) {
	k := rand.N(3000)
	switch {
	case k < 560:
		w.register(user)
	case k < 920:
		w.enrol(user)
	case k < 2600:
		w.newEvent(user)
	default:
		w.edit(user)
	}
}

const weekDE = "GenDe000000"

func (w *week) eventPayload(s *weekSave) map[string]any {
	return map[string]any{"event": s.Event, "enrollment": s.Enr, "trackedEntity": s.TE, "program": fc.GenProgram(s.Program),
		"programStage": fc.GenStage(s.Program, 1), "orgUnit": fc.Facility, "status": "ACTIVE",
		"occurredAt": time.Now().Format("2006-01-02"),
		"dataValues": []any{map[string]any{"dataElement": weekDE, "value": s.Value}}}
}

func (w *week) newSave(kind string, user int) *weekSave {
	return &weekSave{Kind: kind, Phase: w.curPhase(), User: fc.GenUser(user), Android: w.android(user),
		Value: fmt.Sprintf("%d units", rand.N(997)), At: time.Now()}
}

func (w *week) register(user int) {
	s := w.newSave("register", user)
	s.TE, s.Enr, s.Event, s.Program = tk.UID(), tk.UID(), tk.UID(), rand.N(w.pl.Scale.Programs)
	enr := map[string]any{"enrollment": s.Enr, "program": fc.GenProgram(s.Program), "orgUnit": fc.Facility, "status": "ACTIVE",
		"enrolledAt": time.Now().Format("2006-01-02"), "occurredAt": time.Now().Format("2006-01-02"),
		"events": []any{w.eventPayload(s)}}
	w.post(user, s, map[string]any{"trackedEntities": []any{map[string]any{"trackedEntity": s.TE, "trackedEntityType": fc.TET,
		"orgUnit": fc.Facility, "attributes": []any{
			map[string]any{"attribute": fc.AttrFirstName, "value": []string{"Amina", "Chen", "Emeka", "Hana"}[rand.N(4)]},
			map[string]any{"attribute": fc.AttrLastName, "value": "Week" + strings.ToLower(s.TE[:5])}},
		"enrollments": []any{enr}}}})
}

// enrol enrols a person registered earlier in the week into another program.
func (w *week) enrol(user int) {
	w.mu.Lock()
	var reg *weekSave
	for range 20 {
		if len(w.regs) == 0 {
			break
		}
		if r := w.regs[rand.N(len(w.regs))]; !w.pickedNew[r.TE] {
			reg = r
			w.pickedNew[r.TE] = true
			break
		}
	}
	w.mu.Unlock()
	if reg == nil {
		w.register(user)
		return
	}
	s := w.newSave("enrol", user)
	s.TE, s.Enr, s.Event, s.Program = reg.TE, tk.UID(), tk.UID(), (reg.Program+1)%w.pl.Scale.Programs
	ev := w.eventPayload(s)
	w.post(user, s, map[string]any{"enrollments": []any{map[string]any{"enrollment": s.Enr, "trackedEntity": s.TE,
		"program": fc.GenProgram(s.Program), "orgUnit": fc.Facility, "status": "ACTIVE",
		"enrolledAt": time.Now().Format("2006-01-02"), "occurredAt": time.Now().Format("2006-01-02"), "events": []any{ev}}}})
}

// activeEnrollment is a generated enrollment the facility still works in (enrolled in
// the last 12 months): its uid, person and program.
func (w *week) activeEnrollment() (enr, te string, program int) {
	s := w.pl.Scale
	lo := s.Enrollments * (s.Years*12 - 11) / (s.Years * 12)
	j := lo + rand.N(s.Enrollments-lo)
	return fmt.Sprintf("En%09d", j), fmt.Sprintf("Te%09d", j%s.TEs), (j + j/s.TEs) % s.Programs
}

func (w *week) newEvent(user int) {
	s := w.newSave("event", user)
	s.Event = tk.UID()
	w.mu.Lock()
	var reg *weekSave
	if len(w.regs) > 0 && rand.N(2) == 0 {
		reg = w.regs[rand.N(len(w.regs))]
	}
	w.mu.Unlock()
	if reg != nil {
		s.TE, s.Enr, s.Program = reg.TE, reg.Enr, reg.Program
	} else {
		s.Enr, s.TE, s.Program = w.activeEnrollment()
	}
	w.post(user, s, map[string]any{"events": []any{w.eventPayload(s)}})
}

// edit changes the value of an event saved this week, as its author.
func (w *week) edit(user int) {
	w.mu.Lock()
	var prev *weekSave
	if len(w.events) > 0 {
		prev = w.events[rand.N(len(w.events))]
	}
	w.mu.Unlock()
	if prev == nil {
		w.newEvent(user)
		return
	}
	idx := slices.IndexFunc(w.clients, func(c *tk.Client) bool { return c.User == prev.User })
	s := w.newSave("edit", idx)
	s.TE, s.Enr, s.Event, s.Program = prev.TE, prev.Enr, prev.Event, prev.Program
	w.post(idx, s, map[string]any{"events": []any{w.eventPayload(s)}})
}

// post sends a save as user's device (Android with atomicMode=OBJECT, as the app does)
// and records it.
func (w *week) post(user int, s *weekSave, payload map[string]any) {
	path := tk.SyncImport
	if s.Android {
		path = strings.Replace(path, "atomicMode=ALL", "atomicMode=OBJECT", 1)
	}
	st, h, b, err := w.client(user).Try("POST", path, payload)
	s.Dur, s.Status = time.Since(s.At), st
	if err != nil {
		s.Err = err.Error()
	}
	s.Confirmed = err == nil && accepted(st, b)
	s.Online = h != nil && h.Get("X-Hub-Mode") == ""
	if !s.Confirmed && err == nil {
		s.Err = fmt.Sprintf("%d %.300s", st, b)
	}
	w.mu.Lock()
	w.saves = append(w.saves, s)
	w.ops = append(w.ops, weekOp{s.Phase, "save: " + s.Kind, s.At, s.Dur, st, !s.Online, s.Err, 0})
	if s.Confirmed {
		switch s.Kind {
		case "register":
			w.regs = append(w.regs, s)
			w.events = append(w.events, s)
		case "enrol", "event":
			w.events = append(w.events, s)
		}
	}
	w.mu.Unlock()
}

func errOf(st int, b []byte) string {
	if st == 200 {
		return ""
	}
	return fmt.Sprintf("%.200s", b)
}

func accepted(status int, body []byte) bool {
	if status != 200 {
		return false
	}
	var rep struct {
		Status string `json:"status"`
		Stats  struct {
			Ignored int `json:"ignored"`
		} `json:"stats"`
	}
	return json.Unmarshal(body, &rep) == nil && rep.Status == "OK" && rep.Stats.Ignored == 0
}

func (w *week) timed(user int, kind, path string) (int, []byte) {
	at := time.Now()
	st, h, b, _ := w.client(user).Try("GET", path, nil)
	w.mu.Lock()
	w.ops = append(w.ops, weekOp{w.phase, kind, at, time.Since(at), st, h != nil && h.Get("X-Hub-Mode") != "", errOf(st, b), 0})
	w.mu.Unlock()
	return st, b
}

// read is one of a day's reads: a working list (two in five), a dashboard (a person
// and their events; two in five, half of them people registered this week) or a
// search by name.
func (w *week) read(user int) {
	p := rand.N(w.pl.Scale.Programs)
	prog := fc.GenProgram(p)
	switch k := rand.N(5); {
	case k < 2:
		w.timed(user, "read: working list", "/api/tracker/trackedEntities?program="+prog+"&orgUnits="+fc.Facility+
			"&page=1&pageSize=15&order=createdAt:desc&fields=trackedEntity,attributes[attribute,value],enrollments[enrollment,status,enrolledAt]")
	case k < 4:
		// The fake central lists events only of generated enrollments: online, a
		// dashboard of a person registered this week answers 404 from it.
		enr, te, pr := w.activeEnrollment()
		kind := "read: dashboard"
		w.mu.Lock()
		if len(w.regs) > 0 && rand.N(2) == 0 {
			r := w.regs[rand.N(len(w.regs))]
			enr, te, pr, kind = r.Enr, r.TE, r.Program, "read: dashboard, new person"
		}
		w.mu.Unlock()
		at := time.Now()
		st, h, b, _ := w.client(user).Try("GET", "/api/tracker/trackedEntities/"+te+"?program="+fc.GenProgram(pr)+"&fields=*", nil)
		if st == 200 {
			st, _, b, _ = w.client(user).Try("GET", "/api/tracker/events?enrollments="+enr+"&fields=*&paging=false", nil)
		}
		w.mu.Lock()
		w.ops = append(w.ops, weekOp{w.phase, kind, at, time.Since(at), st, h != nil && h.Get("X-Hub-Mode") != "", errOf(st, b), 0})
		w.mu.Unlock()
	default:
		w.timed(user, "read: search by name", "/api/tracker/trackedEntities?program="+prog+"&orgUnits="+fc.Facility+
			"&filter="+fc.AttrFirstName+":like:"+[]string{"Amina", "Chen", "Emeka", "Hana", "Omar"}[rand.N(5)]+
			"&pageSize=15&fields=trackedEntity,attributes[attribute,value]")
	}
}

// androidSync is an Android device's incremental sync: what changed in each program
// since its last sync (up to 500 people a program, 50 a page).
func (w *week) androidSync(user int) {
	w.mu.Lock()
	if w.lastSync == nil {
		w.lastSync = map[int]time.Time{}
	}
	since, ok := w.lastSync[user]
	if !ok {
		since = time.Now().Add(-w.pl.AndroidEvery)
	}
	w.lastSync[user] = time.Now()
	w.mu.Unlock()
	after := since.Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000")
	at := time.Now()
	status, local, people := 200, true, 0
	for p := 0; p < w.pl.Scale.Programs; p++ {
		for page, got := 1, 0; got < 500; page++ {
			path := "/api/tracker/trackedEntities?program=" + fc.GenProgram(p) + "&orgUnit=" + fc.Facility +
				"&orgUnitMode=DESCENDANTS&includeDeleted=true&page=" + strconv.Itoa(page) + "&pageSize=50&paging=true" +
				"&updatedAfter=" + after +
				"&fields=trackedEntity,orgUnit,attributes[attribute,value],enrollments[enrollment,program,status,events[event,programStage,status,dataValues[dataElement,value]]]"
			st, h, b, _ := w.client(user).Try("GET", path, nil)
			local = local && h != nil && h.Get("X-Hub-Mode") != ""
			if st != 200 {
				status = st
				break
			}
			n := bytes.Count(b, []byte(`"trackedEntity":`))
			got += n
			people += n
			if n < 50 {
				break
			}
		}
	}
	w.mu.Lock()
	w.ops = append(w.ops, weekOp{w.phase, "android: incremental sync", at, time.Since(at), status, local, "", people})
	w.mu.Unlock()
}

// --- the reconnection ----------------------------------------------------------------

// countPush counts the objects each push carries (Netcut's rewrite hook; never
// matches).
func (w *week) countPush(r *http.Request) bool {
	if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/api/tracker") || r.Body == nil {
		return false
	}
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	var payload tracker.M
	if json.Unmarshal(b, &payload) != nil {
		return false
	}
	w.mu.Lock()
	for _, it := range tracker.Flatten(payload) {
		w.sent[it.UID]++
	}
	w.mu.Unlock()
	return false
}

// drain restores the uplink over link while staff go on working at a field rate, and
// times the push until the outbox is empty; then checks central.
func (w *week) drain(link weekLink) {
	e := w.e
	phase := "drain " + link.Name
	w.setPhase(phase)
	w.mu.Lock()
	w.sent = map[string]int{}
	w.mu.Unlock()
	e.Net.SetRate(link.Rate)
	e.Net.SetDelay(link.Delay)
	pending0 := pendingOf(w.p.Status())
	stop := w.fieldLoad(w.pl.DrainSaves)
	e.Net.Calls()
	t0 := time.Now()
	e.Net.SetDown(false)
	var online, drained time.Duration
	var series []string
	for time.Since(t0) < w.pl.Watch {
		s := w.p.Status()
		at := time.Since(t0)
		if s != nil {
			if online == 0 && tk.Online(s) {
				online = at
			}
			if int(at.Seconds())%60 == 0 {
				series = append(series, fmt.Sprintf("%.0f s: %d pending", at.Seconds(), pendingOf(s)))
			}
			if tk.Drained(s) {
				drained = at
				break
			}
		}
		time.Sleep(time.Second)
	}
	time.Sleep(30 * time.Second) // answers just after the drain
	stop()
	calls := e.Net.Calls()
	w.p.WaitFor("the outbox to drain after the load", 30*time.Minute, tk.Drained)
	calls = append(calls, e.Net.Calls()...)
	w.sample()
	var pushes, other int
	var central time.Duration
	for _, c := range calls {
		if c.Method == "POST" && c.URL.Path == "/api/tracker" && c.URL.Query().Get("atomicMode") == "OBJECT" {
			pushes++
			central += c.Dur
		} else {
			other++
		}
	}
	w.note("%s: %d objects pending; online after %.0f s; drained after %s; %d pushes (%.1f/s over the drain), central answered in %s in all; %d other requests to central",
		phase, pending0, online.Seconds(), drained.Round(time.Second), pushes, float64(pushes)/max(drained.Seconds(), 1),
		central.Round(time.Second), other)
	w.note("%s: pending each minute: %s", phase, strings.Join(series, "; "))
	w.note("%s: saves while draining: %s", phase, w.savesIn(phase))
	w.check(phase)
}

// fieldLoad is staff reading and Android devices syncing at field rates (ReadRate a
// second; each device every AndroidEvery), and saving at saves a second (0: none).
// Each kind has at most 32 requests open: a tick that finds them all open is
// skipped, and counted.
func (w *week) fieldLoad(saves float64) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	every := func(kind string, d time.Duration, f func(i int)) {
		wg.Add(1)
		sem := make(chan struct{}, 32)
		go func() {
			defer wg.Done()
			tick := time.NewTicker(d)
			defer tick.Stop()
			for i := 0; ; i++ {
				select {
				case <-done:
					return
				case <-tick.C:
					select {
					case sem <- struct{}{}:
					default:
						w.mu.Lock()
						w.skipped[w.phase+": "+kind]++
						w.mu.Unlock()
						continue
					}
					wg.Add(1)
					go func() { defer func() { <-sem; wg.Done() }(); f(i) }()
				}
			}
		}()
	}
	if saves > 0 {
		every("saves", time.Duration(float64(time.Second)/saves), func(i int) { w.save(i % w.pl.Users) })
	}
	every("reads", time.Duration(float64(time.Second)/w.pl.ReadRate), func(i int) { w.read(i % w.pl.Users) })
	if w.pl.Android > 0 {
		every("android syncs", w.pl.AndroidEvery/time.Duration(w.pl.Android), func(i int) { w.androidSync(i % w.pl.Android) })
	}
	var once sync.Once
	return func() { once.Do(func() { close(done); wg.Wait() }) }
}

// check reads every confirmed save back from central: each there, whole, by its
// author, with the value of the event's last confirmed save (or of a later one not
// confirmed); a registration not confirmed is there whole or not at all. It also
// counts objects a push carried more than once in this drain.
func (w *week) check(phase string) {
	// The hub of this drain holds the week's saves and its own: a drain before it
	// started from the same copy of the week.
	w.mu.Lock()
	var saves []*weekSave
	for _, s := range w.saves {
		if strings.HasPrefix(s.Phase, "day ") || s.Phase == phase {
			saves = append(saves, s)
		}
	}
	sent := maps.Clone(w.sent)
	w.mu.Unlock()
	uidsOf := func(s *weekSave) []string {
		switch s.Kind {
		case "register":
			return []string{s.TE, s.Enr, s.Event}
		case "enrol":
			return []string{s.Enr, s.Event}
		}
		return []string{s.Event}
	}
	// Sent more often than saved: each confirmed save is one outbox entry or one save
	// passed on, so an object goes out at most once per save that holds it.
	expected := map[string]int{}
	for _, s := range saves {
		if s.Confirmed {
			for _, u := range uidsOf(s) {
				expected[u]++
			}
		}
	}
	extra := 0
	for u, n := range sent {
		if e, ok := expected[u]; ok && n > e {
			extra += n - e
		}
	}
	var lost, partial, wrong []string
	checked := 0
	f := w.e.Fake
	author := func(o tracker.M) string {
		m, _ := o["createdBy"].(map[string]any)
		s, _ := m["username"].(string)
		return s
	}
	byEvent := map[string][]*weekSave{}
	for _, s := range saves {
		byEvent[s.Event] = append(byEvent[s.Event], s)
	}
	for _, s := range saves {
		uids := uidsOf(s)
		have := 0
		for _, u := range uids {
			if f.Object(u) != nil {
				have++
			}
		}
		if !s.Confirmed {
			if s.Kind != "edit" && have > 0 && have < len(uids) {
				partial = append(partial, fmt.Sprintf("%s %s (%d of %d)", s.Kind, s.Event, have, len(uids)))
			}
			continue
		}
		checked++
		if have < len(uids) {
			lost = append(lost, fmt.Sprintf("%s %s by %s at %s (%d of %d on central)", s.Kind, s.Event, s.User, s.At.Format("15:04:05"), have, len(uids)))
			continue
		}
		if s.Kind != "edit" {
			if a := author(f.Object(uids[0])); a != s.User {
				wrong = append(wrong, fmt.Sprintf("%s %s created by %q, saved by %s", s.Kind, uids[0], a, s.User))
			}
		}
	}
	for ev, ss := range byEvent {
		want := map[string]bool{}
		last := -1
		for i, s := range ss {
			if s.Confirmed {
				last = i
			}
		}
		if last < 0 {
			continue
		}
		for _, s := range ss[last:] {
			want[s.Value] = true
		}
		o := f.Object(ev)
		got := ""
		dvs, _ := o["dataValues"].([]any)
		for _, x := range dvs {
			if m, _ := x.(map[string]any); m["dataElement"] == weekDE {
				got, _ = m["value"].(string)
			}
		}
		if !want[got] {
			wrong = append(wrong, fmt.Sprintf("event %s: central %q, want one of %v", ev, got, want))
		}
	}
	w.note("%s: central holds %d confirmed saves checked: lost %d %v; partial %d %v; wrong %d %v; sent more often than saved %d (%d objects sent)",
		phase, checked, len(lost), firstN(lost), len(partial), firstN(partial), len(wrong), firstN(wrong), extra, len(sent))
	if len(lost)+len(partial)+len(wrong) > 0 {
		w.t.Errorf("%s: invariants broken", phase)
	}
}

func firstN(s []string) []string { return s[:min(len(s), 5)] }

func pendingOf(s map[string]any) int {
	n, _ := s["pending"].(float64)
	return int(n)
}

// --- sampling and the report -------------------------------------------------------

func (w *week) sampler() (stop func()) {
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(w.pl.Sample)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				w.sample()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func (w *week) sample() {
	now := time.Now()
	s := weekSample{At: now, Phase: w.curPhase(), Files: map[string]float64{}}
	if pid := w.p.Pid(); pid > 0 {
		s.PID, s.RSSMB, s.PeakRSSMB = pid, w.p.RSSMB(), w.p.PeakRSSMB()
		cpu := procCPU(pid)
		w.mu.Lock()
		prev, since := 0.0, w.lastAt
		if pid == w.lastPID {
			prev = w.lastCPU
		}
		if !since.IsZero() {
			s.CPUPct = 100 * (cpu - prev) / now.Sub(since).Seconds()
		}
		w.lastPID, w.lastCPU, w.lastAt = pid, cpu, now
		w.mu.Unlock()
		s.Goroutines, s.HeapMB = w.goStats()
	}
	filepath.Walk(w.e.Dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			mb := float64(fi.Size()) / 1e6
			s.DataMB += mb
			rel, _ := filepath.Rel(w.e.Dir, p)
			s.Files[strings.SplitN(rel, string(filepath.Separator), 2)[0]] += mb
		}
		return nil
	})
	if st := w.p.Status(); st != nil {
		s.Mode, _ = st["mode"].(string)
		s.Pending, _ = st["pending"].(float64)
		s.Refused, _ = st["rejected"].(float64)
	}
	w.mu.Lock()
	for _, x := range w.saves {
		s.Saves++
		if x.Confirmed {
			s.Confirmed++
		}
	}
	s.Reads = len(w.ops) - len(w.saves)
	w.samples = append(w.samples, s)
	w.mu.Unlock()
	w.t.Logf("%s sample: rss %.0f MB (peak %.0f), heap %.0f MB, cpu %.0f%%, goroutines %d; data %.0f MB %v; %s, %.0f pending, %.0f refused; %d saves (%d confirmed)",
		s.Phase, s.RSSMB, s.PeakRSSMB, s.HeapMB, s.CPUPct, s.Goroutines, s.DataMB, roundFiles(s.Files), s.Mode, s.Pending, s.Refused, s.Saves, s.Confirmed)
	w.writeAll()
}

func roundFiles(m map[string]float64) map[string]int {
	out := map[string]int{}
	for k, v := range m {
		out[k] = int(v + 0.5)
	}
	return out
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

// goStats reads the hub's goroutines and live heap from its profiler (HUB_PPROF).
func (w *week) goStats() (int, float64) {
	c := http.Client{Timeout: 10 * time.Second}
	get := func(path string) string {
		resp, err := c.Get("http://" + w.pprof + path)
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	var g int
	if m := regexp.MustCompile(`goroutine profile: total (\d+)`).FindStringSubmatch(get("/debug/pprof/goroutine?debug=1")); m != nil {
		g, _ = strconv.Atoi(m[1])
	}
	var heap float64
	if m := regexp.MustCompile(`# HeapInuse = (\d+)`).FindStringSubmatch(get("/debug/pprof/heap?debug=1&gc=1")); m != nil {
		heap, _ = strconv.ParseFloat(m[1], 64)
	}
	return g, heap / 1e6
}

func (w *week) savesIn(phase string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	kinds := map[string]int{}
	var n, conf, online int
	why := map[string]int{}
	for _, s := range w.saves {
		if phase != "" && s.Phase != phase {
			continue
		}
		n++
		kinds[s.Kind]++
		if s.Confirmed {
			conf++
			if s.Online {
				online++
			}
		} else {
			why[s.Err[:min(len(s.Err), 120)]]++
		}
	}
	return fmt.Sprintf("%d saves %v, %d confirmed (%d by central); not confirmed: %v", n, kinds, conf, online, why)
}

func (w *week) writeAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	lines := func(name string, n int, item func(i int) any) {
		var buf bytes.Buffer
		for i := range n {
			b, _ := json.Marshal(item(i))
			buf.Write(b)
			buf.WriteByte('\n')
		}
		os.WriteFile(filepath.Join(w.out, name), buf.Bytes(), 0o644)
	}
	lines("samples.jsonl", len(w.samples), func(i int) any { return w.samples[i] })
	lines("ops.jsonl", len(w.ops), func(i int) any { return w.ops[i] })
	lines("ledger.jsonl", len(w.saves), func(i int) any { return w.saves[i] })
	os.WriteFile(filepath.Join(w.out, "notes.txt"), []byte(strings.Join(w.notes, "\n")+"\n"), 0o644)
}

// summary writes, per phase and kind of operation, the count and the answer times,
// and per phase the process's memory, CPU and data folder.
func (w *week) summary() {
	w.mu.Lock()
	type key struct{ phase, kind string }
	by := map[key][]time.Duration{}
	errs := map[key]int{}
	var phases []string
	for _, o := range w.ops {
		k := key{o.Phase, o.Kind}
		by[k] = append(by[k], o.Dur)
		if o.Status != 200 {
			errs[k]++
		}
		if !slices.Contains(phases, o.Phase) {
			phases = append(phases, o.Phase)
		}
	}
	out := []string{"plan: " + w.pl.String(), "", fmt.Sprintf("%-16s %-28s %7s %8s %8s %8s %8s %6s", "phase", "operation", "count", "p50", "p95", "p99", "max", "errors")}
	var keys []key
	for k := range by {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int {
		if c := slices.Index(phases, a.phase) - slices.Index(phases, b.phase); c != 0 {
			return c
		}
		return strings.Compare(a.kind, b.kind)
	})
	for _, k := range keys {
		ds := by[k]
		slices.Sort(ds)
		q := func(f float64) string {
			return ds[min(len(ds)-1, int(f*float64(len(ds))))].Round(time.Millisecond).String()
		}
		out = append(out, fmt.Sprintf("%-16s %-28s %7d %8s %8s %8s %8s %6d", k.phase, k.kind, len(ds), q(0.5), q(0.95), q(0.99),
			ds[len(ds)-1].Round(time.Millisecond), errs[k]))
	}
	out = append(out, "", fmt.Sprintf("%-16s %10s %10s %10s %10s %10s %10s", "phase", "rss max", "peak rss", "heap max", "cpu mean", "data end", "pending"))
	for _, ph := range phases {
		var rss, peak, heap, cpu, data, pend float64
		n := 0
		for _, s := range w.samples {
			if s.Phase != ph {
				continue
			}
			rss, peak, heap = max(rss, s.RSSMB), max(peak, s.PeakRSSMB), max(heap, s.HeapMB)
			cpu += s.CPUPct
			n++
			data, pend = s.DataMB, s.Pending
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("%-16s %8.0f MB %8.0f MB %8.0f MB %9.0f%% %8.0f MB %10.0f", ph, rss, peak, heap, cpu/float64(n), data, pend))
		}
	}
	out = append(out, "", fmt.Sprintf("field load skipped (all its requests open): %v", w.skipped), "", "notes:")
	out = append(out, w.notes...)
	w.mu.Unlock()
	text := strings.Join(out, "\n") + "\n"
	os.WriteFile(filepath.Join(w.out, "summary.txt"), []byte(text), 0o644)
	w.t.Log("\n" + text)
	w.t.Logf("results in %s", w.out)
}

func (w *week) keepLog() {
	if b, err := os.ReadFile(w.p.Log); err == nil {
		os.WriteFile(filepath.Join(w.out, "hub.log"), b, 0o644)
	}
	w.writeAll()
}
