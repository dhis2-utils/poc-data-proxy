package adversarial

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/hubaccount"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// M2: a busy working day on which the hub's heavy housekeeping comes on top of the
// work: retention set and then tightened on central (each prune runs in a sync cycle
// while staff save), full pulls (full_pull_interval shortened), and an upgrade chosen
// on central during an outage, applied when the uplink returns with saves queued.
// Three writers and a reader (the soak's clients, soak_test.go) work through it all.
// Must hold: I1–I8: every confirmed save on central once, whole, by its author; the
// hub refuses nothing it should take; every person staff registered is still theirs
// to open offline at the end (retention keeps what is active, a full pull keeps what
// was saved during it); reads and saves stay fast; the upgrade waits for the outbox.
//
//	HUB_M2=40m (the default) is the length of the day; results in testresults/m2/<tag>/.
//	HUB_M2_SEED=1500 people are added to central first (m2Seed); HUB_M2_PG names
//	central's database ('host=… user=dhis password=dhis dbname=dhis2') to age them.
func TestM2BusyDay(t *testing.T) {
	d := 40 * time.Minute
	if v := os.Getenv("HUB_M2"); v != "" {
		var err error
		if d, err = time.ParseDuration(v); err != nil || d < 10*time.Minute {
			t.Fatalf("HUB_M2=%q: want a duration of at least 10m", v)
		}
	}
	ch := newChannel(t)
	cur := ch.build("", "0.2.0")
	next := ch.build("", "0.3.0")
	e := tk.Start(t)
	r := newRun(t, e)
	// Housekeeping at soak pace; a full pull every quarter of the day.
	fullEvery := d / 4
	e.ProcConfig = []string{
		"sync_interval: 1m",
		"ping_interval: 15s",
		"ping_timeout: 5s",
		"reconnect_delay: 5s",
		"ownership_check_interval: 2m",
		"access_refresh_interval: 2m",
		"full_pull_interval: " + fullEvery.String(),
		"session_max: 20m",
		"reserved_pool_size: 20",
	}
	e.Central.Do("DELETE", "/api/dataStore/facility-hub-config/retention", nil) // start with none
	seed := 1500
	if v := os.Getenv("HUB_M2_SEED"); v != "" {
		seed, _ = strconv.Atoi(v)
	}
	aged := 0
	if seed > 0 {
		aged = m2Seed(t, e, r, seed, os.Getenv("HUB_M2_PG"))
	}
	r.p = e.DetachRelease(cur)
	t.Cleanup(r.keepLog)
	e.SetupOnly(tk.RoleAdmin)
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if _, ok := e.M.User(role); ok {
			e.Web(role).Login()
		}
	}
	setup := time.Now()
	r.p.WaitFor("the first pull", 15*time.Minute, func(s map[string]any) bool { return s["lastPull"] != nil && s["syncing"] == false })
	t.Logf("set up on %v; first pull %s; run tag %s; hub log %s", r.p.Status()["version"], time.Since(setup).Round(time.Second), r.tag, r.p.Log)
	hubID := r.p.Status()["hubId"].(string)
	// Retention and the release choice count only signed for an approved hub (a hub not
	// approved uses the default retention, which keeps everything).
	moveKey := approveProc(t, e, hubID)
	setRetention := func(v string) {
		const path = "/api/dataStore/facility-hub-config/retention"
		b := signedFor(e, hubID, moveKey, "facility-hub-config/retention", json.RawMessage(v))
		if st, _, _ := e.Central.Do("PUT", path, b); st == 404 {
			e.Central.Do("POST", path, b)
		}
	}
	t.Cleanup(func() { e.Central.Do("DELETE", "/api/dataStore/facility-hub-config/retention", nil) })

	out := filepath.Join("..", "..", "testresults", "m2", time.Now().Format("20060102-1504")+"-"+r.tag)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	pl := soakPlan{D: d, s: 1, SampleEvery: 2 * time.Minute, Pause: 3 * time.Second, ReadEvery: 2 * time.Second}
	so := &soak{r: r, pl: pl, out: out, start: time.Now()}
	so.writeJSON("run.json", map[string]any{"tag": r.tag, "program": r.prog.ID, "stage": r.stage, "dataElement": r.de,
		"duration": d.String(), "config": e.ProcConfig, "central": e.M.CentralURL, "hubLog": r.p.Log})
	t.Cleanup(func() { so.writeLedger() })

	// The day, as fractions of it.
	at := func(f float64) time.Duration { return time.Duration(f * float64(d)) }
	type step struct {
		At   time.Duration
		What string
		Do   func()
	}
	var offered, back time.Time
	var queuedAtUp any
	steps := []step{
		{at(0.08), "retention of 12 months set on central", func() { setRetention(`{"defaultMonths": 12}`) }},
		{at(0.30), "uplink down", func() { e.Net.SetDown(true) }},
		{at(0.35), "release 0.3.0 chosen on central", func() {
			ch.publish("0.3.0", next)
			chooseSigned(e, "0.3.0", hubID, moveKey)
			offered = time.Now()
		}},
		{at(0.42), "uplink up", func() {
			queuedAtUp = r.p.Status()["pending"]
			e.Net.SetDown(false)
			back = time.Now()
		}},
		{at(0.62), "retention tightened to 6 months", func() { setRetention(`{"defaultMonths": 6}`) }},
	}

	stopW := so.write()
	stopR := so.read()
	so.sample()
	nextSample := pl.SampleEvery
	var swapped, onNew time.Time
	var lastOld map[string]any // the old release's last status before it went
	for i := 0; ; {
		el := time.Since(so.start)
		if el >= d {
			break
		}
		if !back.IsZero() && onNew.IsZero() {
			s := r.p.Status()
			switch {
			case s == nil && swapped.IsZero():
				swapped = time.Now()
			case s != nil && s["version"] == "0.3.0":
				onNew = time.Now()
				so.note("on 0.3.0 (pending %v)", s["pending"])
			case s != nil:
				lastOld = s
			}
		}
		if i < len(steps) && el >= steps[i].At {
			steps[i].Do()
			so.note("%s (%v objects pending)", steps[i].What, r.p.Status()["pending"])
			i++
			continue
		}
		if el >= nextSample {
			so.sample()
			nextSample += pl.SampleEvery
			continue
		}
		time.Sleep(200 * time.Millisecond)
	}
	stopW()
	stopR()
	e.Net.SetDown(false)
	so.sample()
	so.writeLedger()
	so.writeSamples()

	// The upgrade: after the outbox, with nothing pending.
	if onNew.IsZero() {
		t.Errorf("the hub did not upgrade to 0.3.0 during the day:\n%s", r.p.Tail(30))
	} else {
		gap := "answered every 200 ms poll"
		if !swapped.IsZero() {
			gap = "did not answer for " + fmtDur(onNew.Sub(swapped))
		}
		t.Logf("upgrade: chosen at +%s while offline; uplink back at +%s with %v objects pending; the old release last answered with pending %v; "+
			"on 0.3.0 %s after the uplink returned (the hub %s)", fmtDur(offered.Sub(so.start)), fmtDur(back.Sub(so.start)),
			queuedAtUp, lastOld["pending"], fmtDur(onNew.Sub(back)), gap)
		if lastOld["pending"] != float64(0) {
			t.Errorf("the old release went with %v objects pending", lastOld["pending"])
		}
		if n := r.p.Grep("upgrade result=ok from=0.2.0 to=0.3.0"); n != 1 {
			t.Errorf("upgrade result=ok logged %d times", n)
		}
	}

	c := r.finish(15 * time.Minute)
	so.sample()
	so.writeSamples()
	so.summary(c)

	// What the housekeeping did, from the hub's log.
	hk := busyHousekeeping(t, r.p.Log)
	so.writeJSON("housekeeping.json", hk)
	t.Logf("housekeeping: %s", hk)
	if aged > 0 && hk.Pruned < aged {
		t.Errorf("retention pruned %d objects; %d enrollments on central are older than the window", hk.Pruned, aged)
	}
	if hk.Pruned == 0 {
		t.Errorf("retention pruned nothing: the scenario did not test pruning")
	}
	if hk.FullPulls < 2 {
		t.Errorf("%d full pulls logged; want at least 2", hk.FullPulls)
	}

	// I5: nothing refused.
	if st := r.p.Status(); st["rejected"] != float64(0) {
		t.Errorf("I5: %v changes refused at the end", st["rejected"])
	}

	// I1/I8: every person registered is still the author's to open offline.
	var regs []netSave
	for _, s := range r.ledger.all() {
		if s.Confirmed && !s.Update {
			regs = append(regs, s)
		}
	}
	e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	clients := map[string]*tk.Client{}
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if _, ok := e.M.User(role); !ok {
			continue
		}
		c := e.Web(role)
		if st, _, b, err := c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": e.M.Password}); err != nil || st != 200 {
			t.Fatalf("offline login of %s: %d %v %.200s", c.User, st, err, b)
		}
		clients[c.User] = c
	}
	var hidden []string
	for _, s := range regs {
		if st, _ := clients[s.User].Do("GET", "/api/tracker/trackedEntities/"+s.TE+"?program="+r.prog.ID+"&fields=trackedEntity", nil); st != 200 {
			hidden = append(hidden, fmt.Sprintf("%s by %s at %s (online=%v, %d)", s.TE, s.User, s.At.Format("15:04:05"), s.Online, st))
		}
	}
	e.Net.SetDown(false)
	t.Logf("offline at the end: %d of %d registrations hidden from their author %v", len(hidden), len(regs), first(hidden))
	if len(hidden) > 0 {
		t.Errorf("I1/I8: %d of %d people registered during the day are hidden offline from their author", len(hidden), len(regs))
	}

	// Answers stay reasonable: reads and saves by window are in samples.jsonl; the
	// totals here.
	var reads, readErr int
	var lists, saves []time.Duration
	for _, rd := range so.reads {
		reads++
		if rd.Err != "" || rd.Status != 200 {
			readErr++
		} else if rd.Kind == "list" {
			lists = append(lists, rd.Dur)
		}
	}
	for _, s := range r.ledger.all() {
		if s.Confirmed {
			saves = append(saves, s.Dur)
		}
	}
	t.Logf("reads: %d, %d not 200; working list %s; confirmed saves waited %s", reads, readErr, durStats(lists), durStats(saves))
	so.writeJSON("result.json", map[string]any{"ledger": r.ledger.summary(), "central": c.String(), "housekeeping": hk,
		"hidden": hidden, "reads": reads, "readErrors": readErr, "lists": durStats(lists), "saves": durStats(saves)})
}

