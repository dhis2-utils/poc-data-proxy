// Package adversarial runs the worst cases of docs/ADVERSARIAL-TESTING.md against the
// real DHIS2 in HUB_MANIFEST: clients keep saving through a hub while its network,
// its process or central fail, and every save the hub confirmed is then looked up on
// central.
package adversarial

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// netSave is one save a client attempted: a registration (person, enrollment and one
// event) or an update of that event's value.
type netSave struct {
	User      string
	TE, Enr   string
	Event     string
	Value     string // the event's value of netDE after this save
	Update    bool
	Confirmed bool // the hub answered with a report of everything stored
	Online    bool // central answered (the hub's own answers carry X-Hub-Mode)
	At        time.Time
	Status    int
	Err       string
}

// netLedger records every save of a scenario.
type netLedger struct {
	mu    sync.Mutex
	saves []netSave
}

func (l *netLedger) add(s netSave) {
	l.mu.Lock()
	l.saves = append(l.saves, s)
	l.mu.Unlock()
}

func (l *netLedger) all() []netSave {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]netSave(nil), l.saves...)
}

func (l *netLedger) summary() string {
	var conf, unconf, online, upd int
	for _, s := range l.all() {
		if s.Confirmed {
			conf++
			if s.Online {
				online++
			}
		} else {
			unconf++
		}
		if s.Update {
			upd++
		}
	}
	return fmt.Sprintf("%d saves: %d confirmed (%d by central, %d by the hub), %d not confirmed; %d of them updates",
		conf+unconf, conf, online, conf-online, unconf, upd)
}

// netRun is one scenario: a hub process, its uplink and the clients saving through it.
type netRun struct {
	t      *testing.T
	e      *tk.Env
	p      *tk.Proc
	prog   tk.Program
	stage  string
	de     string // the data element each save sets
	tag    string // in every person's name, to find them on central
	ledger *netLedger
}

// netStart sets a hub up for the facility in HUB_MANIFEST, logs the writers in once
// (so each has a key slot and can unlock the hub after a restart), and moves the hub
// to its own process.
func netStart(t *testing.T) *netRun {
	t.Helper()
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("adversarial scenarios run against a real DHIS2 (HUB_MANIFEST)")
	}
	r := &netRun{t: t, e: e, prog: e.M.Primary(), tag: "Zqadv" + strings.ToLower(tk.UID()[:6]), ledger: &netLedger{}}
	// A stage every writer may use, with a numeric value to change.
	for _, st := range r.prog.Stages {
		if st == r.prog.RestrictedStage {
			continue
		}
		for de, v := range r.prog.EventValues[st] {
			if _, err := strconv.Atoi(v); err == nil {
				r.stage, r.de = st, de
			}
		}
		if r.de != "" {
			break
		}
	}
	if r.de == "" {
		t.Skip("the primary program has no stage with a numeric value to save")
	}
	e.Setup()
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if _, ok := e.M.User(role); ok {
			e.Web(role).Login()
		}
	}
	e.Sync()
	r.p = e.Detach()
	r.p.WaitFor("online", time.Minute, tk.Online)
	t.Logf("run tag %s; hub log %s", r.tag, r.p.Log)
	return r
}

// netWriter is one client saving in a loop: new registrations, and updates of the
// value of events it registered before.
type netWriter struct {
	r    *netRun
	c    *tk.Client
	web  bool
	mine []netSave
}

func (r *netRun) writers() []*netWriter {
	var ws []*netWriter
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if _, ok := r.e.M.User(role); !ok {
			continue
		}
		ws = append(ws, &netWriter{r: r, c: r.e.Web(role), web: true})
	}
	ws = append(ws, &netWriter{r: r, c: r.e.Android(tk.RoleNurse)})
	return ws
}

// write starts the writers; the returned function stops them and waits.
func (r *netRun) write(pause time.Duration) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for _, w := range r.writers() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				w.once()
				time.Sleep(pause)
			}
		}()
	}
	var once sync.Once
	return func() { once.Do(func() { close(done); wg.Wait() }) }
}

