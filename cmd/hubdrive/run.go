package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	mrand "math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// syncImport is the tracker import the Capture apps make (synchronous, all or nothing).
const syncImport = "/api/tracker?async=false&importStrategy=CREATE_AND_UPDATE&atomicMode=ALL"

// Kinds of save.
const (
	kindRegister = "register" // a new person, enrolled, with one event in the registration stage
	kindEvent    = "event"    // a new event in the event stage, for a person registered before
	kindUpdate   = "update"   // a changed value of an event saved before
)

// record is one line of the ledger: a save a client attempted and the hub's answer.
type record struct {
	At        time.Time         `json:"at"`
	Tag       string            `json:"tag"`
	Program   string            `json:"program"`
	OrgUnit   string            `json:"orgUnit"`
	User      string            `json:"user"`
	Client    string            `json:"client"` // web (session) or android (Basic auth)
	Kind      string            `json:"kind"`
	TE        string            `json:"te"`
	Enr       string            `json:"enrollment"`
	Event     string            `json:"event"`
	Stage     string            `json:"stage"`
	Attrs     map[string]string `json:"attrs,omitempty"` // a registration's attributes
	Values    map[string]string `json:"values"`          // the event's data values as saved
	DE        string            `json:"de,omitempty"`    // the value this save sets or changes
	Confirmed bool              `json:"confirmed"`       // a 200 with an OK import report, nothing ignored
	ByHub     bool              `json:"byHub,omitempty"` // the hub answered itself (offline), not central
	Status    int               `json:"status"`
	Answer    string            `json:"answer,omitempty"` // why it is not confirmed
	Ms        int64             `json:"ms"`               // how long the client waited
}

type runConfig struct {
	Hub      string
	RT       http.RoundTripper
	Timeout  time.Duration
	Users    []string
	Password string
	Web      bool
	Android  bool

	Program, OrgUnit, TET string
	FirstAttr, LastAttr   string
	Attrs                 map[string]string
	RegStage, RegDE       string
	RegValues             map[string]string
	EventStage, EventDE   string
	EventValues           map[string]string
	Lo, Hi                int

	Pace   time.Duration
	Tag    string
	Ledger string
	Log    *log.Logger
}