// busyDay is what the hub logged of its housekeeping.
type busyDay struct {
	FullPulls   int      // pulls that ran in full ("full pull removed visibility" or a full=true group)
	Removed     int      // objects whose visibility a full pull removed
	RemovedBy   []string // per log line: user and count
	Pruned      int      // events pruned, enrollments stubbed, objects deleted
	PruneLines  []string
	Upgrades    []string
	CycleErrors int
	CycleMaxMS  int
	SyncCycles  int
	WarnLines   map[string]int // warnings by message
}

func (b busyDay) String() string {
	return fmt.Sprintf("%d sync cycles (max %d ms, %d with errors); %d full pulls removing visibility of %d objects %v; pruned %d %v; upgrades %v; warnings %v",
		b.SyncCycles, b.CycleMaxMS, b.CycleErrors, b.FullPulls, b.Removed, b.RemovedBy, b.Pruned, b.PruneLines, b.Upgrades, b.WarnLines)
}

var (
	busyRemoved = regexp.MustCompile(`msg="full pull removed visibility" user=(\S+) objects=(\d+)`)
	busyPruned  = regexp.MustCompile(`msg="retention pruned (events|tracker)" (.*)$`)
	busyNum     = regexp.MustCompile(`=(\d+)`)
	busyMsg     = regexp.MustCompile(`msg="?([^"=]+?)"? (\w+=|$)`)
)