// once makes one save: every third an update of an earlier confirmed event.
func (w *netWriter) once() {
	r := w.r
	s := netSave{User: w.c.User, Value: strconv.Itoa(30 + rand.N(60)), At: time.Now()}
	var payload map[string]any
	if len(w.mine) > 0 && rand.N(3) == 0 {
		prev := w.mine[rand.N(len(w.mine))]
		s.TE, s.Enr, s.Event, s.Update = prev.TE, prev.Enr, prev.Event, true
		payload = map[string]any{"events": []any{r.event(s)}}
	} else {
		pr := r.e.M.NewPerson(r.prog, r.tag+strings.ToLower(tk.UID()[:5]))
		s.TE, s.Enr, s.Event = pr.TE, pr.Enrollment, tk.UID()
		te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
		te["enrollments"].([]any)[0].(map[string]any)["events"] = []any{r.event(s)}
		payload = pr.Payload
	}
	st, hdr, b, err := w.post(payload)
	s.Status = st
	if err != nil {
		s.Err = err.Error()
	} else if st == 401 && w.web {
		w.c.Try("POST", "/api/auth/login", map[string]string{"username": w.c.User, "password": r.e.M.Password})
	}
	s.Confirmed = err == nil && accepted(st, b)
	s.Online = hdr == ""
	if !s.Confirmed && err == nil && st != 401 && st != 503 {
		s.Err = fmt.Sprintf("%d %.300s", st, b)
	}
	r.ledger.add(s)
	if s.Confirmed && !s.Update {
		w.mine = append(w.mine, s)
	}
}

func (w *netWriter) post(payload map[string]any) (int, string, []byte, error) {
	st, h, b, err := w.c.Try("POST", tk.SyncImport, payload)
	return st, h.Get("X-Hub-Mode"), b, err
}

func (r *netRun) event(s netSave) map[string]any {
	dvs := []any{}
	for de, v := range r.prog.EventValues[r.stage] {
		if de == r.de {
			v = s.Value
		}
		dvs = append(dvs, map[string]any{"dataElement": de, "value": v})
	}
	return map[string]any{"event": s.Event, "enrollment": s.Enr, "program": r.prog.ID, "programStage": r.stage,
		"orgUnit": r.e.M.Facility.ID, "occurredAt": s.At.UTC().Format("2006-01-02"), "status": "ACTIVE", "dataValues": dvs}
}

// accepted: the import report says everything was stored (cmd/hubload's rule).
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

// netCheck is what central holds of the ledger's saves.
type netCheck struct {
	Lost       []string // confirmed, not on central (or different)
	Partial    []string // an unconfirmed registration stored in part
	Duplicates []string
	Wrong      []string // on central with another value or author
	Checked    int
}

func (c netCheck) ok() bool {
	return len(c.Lost)+len(c.Partial)+len(c.Duplicates)+len(c.Wrong) == 0
}

func (c netCheck) String() string {
	return fmt.Sprintf("%d registrations checked; lost %d %v; partial %d %v; duplicates %d %v; wrong %d %v",
		c.Checked, len(c.Lost), first(c.Lost), len(c.Partial), first(c.Partial), len(c.Duplicates), first(c.Duplicates),
		len(c.Wrong), first(c.Wrong))
}

func first(s []string) []string {
	if len(s) > 5 {
		return s[:5]
	}
	return s
}

// check reads every registration of the ledger from central as the superuser: each
// confirmed one is there whole, by its author, with the value of the last confirmed
// save (or of a later unconfirmed one, which may have landed); each unconfirmed one
// is there whole or not at all; nobody of this run is on central twice.
func (r *netRun) check() netCheck {
	r.t.Helper()
	var out netCheck
	byEvent := map[string][]netSave{}
	var order []string
	for _, s := range r.ledger.all() {
		if byEvent[s.Event] == nil {
			order = append(order, s.Event)
		}
		byEvent[s.Event] = append(byEvent[s.Event], s)
	}
	for _, ev := range order {
		saves := byEvent[ev]
		reg := saves[0] // the registration comes first
		if reg.Update {
			continue
		}
		out.Checked++
		te := r.central("trackedEntities", reg.TE, "trackedEntity,createdBy,deleted")
		enr := r.central("enrollments", reg.Enr, "enrollment,deleted")
		evo := r.central("events", reg.Event, "event,createdBy,dataValues,deleted")
		have := 0
		for _, o := range []map[string]any{te, enr, evo} {
			if o != nil && o["deleted"] != true {
				have++
			}
		}
		confirmed := false
		for _, s := range saves {
			confirmed = confirmed || (s.Confirmed && !s.Update)
		}
		switch {
		case confirmed && have < 3:
			out.Lost = append(out.Lost, fmt.Sprintf("%s by %s at %s (%d of 3 on central)", reg.TE, reg.User, reg.At.Format("15:04:05.000"), have))
			continue
		case !confirmed && have > 0 && have < 3:
			out.Partial = append(out.Partial, fmt.Sprintf("%s (%d of 3)", reg.TE, have))
			continue
		case have == 0:
			continue
		}
		if u := username(te, "createdBy"); u != reg.User {
			out.Wrong = append(out.Wrong, fmt.Sprintf("%s created by %q, saved by %s", reg.TE, u, reg.User))
		}
		got := value(evo, r.de)
		if want := possible(saves); !want[got] {
			var hist []string
			for _, s := range saves {
				hist = append(hist, fmt.Sprintf("%s %s confirmed=%v byCentral=%v %s", s.At.Format("15:04:05.000"), s.Value, s.Confirmed, s.Online, s.Err))
			}
			out.Wrong = append(out.Wrong, fmt.Sprintf("event %s: central %q, want one of %v; saves: %s", ev, got, keys(want), strings.Join(hist, "; ")))
		}
	}
	// Duplicates: central holds each person of this run once, and each enrollment one
	// event.
	q := url.Values{"program": {r.prog.ID}, "orgUnits": {r.e.M.Facility.ID}, "fields": {"trackedEntity,enrollments[enrollment,events[event]]"},
		"filter": {r.prog.SearchAttr + ":like:" + r.tag}, "paging": {"false"}}
	var res struct {
		TEs []struct {
			TE   string `json:"trackedEntity"`
			Enrs []struct {
				Enr    string `json:"enrollment"`
				Events []any  `json:"events"`
			} `json:"enrollments"`
		} `json:"trackedEntities"`
	}
	if err := r.e.Central.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &res); err != nil {
		r.t.Fatalf("listing the run's people on central: %v", err)
	}
	seen := map[string]bool{}
	known := map[string]bool{}
	for _, s := range r.ledger.all() {
		known[s.TE] = true
	}
	for _, te := range res.TEs {
		if seen[te.TE] || !known[te.TE] {
			out.Duplicates = append(out.Duplicates, "person "+te.TE)
		}
		seen[te.TE] = true
		for _, en := range te.Enrs {
			if len(en.Events) > 1 {
				out.Duplicates = append(out.Duplicates, fmt.Sprintf("enrollment %s has %d events", en.Enr, len(en.Events)))
			}
		}
	}
	return out
}