func runMain(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	hf := addHubFlags(fs, 90*time.Second)
	users := fs.String("users", "ngelehun1,ngelehun2,ngelehun3", "staff users who save, comma-separated")
	password := fs.String("password", env("HUBDRIVE_PASSWORD", ""), "their password (or HUBDRIVE_PASSWORD)")
	clients := fs.String("clients", "web,android", "the clients each user saves with: web (Capture web, session login), android (Basic auth)")
	program := fs.String("program", "IpHINAT79UW", "tracker program (Child Programme)")
	ou := fs.String("orgunit", "DiszpKrYNg8", "the hub's facility (Ngelehun CHC)")
	tet := fs.String("tet", "nEenWmSyUEp", "the program's tracked entity type (Person)")
	first := fs.String("first-attr", "w75KJ2mc4zz", `attribute set to "HubTest" (First name)`)
	last := fs.String("last-attr", "zDhUuAYrxNC", "attribute set to the run tag and a number (Last name); check finds the run's people by it")
	attrs := fs.String("attrs", "cejWyOfXge6=Female", "other attributes of a registration, uid=value,…")
	regStage := fs.String("reg-stage", "A03MvHHogjR", "stage of the event saved with a registration (Birth)")
	regDE := fs.String("reg-de", "UXz7xuGCEhU", "numeric data element of that event, set and later changed (MCH Weight (g))")
	regValues := fs.String("reg-values", "a3kGcGDCuk6=8,bx6fsa0t90x=true", "its other data values (Apgar score, BCG dose)")
	evStage := fs.String("event-stage", "ZzYYXq4fJie", `stage of the event added to a person registered before (Baby Postnatal; "" for none)`)
	evDE := fs.String("event-de", "GQY2lXrypjO", `numeric data element of that event, set and later changed (MCH Infant Weight (g); "" for none)`)
	evValues := fs.String("event-values", "X8zyunlgUfM=Exclusive", "its other data values (MCH Infant Feeding)")
	rng := fs.String("range", "2500-4500", "the numbers saved in -reg-de and -event-de")
	pace := fs.Duration("pace", 5*time.Second, "pause between two saves of one client")
	tag := fs.String("tag", "", `run tag in every person's last name (default "r" and the date and time, e.g. r1028-1532)`)
	ledger := fs.String("ledger", "", "ledger file, appended to (default hubdrive-TAG.jsonl)")
	fs.Parse(args)

	if *hf.hub == "" || *password == "" {
		return fmt.Errorf("run needs -hub and -password")
	}
	rt, err := hf.transport()
	if err != nil {
		return err
	}
	c := runConfig{Hub: hf.url(), RT: rt, Timeout: *hf.timeout, Password: *password,
		Program: *program, OrgUnit: *ou, TET: *tet, FirstAttr: *first, LastAttr: *last, RegStage: *regStage, RegDE: *regDE,
		EventStage: *evStage, EventDE: *evDE, Pace: *pace, Tag: *tag, Ledger: *ledger}
	for _, u := range strings.Split(*users, ",") {
		if u = strings.TrimSpace(u); u != "" {
			c.Users = append(c.Users, u)
		}
	}
	c.Web, c.Android = strings.Contains(*clients, "web"), strings.Contains(*clients, "android")
	if c.Attrs, err = values(*attrs); err == nil {
		if c.RegValues, err = values(*regValues); err == nil {
			c.EventValues, err = values(*evValues)
		}
	}
	if err == nil {
		c.Lo, c.Hi, err = span(*rng)
	}
	if err != nil {
		return err
	}
	if c.Tag == "" {
		c.Tag = "r" + time.Now().Format("0102-1504")
	}
	if c.Ledger == "" {
		c.Ledger = "hubdrive-" + c.Tag + ".jsonl"
	}
	logf, err := os.OpenFile(strings.TrimSuffix(c.Ledger, ".jsonl")+".log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	c.Log = log.New(io.MultiWriter(os.Stdout, logf), "", log.Ltime)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() { <-ctx.Done(); stop() }() // a second Ctrl-C ends it at once
	_, err = drive(ctx, c)
	return err
}

// drive saves through the hub until ctx ends and returns the totals.
func drive(ctx context.Context, c runConfig) (*totals, error) {
	led, err := os.OpenFile(c.Ledger, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer led.Close()
	d := &driver{c: c, led: led, tot: newTotals()}
	c.Log.Printf("run %s: %d users × %v, program %s at %s, a save per client every %s; ledger %s",
		c.Tag, len(c.Users), clientKinds(c), c.Program, c.OrgUnit, c.Pace, c.Ledger)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); d.watch(ctx) }()
	for i, u := range c.Users {
		for _, kind := range clientKinds(c) {
			w := d.writer(u, kind, i)
			wg.Add(1)
			go func() { defer wg.Done(); w.loop(ctx) }()
		}
	}
	wg.Wait()
	d.tot.report(c.Log)
	c.Log.Printf("ledger %s; check it with: hubdrive check -hub %s … %s", c.Ledger, c.Hub, c.Ledger)
	return d.tot, nil
}

func clientKinds(c runConfig) []string {
	var out []string
	if c.Web {
		out = append(out, "web")
	}
	if c.Android {
		out = append(out, "android")
	}
	return out
}

type driver struct {
	c   runConfig
	mu  sync.Mutex
	led *os.File
	tot *totals
}

// append writes a save to the ledger, on disk before the next.
func (d *driver) append(r record) {
	b, _ := json.Marshal(r)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.led.Write(append(b, '\n'))
	d.led.Sync()
}

// --- clients ---------------------------------------------------------------------

type writer struct {
	d      *driver
	user   string
	kind   string // web or android
	http   *http.Client
	n      int // people registered
	id     string
	login  bool     // a web session is needed
	retry  *attempt // a save not confirmed, sent again as the apps do
	mine   []record // confirmed saves of events with a number to change
	noEv   []record // confirmed registrations without an added event yet
	reason string   // the last reason a save was not confirmed, logged once
}

// attempt is a save and its payload, kept to be sent again.
type attempt struct {
	r       record
	payload map[string]any
}