func busyHousekeeping(t *testing.T, log string) busyDay {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var out busyDay
	out.WarnLines = map[string]int{}
	fullSeen := map[string]bool{} // a full pull: one per cycle start time
	cycle := 0
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.Contains(ln, "msg=request ") {
			continue
		}
		if m := soakCycle.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
			out.SyncCycles++
			cycle++
			ms, _ := strconv.Atoi(m[1])
			out.CycleMaxMS = max(out.CycleMaxMS, ms)
			if m[2] != "<nil>" {
				out.CycleErrors++
			}
		}
		if m := busyRemoved.FindStringSubmatch(ln); m != nil {
			n, _ := strconv.Atoi(m[2])
			out.Removed += n
			out.RemovedBy = append(out.RemovedBy, fmt.Sprintf("cycle %d: %s %d", cycle, m[1], n))
			fullSeen[strconv.Itoa(cycle)] = true
		}
		if strings.Contains(ln, "full=true") {
			fullSeen[strconv.Itoa(cycle)] = true
		}
		if m := busyPruned.FindStringSubmatch(ln); m != nil {
			for _, n := range busyNum.FindAllStringSubmatch(m[2], -1) {
				x, _ := strconv.Atoi(n[1])
				out.Pruned += x
			}
			out.PruneLines = append(out.PruneLines, fmt.Sprintf("cycle %d: %s %s", cycle, m[1], m[2]))
		}
		if _, after, ok := strings.Cut(ln, "msg=upgrade result="); ok {
			out.Upgrades = append(out.Upgrades, "result="+after)
		}
		if strings.Contains(ln, "level=WARN") {
			msg := ln
			if m := busyMsg.FindStringSubmatch(ln); m != nil {
				msg = m[1]
			}
			out.WarnLines[msg[:min(len(msg), 80)]]++
		}
	}
	out.FullPulls = len(fullSeen)
	return out
}