// possible is the set of values central may hold for an event: the last confirmed
// save's, or any later unconfirmed one's.
func possible(saves []netSave) map[string]bool {
	last := -1
	for i, s := range saves {
		if s.Confirmed {
			last = i
		}
	}
	out := map[string]bool{}
	start := 0
	if last >= 0 {
		out[saves[last].Value] = true
		start = last + 1
	}
	for _, s := range saves[start:] {
		out[s.Value] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (r *netRun) central(kind, uid, fields string) map[string]any {
	st, b, err := r.e.Central.Do("GET", "/api/tracker/"+kind+"/"+uid+"?fields="+fields, nil)
	if err != nil {
		r.t.Fatalf("reading %s %s on central: %v", kind, uid, err)
	}
	if st != 200 {
		return nil
	}
	var o map[string]any
	json.Unmarshal(b, &o)
	return o
}

func username(o map[string]any, key string) string {
	m, _ := o[key].(map[string]any)
	s, _ := m["username"].(string)
	return s
}

func value(ev map[string]any, de string) string {
	dvs, _ := ev["dataValues"].([]any)
	for _, x := range dvs {
		if m, _ := x.(map[string]any); m["dataElement"] == de {
			s, _ := m["value"].(string)
			return s
		}
	}
	return ""
}

// finish waits for the hub to push everything by itself, checks central and reports.
func (r *netRun) finish(drain time.Duration) netCheck {
	r.t.Helper()
	start := time.Now()
	r.p.WaitFor("the outbox to drain", drain, tk.Drained)
	r.t.Logf("drained %s after the fault ended", time.Since(start).Round(time.Second))
	r.t.Logf("ledger: %s", r.ledger.summary())
	c := r.check()
	r.t.Logf("central: %s", c)
	r.keepLog()
	if !c.ok() {
		r.t.Errorf("invariants broken: %s", c)
	}
	return c
}

// keepLog copies the hub's log to testresults/adversarial, as the scenario's evidence.
func (r *netRun) keepLog() {
	dir := filepath.Join("..", "..", "testresults", "adversarial")
	os.MkdirAll(dir, 0o755)
	b, _ := os.ReadFile(r.p.Log)
	name := filepath.Join(dir, r.t.Name()+"-"+r.tag+".log")
	if os.WriteFile(name, b, 0o644) == nil {
		r.t.Logf("hub log kept in %s", name)
	}
}

// calls summarises what the hub asked central: requests per second, imports, pings.
func callStats(calls []tk.Call, d time.Duration) string {
	var imports, pings, faults int
	for _, c := range calls {
		switch {
		case c.Method == "POST" && c.URL.Path == "/api/tracker":
			imports++
		case strings.HasSuffix(c.URL.Path, "/api/system/ping"):
			pings++
		}
		if c.Fault != "" {
			faults++
		}
	}
	return fmt.Sprintf("%d requests in %s (%.1f/s): %d imports, %d pings, %d faulted",
		len(calls), d.Round(time.Second), float64(len(calls))/d.Seconds(), imports, pings, faults)
}