func (d *driver) writer(user, kind string, i int) *writer {
	w := &writer{d: d, user: user, kind: kind, id: fmt.Sprintf("%s%d", kind[:1], i+1),
		http: &http.Client{Transport: d.c.RT, Timeout: d.c.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if kind == "web" {
		w.http.Jar, _ = cookiejar.New(nil)
		w.login = true
	}
	return w
}

func (w *writer) loop(ctx context.Context) {
	first := time.Duration(mrand.Int64N(int64(w.d.c.Pace) + 1)) // clients out of step
	for sleep(ctx, first); ctx.Err() == nil; sleep(ctx, w.pause()) {
		w.once()
	}
}

func (w *writer) pause() time.Duration {
	p := w.d.c.Pace
	return p/2 + time.Duration(mrand.Int64N(int64(p)+1))
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// do sends a request to the hub as this client.
func (w *writer) do(method, path string, body any) (int, http.Header, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, w.d.c.Hub+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if w.kind == "android" {
		req.SetBasicAuth(w.user, w.d.c.Password)
		req.Header.Set("User-Agent", "okhttp/4.12.0")
	} else {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b, err
}

// once makes one save: the last one not confirmed again, else a new event for a
// person registered before (one in four), a changed value (one in four) or a new
// registration.
func (w *writer) once() {
	if w.login {
		st, _, b, err := w.do("POST", "/api/auth/login", map[string]string{"username": w.user, "password": w.d.c.Password})
		if err != nil || st != 200 || !bytes.Contains(b, []byte("SUCCESS")) {
			w.note(fmt.Sprintf("login: %s", answer(st, b, err)))
			w.d.tot.add(time.Now(), "login", false, false, answer(st, b, err))
			return
		}
		w.login = false
	}
	a := w.retry
	switch {
	case a != nil:
		a.r.At = time.Now()
	case len(w.noEv) > 0 && w.d.c.EventStage != "" && mrand.IntN(4) == 0:
		i := mrand.IntN(len(w.noEv))
		reg := w.noEv[i]
		w.noEv = append(w.noEv[:i], w.noEv[i+1:]...)
		a = w.event(reg)
	case len(w.mine) > 0 && mrand.IntN(4) == 0:
		a = w.update(w.mine[mrand.IntN(len(w.mine))])
	default:
		a = w.register()
	}
	st, h, b, err := w.do("POST", syncImport, a.payload)
	r := a.r
	r.Status, r.Ms = st, time.Since(r.At).Milliseconds()
	r.Confirmed = err == nil && accepted(st, b)
	r.ByHub = h.Get("X-Hub-Mode") != ""
	w.retry = nil
	if !r.Confirmed {
		r.Answer = answer(st, b, err)
		w.note(fmt.Sprintf("%s %s: %s", r.Kind, r.Event, r.Answer))
		if st == 401 || st == 302 || st == 403 {
			w.login = w.kind == "web"
		}
		// The apps keep a save that did not go through and send it again; one the
		// hub refused (an import error) is not sent again.
		if st != 409 && st != 400 {
			w.retry = &attempt{r: a.r, payload: a.payload}
		}
	} else if w.reason != "" {
		w.d.c.Log.Printf("%s %s (%s): saving again", w.user, w.kind, w.id)
		w.reason = ""
	}
	w.d.append(r)
	w.d.tot.add(r.At, r.Kind, r.Confirmed, r.ByHub, r.Answer)
	if r.Confirmed {
		switch r.Kind {
		case kindRegister:
			w.noEv = append(w.noEv, r)
			w.keep(r)
		case kindEvent:
			w.keep(r)
		}
	}
}

// keep remembers a confirmed event whose number later saves change.
func (w *writer) keep(r record) {
	for i, m := range w.mine {
		if m.Event == r.Event {
			w.mine[i] = r
			return
		}
	}
	if r.DE != "" {
		w.mine = append(w.mine, r)
	}
}

// note logs why a save was not confirmed, once for each new reason.
func (w *writer) note(why string) {
	short := why
	if i := strings.Index(short, ": "); i >= 0 {
		short = short[i+2:]
	}
	if len(short) > 60 {
		short = short[:60]
	}
	if short != w.reason {
		w.d.c.Log.Printf("%s %s (%s) not confirmed: %.300s", w.user, w.kind, w.id, why)
		w.reason = short
	}
}

func (w *writer) number() string {
	c := w.d.c
	return strconv.Itoa(c.Lo + mrand.IntN(c.Hi-c.Lo+1))
}

func (w *writer) base(kind string) record {
	c := w.d.c
	return record{At: time.Now(), Tag: c.Tag, Program: c.Program, OrgUnit: c.OrgUnit, User: w.user, Client: w.kind, Kind: kind}
}

func (w *writer) register() *attempt {
	c := w.d.c
	w.n++
	r := w.base(kindRegister)
	r.TE, r.Enr, r.Event, r.Stage, r.DE = uid(), uid(), uid(), c.RegStage, c.RegDE
	r.Attrs = map[string]string{}
	for k, v := range c.Attrs {
		r.Attrs[k] = v
	}
	r.Attrs[c.FirstAttr] = "HubTest"
	r.Attrs[c.LastAttr] = fmt.Sprintf("%s-%s-%s-%03d", c.Tag, w.user, w.id, w.n)
	r.Values = map[string]string{}
	for k, v := range c.RegValues {
		r.Values[k] = v
	}
	if c.RegDE != "" {
		r.Values[c.RegDE] = w.number()
	}
	var attrs []any
	for k, v := range r.Attrs {
		attrs = append(attrs, map[string]any{"attribute": k, "value": v})
	}
	today := r.At.Format("2006-01-02")
	ev := w.eventJSON(r)
	return &attempt{r: r, payload: map[string]any{"trackedEntities": []any{map[string]any{
		"trackedEntity": r.TE, "trackedEntityType": c.TET, "orgUnit": c.OrgUnit, "attributes": attrs,
		"enrollments": []any{map[string]any{"enrollment": r.Enr, "program": c.Program, "orgUnit": c.OrgUnit,
			"enrolledAt": today, "occurredAt": today, "status": "ACTIVE", "events": []any{ev}}}}}}}
}

func (w *writer) event(reg record) *attempt {
	c := w.d.c
	r := w.base(kindEvent)
	r.TE, r.Enr, r.Event, r.Stage, r.DE = reg.TE, reg.Enr, uid(), c.EventStage, c.EventDE
	r.Values = map[string]string{}
	for k, v := range c.EventValues {
		r.Values[k] = v
	}
	if c.EventDE != "" {
		r.Values[c.EventDE] = w.number()
	}
	return &attempt{r: r, payload: map[string]any{"events": []any{w.eventJSON(r)}}}
}

func (w *writer) update(prev record) *attempt {
	r := w.base(kindUpdate)
	r.TE, r.Enr, r.Event, r.Stage, r.DE = prev.TE, prev.Enr, prev.Event, prev.Stage, prev.DE
	r.Values = map[string]string{}
	for k, v := range prev.Values {
		r.Values[k] = v
	}
	r.Values[r.DE] = w.number()
	ev := w.eventJSON(r)
	ev["occurredAt"] = prev.At.Format("2006-01-02")
	return &attempt{r: r, payload: map[string]any{"events": []any{ev}}}
}

func (w *writer) eventJSON(r record) map[string]any {
	c := w.d.c
	dvs := []any{}
	for k, v := range r.Values {
		dvs = append(dvs, map[string]any{"dataElement": k, "value": v})
	}
	return map[string]any{"event": r.Event, "enrollment": r.Enr, "trackedEntity": r.TE, "program": c.Program,
		"programStage": r.Stage, "orgUnit": c.OrgUnit, "occurredAt": r.At.Format("2006-01-02"), "status": "ACTIVE",
		"dataValues": dvs}
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

func answer(st int, b []byte, err error) string {
	if ue, ok := err.(*url.Error); ok {
		err = ue.Err // the reason, without the long address
	}
	if err != nil {
		return err.Error()
	}
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		return fmt.Sprintf("%d %.300s", st, m.Message)
	}
	return fmt.Sprintf("%d %.300s", st, bytes.TrimSpace(b))
}

const uidChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func uid() string {
	b := make([]byte, 11)
	rand.Read(b)
	for i := range b {
		n := len(uidChars)
		if i == 0 {
			n = 52
		}
		b[i] = uidChars[int(b[i])%n]
	}
	return string(b)
}

// --- the timeline ----------------------------------------------------------------

// watch polls the hub's status page every 2 seconds and logs every change (up or down,
// online or offline, locked), and a line of counts every minute.
func (d *driver) watch(ctx context.Context) {
	c := &http.Client{Transport: d.c.RT, Timeout: 5 * time.Second}
	last, since := "", time.Now()
	minute := time.Now().Truncate(time.Minute)
	for ctx.Err() == nil {
		state, pending := hubState(c, d.c.Hub)
		if state != last {
			if last != "" {
				d.c.Log.Printf("hub: %s (was %s for %s)", state, last, time.Since(since).Round(time.Second))
			} else {
				d.c.Log.Printf("hub: %s", state)
			}
			d.tot.event(state)
			last, since = state, time.Now()
		}
		if m := time.Now().Truncate(time.Minute); m.After(minute) {
			d.c.Log.Printf("minute %s: %s; hub %s, %s pending", minute.Format("15:04"), d.tot.minute(minute), state, pending)
			minute = m
		}
		sleep(ctx, 2*time.Second)
	}
}

// hubState is the hub's state in a word or three, and its pending count.
func hubState(c *http.Client, hub string) (string, string) {
	resp, err := c.Get(hub + "/hub/status")
	if err != nil {
		return "down", "?"
	}
	defer resp.Body.Close()
	var s map[string]any
	if json.NewDecoder(resp.Body).Decode(&s) != nil {
		return fmt.Sprintf("up, status %d", resp.StatusCode), "?"
	}
	state := fmt.Sprintf("up, %v", s["mode"])
	if s["locked"] == true {
		state += ", locked"
	}
	if s["setup"] == true {
		state += ", not set up"
	}
	return state, fmt.Sprint(s["pending"])
}

// --- totals ----------------------------------------------------------------------

type totals struct {
	mu      sync.Mutex
	all     map[string]*count // by kind
	minutes map[time.Time]map[string]*count
	why     map[string]int
	events  map[string]int // timeline states entered
}

type count struct{ confirmed, byHub, not int }

func newTotals() *totals {
	return &totals{all: map[string]*count{}, minutes: map[time.Time]map[string]*count{}, why: map[string]int{}, events: map[string]int{}}
}

func (t *totals) add(at time.Time, kind string, confirmed, byHub bool, why string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := at.Truncate(time.Minute)
	if t.minutes[m] == nil {
		t.minutes[m] = map[string]*count{}
	}
	for _, c := range []map[string]*count{t.all, t.minutes[m]} {
		if c[kind] == nil {
			c[kind] = &count{}
		}
		switch {
		case confirmed && byHub:
			c[kind].confirmed++
			c[kind].byHub++
		case confirmed:
			c[kind].confirmed++
		default:
			c[kind].not++
		}
	}
	if !confirmed {
		if len(why) > 70 {
			why = why[:70]
		}
		t.why[why]++
	}
}

func (t *totals) event(state string) {
	t.mu.Lock()
	t.events[state]++
	t.mu.Unlock()
}

func (t *totals) minute(m time.Time) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return line(t.minutes[m])
}

func line(cs map[string]*count) string {
	var parts []string
	for _, k := range []string{kindRegister, kindEvent, kindUpdate, "login"} {
		c := cs[k]
		if c == nil {
			continue
		}
		s := fmt.Sprintf("%s %d confirmed (%d by the hub alone)", k, c.confirmed, c.byHub)
		if k == "login" {
			s = "login"
		}
		if c.not > 0 {
			s += fmt.Sprintf(", %d not", c.not)
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "no saves"
	}
	return strings.Join(parts, "; ")
}

func (t *totals) confirmed() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, c := range t.all {
		n += c.confirmed
	}
	return n
}

func (t *totals) report(l *log.Logger) {
	t.mu.Lock()
	defer t.mu.Unlock()
	l.Printf("totals: %s", line(t.all))
	var whys []string
	for w := range t.why {
		whys = append(whys, w)
	}
	sort.Slice(whys, func(i, j int) bool { return t.why[whys[i]] > t.why[whys[j]] })
	for _, w := range whys {
		l.Printf("  not confirmed %4d× %s", t.why[w], w)
	}
	var st []string
	for s, n := range t.events {
		st = append(st, fmt.Sprintf("%s %d×", s, n))
	}
	sort.Strings(st)
	l.Printf("hub states entered: %s", strings.Join(st, "; "))
}