// m2Seed gives the facility a history on central before the hub is set up: n people
// in the primary program, two in three with an enrollment completed two years ago
// (its last change moved back in central's database, as the API cannot), one in three
// active since this year. Retention then has old enrollments to prune, and a full
// pull has a facility's worth of people to list.
func m2Seed(t *testing.T, e *tk.Env, r *netRun, n int, pg string) (old int) {
	t.Helper()
	p := r.prog
	twoYears := time.Now().AddDate(-2, 0, 0).UTC().Format(time.DateOnly)
	thisYear := time.Now().AddDate(0, -2, 0).UTC().Format(time.DateOnly)
	var olds []string
	var batch []any
	flush := func() {
		if len(batch) == 0 {
			return
		}
		st, b, err := e.Central.Do("POST", "/api/tracker?async=false&importStrategy=CREATE_AND_UPDATE&atomicMode=OBJECT",
			map[string]any{"trackedEntities": batch})
		if err != nil || st != 200 {
			t.Fatalf("seeding central: %d %v %.800s", st, err, b)
		}
		batch = batch[:0]
	}
	for i := range n {
		pr := e.M.NewPerson(p, "Zqm2hist"+strings.ToLower(tk.UID()[:5]))
		te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
		enr := te["enrollments"].([]any)[0].(map[string]any)
		date, status := thisYear, "ACTIVE"
		if i%3 != 2 {
			date, status = twoYears, "COMPLETED"
			olds = append(olds, pr.Enrollment)
		}
		enr["enrolledAt"], enr["occurredAt"], enr["status"] = date, date, status
		ev := r.event(netSave{Event: tk.UID(), Enr: pr.Enrollment, Value: "50"})
		ev["occurredAt"], ev["status"] = date, "COMPLETED"
		enr["events"] = []any{ev}
		batch = append(batch, te)
		if len(batch) == 100 {
			flush()
		}
	}
	flush()
	if pg == "" {
		t.Logf("seeded %d people; HUB_M2_PG unset, so their enrollments are not aged and tracker retention has nothing to prune", n)
		return 0
	}
	var q strings.Builder
	q.WriteString("update enrollment set lastupdated = enrollmentdate, lastupdatedatclient = enrollmentdate where uid in (")
	for i, u := range olds {
		if i > 0 {
			q.WriteByte(',')
		}
		q.WriteString("'" + u + "'")
	}
	q.WriteString(")")
	out, err := exec.Command("psql", pg, "-v", "ON_ERROR_STOP=1", "-Atc", q.String()).CombinedOutput()
	if err != nil {
		t.Fatalf("ageing the seeded enrollments on central: %v %s", err, out)
	}
	t.Logf("seeded %d people on central, %d with an enrollment completed two years ago (%s)", n, len(olds), strings.TrimSpace(string(out)))
	return len(olds)
}

// Found in M2: an update the nurse made on Android, 20 s after the uplink returned,
// of a person she had registered offline, was refused by the hub itself with 409
// E1000 "needs connection: event … is not available offline". The hub answered
// locally because saves made during the push were waiting in the outbox
// (core.PassThrough), and the nurse could not read her own record: the push leaves
// it a local object (readable by its rules), but the first pull to fetch it after the
// push (here the admin's, listed before hers) stores it as central's, visible to that
// puller only (core.UpsertCentral), and no bit was ever set for its author. Until her
// own pull in the same cycle lists it, she is refused her own record. Fixed: the pull
// shares such objects by the hub's rules (core.upsertCentral; e2e
// TestM2AuthorKeepsOwnRecordsAfterPush).
//
// The scene: the nurse registers people offline; back online, one cycle runs with the
// uplink slowed so its pulls take a while, while the restricted user keeps saving
// (her saves wait in the outbox, so the hub answers tracker requests itself) and the
// nurse keeps reading and updating her people.
func TestM2AuthorKeepsOwnRecordsAfterPush(t *testing.T) {
	r := netPrepare(t)
	e := r.e
	e.Setup()
	nurse, restricted := e.Web(tk.RoleNurse), e.Web(tk.RoleRestricted)
	nurse.Login()
	restricted.Login()
	e.Sync()
	e.Offline()
	w := &netWriter{r: r, c: nurse, web: true}
	for len(w.mine) < 8 {
		w.once()
	}
	regs := slices.Clone(w.mine)
	e.Online()
	e.Net.SetDelay(150 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- e.App.Worker.Cycle(t.Context()) }()
	rw := &netWriter{r: r, c: restricted, web: true}
	hidden := map[string][]string{}
	var reads, local, updates, refused int
	start := time.Now()
	for running := true; running; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("the cycle: %v", err)
			}
			running = false
			continue
		default:
		}
		rw.once()
		for _, s := range regs {
			st, h, _, err := nurse.Try("GET", "/api/tracker/trackedEntities/"+s.TE+"?program="+r.prog.ID+"&fields=trackedEntity", nil)
			if err != nil {
				continue
			}
			reads++
			if h.Get("X-Hub-Mode") != "" {
				local++
				if st != 200 {
					hidden[s.TE] = append(hidden[s.TE], fmt.Sprintf("read %d at +%s", st, fmtDur(time.Since(start))))
				}
			}
		}
		before := len(r.ledger.all())
		w.once() // a registration or an update of one of hers
		if all := r.ledger.all(); len(all) > before {
			if s := all[len(all)-1]; s.Update {
				updates++
				if !s.Confirmed {
					refused++
					hidden[s.TE] = append(hidden[s.TE], fmt.Sprintf("update refused at +%s: %.120s", fmtDur(time.Since(start)), s.Err))
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.Net.SetDelay(0)
	t.Logf("one cycle of %s after %d offline registrations: %d reads of them by their author (%d answered by the hub), %d updates (%d refused); hidden from her: %d %v",
		fmtDur(time.Since(start)), len(regs), reads, local, updates, refused, len(hidden), hidden)
	if len(hidden) > 0 {
		t.Errorf("I1/I8: %d of the %d people the nurse registered offline were refused to her by the hub during the cycle after the push", len(hidden), len(regs))
	}
}

// approveProc approves the hub running in its own process as the Facility Hub app does
// (hubaccount.Approve as the superuser, with the manifest's hub accounts group and role
// and the code on the hub's page), waits until the hub has taken its account up, and
// returns its move key. The account and the key are deleted when the test ends.
func approveProc(t *testing.T, e *tk.Env, hubID string) []byte {
	t.Helper()
	admin := e.Web(tk.RoleAdmin)
	admin.Login()
	_, page := admin.Do("GET", "/hub/ui", nil)
	m := regexp.MustCompile(`read them this code: <span class="mono">([^<]+)</span>`).FindSubmatch(page)
	if m == nil {
		t.Fatal("no account code on the hub's page")
	}
	t.Cleanup(func() {
		var users struct{ Users []struct{ ID string } }
		e.Central.JSON("GET", "/api/users?fields=id&filter=username:ieq:"+hubID, nil, &users)
		for _, u := range users.Users {
			e.Central.Do("DELETE", "/api/users/"+u.ID, nil)
		}
		e.Central.Do("DELETE", "/api/dataStore/facility-hub-keys/"+hubID, nil)
	})
	if _, err := hubaccount.Approve(context.Background(), hubaccount.Options{Central: e.M.CentralURL,
		Username: e.M.AdminUser, Password: e.M.AdminPassword, Group: e.M.AccountsGroup, Role: e.M.AccountsRole},
		[]hubaccount.Request{{HubID: hubID, Code: string(m[1])}}); err != nil {
		t.Fatalf("approving the hub: %v", err)
	}
	var k struct {
		MoveKey string `json:"moveKey"`
	}
	if err := e.Central.JSON("GET", "/api/dataStore/facility-hub-keys/"+hubID, nil, &k); err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(k.MoveKey)
	if err != nil || len(key) == 0 {
		t.Fatalf("the hub's move key: %v", err)
	}
	for end := time.Now().Add(10 * time.Minute); ; time.Sleep(10 * time.Second) {
		var reg map[string]any
		e.Central.JSON("GET", "/api/dataStore/facility-hub-registry/"+hubID, nil, &reg)
		if reg["hubTokenOwner"] == hubID && reg["account"] == nil {
			return key
		}
		if time.Now().After(end) {
			t.Fatalf("the hub did not take its account up: %v", reg)
		}
	}
}

// signedFor is obj as the Facility Hub app writes it in the entry kind, with a code
// for hubID made with its move key.
func signedFor(e *tk.Env, hubID string, moveKey []byte, kind string, obj json.RawMessage) json.RawMessage {
	e.T.Helper()
	b, err := hubaccount.Sign(map[string][]byte{hubID: moveKey}, kind, obj, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		e.T.Fatal(err)
	}
	return b
}
