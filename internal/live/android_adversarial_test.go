package live

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The Android scenarios of docs/ADVERSARIAL-TESTING.md (I3c, I4c, I5c, N21, N22): the
// real Capture app on a device (adb), against a live central, with data entered on
// the tablet through its forms. Set HUB_CLIENTS=1 to run them; they erase the app's
// data. The device reaches this machine on SANDBOX_HOST_PORT_2 through adb reverse,
// where a front routes by host name: http://127.0.0.1:<port> is central (through a
// plain proxy), http://localhost:<port> the hub, so one tablet can hold an account on
// each. N21 and N22 install other Capture versions from HUB_CAPTURE_APKS (a directory
// of the release APKs, dhis2-v<version>.apk) and leave the newest installed.

// Capture versions for N21 and N22: the version the hub was tested with (and the
// newest release), the one before it, and an older one still found in the field.
const (
	captureNewest   = "3.5.0"
	capturePrevious = "3.4.2"
	captureOld      = "3.2.1.2"
)

// tablet is one Android scenario: a hub, the device, and the front it reaches both
// central and the hub through.
type tablet struct {
	*tk.Env
	t       *testing.T
	a       *tk.Android
	central string // central as the app reaches it
	hub     string // the hub as the app reaches it
	tag     string // first names of this run's people start with it
	out     string
	// centralDown cuts the tablet's route to central, tabletDown all its routes (the
	// front drops the connection).
	centralDown, tabletDown atomic.Bool
}

func startTablet(t *testing.T, scenario string) *tablet {
	port := os.Getenv("SANDBOX_HOST_PORT_2")
	a := &tk.Android{Package: "com.dhis2"}
	switch {
	case os.Getenv("HUB_CLIENTS") == "":
		t.Skip("set HUB_CLIENTS=1 to drive the real Android Capture app")
	case port == "" || !a.Available():
		t.Skip("needs a device (adb) and SANDBOX_HOST_PORT_2")
	}
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST)")
	}
	host, err := a.HostFor(port)
	if err != nil {
		t.Fatal(err)
	}
	if host != "localhost" {
		t.Skip("needs a device on USB: the front tells central from the hub by the host name")
	}
	tb := &tablet{Env: e, t: t, a: a, central: "http://127.0.0.1:" + port, hub: "http://localhost:" + port,
		tag: "Adv" + strings.ToLower(scenario) + randLetters(4)}
	tb.out = t.TempDir()
	if p := os.Getenv("HUB_REPORT"); p != "" {
		tb.out = filepath.Join(filepath.Dir(p), "android-"+strings.ToLower(scenario))
		os.MkdirAll(tb.out, 0o755)
	}

	target, _ := url.Parse(e.M.CentralURL)
	toCentral := httputil.NewSingleHostReverseProxy(target)
	direct := toCentral.Director
	toCentral.Director = func(r *http.Request) { direct(r); r.Host = target.Host }
	toHub := &httputil.ReverseProxy{Director: func(r *http.Request) {
		u, _ := url.Parse(e.Srv.URL) // the hub's address changes when it reboots
		r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	}, FlushInterval: -1}
	drop := func(w http.ResponseWriter) {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				c.Close()
				return
			}
		}
		w.WriteHeader(http.StatusBadGateway)
	}
	front := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		toCentralHost := strings.HasPrefix(r.Host, "127.0.0.1")
		switch {
		case tb.tabletDown.Load(), toCentralHost && tb.centralDown.Load():
			drop(w)
		case toCentralHost:
			toCentral.ServeHTTP(w, r)
		default:
			toHub.ServeHTTP(w, r)
		}
	})
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: front}
	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
		tb.online() // never leave the tablet off the network
	})
	if err := a.SetWifi(true); err != nil {
		t.Fatal(err)
	}
	e.Setup()
	t.Logf("%s: people registered on the tablet are named %s01…", scenario, tb.tag)
	return tb
}

// offline takes the tablet off every network: Wi-Fi off, so the app knows it is
// offline, and the front drops its connections, since the app's requests would
// otherwise still reach the hub over USB (adb reverse; the reserved values manager
// tries whenever a form opens, and removing the reverse leaves open connections up).
func (tb *tablet) offline() {
	tb.t.Helper()
	tb.tabletDown.Store(true)
	if err := tb.a.SetWifi(false); err != nil {
		tb.t.Fatal(err)
	}
}

// online brings the tablet back on the network.
func (tb *tablet) online() {
	tb.t.Helper()
	tb.tabletDown.Store(false)
	if err := tb.a.SetWifi(true); err != nil {
		tb.t.Error(err)
	}
}

func randLetters(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.IntN(len(letters))]
	}
	return string(b)
}

// login resets the app and logs the nurse in to server, and returns the program's
// record count once the sync after login has settled.
func (tb *tablet) login(server string) int {
	tb.t.Helper()
	if err := tb.a.Reset(); err != nil {
		tb.t.Fatal(err)
	}
	return tb.loginOnly(server)
}

// loginOnly logs in on a freshly installed or reset app.
func (tb *tablet) loginOnly(server string) int {
	tb.t.Helper()
	program := tb.M.Primary().Name
	if _, err := tb.a.Login(server, tb.M.Users[tk.RoleNurse].Username, tb.M.Password, program, 10*time.Minute); err != nil {
		tb.t.Fatalf("login to %s: %v", server, err)
	}
	n, _ := settledCount(tb.a, program)
	tb.t.Logf("logged in to %s: %d records under %q", server, n, program)
	return n
}

// register registers count people on the tablet, named tag01, tag02…, in the
// primary program, and returns what the app showed for each.
func (tb *tablet) register(from, count int) []tk.Registration {
	tb.t.Helper()
	var out []tk.Registration
	start := time.Now()
	for i := from; i < from+count; i++ {
		r, err := tb.a.Register(tb.M.Primary().Name, fmt.Sprintf("%s%02d", tb.tag, i), "Tablet", "Synthetic patient number")
		if err != nil {
			tb.t.Fatalf("registering %s%02d on the tablet: %v", tb.tag, i, err)
		}
		if !r.Saved {
			tb.t.Logf("%s: not saved; the app shows: %s", r.First, r.Message)
		}
		out = append(out, r)
	}
	tb.t.Logf("registered %d people on the tablet in %s", count, time.Since(start).Round(time.Second))
	return out
}

// person is one of this run's people as a server holds them.
type person struct {
	TE, First, PatNo, CreatedBy string
	Enrollments, Events         int
}

// people lists this run's people on central (via nil client) or through the hub.
func (tb *tablet) people(c *tk.Client) ([]person, error) {
	p := tb.M.Primary()
	q := url.Values{"program": {p.ID}, "orgUnits": {tb.M.Facility.ID}, "pageSize": {"1000"},
		"filter":         {p.SearchAttr + ":like:" + tb.tag},
		"fields":         {"trackedEntity,createdBy[username],attributes[attribute,value],enrollments[enrollment,events[event]]"},
		"includeDeleted": {"false"}}
	path := "/api/tracker/trackedEntities?" + q.Encode()
	var st int
	var b []byte
	if c == nil {
		var err error
		if st, b, err = tb.Central.Do("GET", path, nil); err != nil {
			return nil, err
		}
	} else {
		st, _, b, _ = c.Try("GET", path, nil)
	}
	if st != 200 {
		return nil, fmt.Errorf("%d %.300s", st, b)
	}
	var page struct {
		TrackedEntities []struct {
			TrackedEntity string
			CreatedBy     struct{ Username string }
			Attributes    []struct{ Attribute, Value string }
			Enrollments   []struct {
				Events []struct{ Event string }
			}
		}
	}
	if err := json.Unmarshal(b, &page); err != nil {
		return nil, err
	}
	var out []person
	for _, te := range page.TrackedEntities {
		pr := person{TE: te.TrackedEntity, CreatedBy: te.CreatedBy.Username, Enrollments: len(te.Enrollments)}
		for _, a := range te.Attributes {
			switch a.Attribute {
			case p.SearchAttr:
				pr.First = a.Value
			case p.GeneratedAttr:
				pr.PatNo = a.Value
			}
		}
		for _, en := range te.Enrollments {
			pr.Events += len(en.Events)
		}
		out = append(out, pr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].First < out[j].First })
	return out, nil
}

// waitPeople waits until a server holds n of this run's people.
func (tb *tablet) waitPeople(c *tk.Client, n int, timeout time.Duration) []person {
	tb.t.Helper()
	deadline := time.Now().Add(timeout)
	var got []person
	for {
		var err error
		got, err = tb.people(c)
		if err != nil {
			tb.t.Logf("listing this run's people: %v", err)
		}
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Second)
	}
}

// sendAll syncs the tablet (home, Sync, Send) until a server holds n of this run's
// people, up to three times, as staff tap Sync again when nothing seems to happen: a
// tap while the app's own sync after login still runs starts nothing.
func (tb *tablet) sendAll(c *tk.Client, n int, each time.Duration) []person {
	tb.t.Helper()
	var got []person
	for try := 1; try <= 3; try++ {
		if err := tb.a.SyncAll(); err != nil {
			tb.t.Fatal(err)
		}
		if got = tb.waitPeople(c, n, each); len(got) >= n {
			if try > 1 {
				tb.t.Logf("the tablet's data arrived after %d taps on Sync", try)
			}
			return got
		}
	}
	return got
}

// once checks that central holds each saved registration exactly once, whole, by the
// nurse (I1, I2): every first name once, with its enrollment and its events, and no
// patient number twice.
func (tb *tablet) once(saved []tk.Registration, got []person) {
	tb.t.Helper()
	byName := map[string][]person{}
	byPatNo := map[string]int{}
	for _, p := range got {
		byName[strings.TrimSpace(p.First)] = append(byName[strings.TrimSpace(p.First)], p)
		if p.PatNo != "" {
			byPatNo[p.PatNo]++
		}
	}
	nurse := tb.M.Users[tk.RoleNurse].Username
	for _, r := range saved {
		ps := byName[r.First]
		switch {
		case len(ps) == 0:
			tb.t.Errorf("I1: %s, saved on the tablet, is not on central", r.First)
			continue
		case len(ps) > 1:
			tb.t.Errorf("I2: %s is on central %d times", r.First, len(ps))
		}
		p := ps[0]
		if p.Enrollments != 1 || p.Events == 0 {
			tb.t.Errorf("I1: %s on central has %d enrollments and %d events", r.First, p.Enrollments, p.Events)
		}
		if p.CreatedBy != nurse {
			tb.t.Errorf("I1: %s created by %q on central, want %s", r.First, p.CreatedBy, nurse)
		}
		if r.Generated != "" && p.PatNo != r.Generated {
			tb.t.Errorf("I1: %s has patient number %q on central; the tablet showed %q", r.First, p.PatNo, r.Generated)
		}
	}
	for v, n := range byPatNo {
		if n > 1 {
			tb.t.Errorf("I2: patient number %s on central %d times", v, n)
		}
	}
	if len(got) > len(saved) {
		tb.t.Errorf("I2: central holds %d of this run's people; the tablet saved %d", len(got), len(saved))
	}
	tb.t.Logf("central holds %d of this run's people for %d saved on the tablet (%d distinct patient numbers)", len(got), len(saved), len(byPatNo))
}

// refusals logs what the hub refused the app since the last call (I8), and returns it.
func (tb *tablet) refusals(phase string) []string {
	tb.t.Helper()
	counts := map[string]int{}
	for _, r := range tb.Requests() {
		if r.Status >= 400 && r.Status != 404 {
			counts[fmt.Sprintf("%d %s %s", r.Status, r.Method, r.Path)]++
		}
	}
	var lines []string
	for k, n := range counts {
		lines = append(lines, fmt.Sprintf("%s (x%d)", k, n))
	}
	sort.Strings(lines)
	for _, l := range lines {
		tb.t.Logf("%s: the hub refused %s", phase, l)
	}
	if len(lines) == 0 {
		tb.t.Logf("%s: the hub refused nothing (404s aside)", phase)
	}
	os.WriteFile(filepath.Join(tb.out, "refused-"+phase+".txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	return lines
}

// queue reports the hub's outbox and refused changes (I5).
func (tb *tablet) queue() (pending, issues int) {
	return tb.App.Hub.Store.CountOutbox(store.OutboxPending), tb.App.Hub.Store.CountIssues()
}

// cycle runs one sync cycle and logs its error rather than failing: a cycle reports
// errors that are not this scenario's, such as central refusing to reserve more
// values of an attribute whose sequence ran out (409).
func (tb *tablet) cycle() {
	tb.t.Helper()
	if err := tb.App.Worker.Cycle(context.Background()); err != nil {
		tb.t.Logf("sync: %v", err)
	}
}

// pushAll brings the hub online and runs cycles until its outbox is empty.
func (tb *tablet) pushAll() {
	tb.t.Helper()
	tb.Online()
	for i := range 5 {
		if err := tb.App.Worker.Cycle(context.Background()); err != nil {
			tb.t.Logf("sync: %v", err)
		}
		if p, _ := tb.queue(); p == 0 {
			tb.t.Logf("outbox empty after %d cycles", i+1)
			return
		}
	}
	p, n := tb.queue()
	tb.t.Errorf("I5: after 5 cycles the outbox holds %d, refused changes %d", p, n)
}

// I3c: a tablet working against central, offline, with unsynced registrations, is
// switched to the hub the way the app allows it: log out, add an account on the hub's
// address, log in. The app keeps one database per account, so the registrations stay
// with central's account: the hub never sees them. They reach central once when the
// tablet logs in to central's account again where central is reachable and syncs.
func TestI3cAndroidSwitchToHub(t *testing.T) {
	tb := startTablet(t, "I3c")
	const n = 3
	tb.login(tb.central)

	tb.offline()
	saved := tb.register(1, n)
	tb.centralDown.Store(true) // the facility network reaches the hub, not central
	tb.online()
	if err := tb.a.Logout(); err != nil {
		t.Fatal(err)
	}
	nodes, _ := tb.a.Screen()
	t.Logf("after logging out with %d unsynced registrations the app shows: %s", n, tk.Texts(nodes))
	if _, err := tb.a.AddAccount(tb.hub, tb.M.Users[tk.RoleNurse].Username, tb.M.Password, tb.M.Primary().Name, 10*time.Minute); err != nil {
		t.Fatalf("adding the hub's account: %v", err)
	}
	count, _ := settledCount(tb.a, tb.M.Primary().Name)
	t.Logf("on the hub's account: %d records", count)
	tb.a.SyncAll()
	time.Sleep(time.Minute)
	nurse := tb.AndroidUser(tb.M.Users[tk.RoleNurse].Username)
	onHub, _ := tb.people(nurse)
	onCentral, _ := tb.people(nil)
	t.Logf("after the switch and a sync on the hub's account: %d of %d on the hub, %d on central", len(onHub), n, len(onCentral))
	if len(onHub) != 0 || len(onCentral) != 0 {
		t.Errorf("the registrations of central's account reached the hub (%d) or central (%d) from the hub's account", len(onHub), len(onCentral))
	}
	tb.refusals("hub-account")

	// Back to central's account where central is reachable: the registrations go.
	tb.centralDown.Store(false)
	if err := tb.a.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := tb.a.SwitchAccount(tb.central, tb.M.Password, tb.M.Primary().Name, 5*time.Minute); err != nil {
		t.Fatalf("back to central's account: %v", err)
	}
	got := tb.sendAll(nil, n, 2*time.Minute)
	tb.once(saved, got)

	// The hub pulls them, and the hub's account shows each once.
	tb.cycle()
	if p, _ := tb.people(nurse); len(p) != n {
		t.Errorf("through the hub after its sync: %d of this run's people, want %d", len(p), n)
	}
	tb.Offline()
	if p, _ := tb.people(nurse); len(p) != n {
		t.Errorf("through the hub offline: %d of this run's people, want %d", len(p), n)
	}
	tb.Online()
	tb.cycle()
	tb.once(saved, tb.waitPeople(nil, n, time.Second))
}

// I4c: a tablet on the hub works offline for "weeks" (a backlog of registrations)
// while central changes under it (others register people at the facility on
// central). The tablet then syncs through the hub while the hub's own uplink is
// down, and the hub pushes later: every registration reaches central once, nothing
// is refused, and the tablet downloads what central gained.
func TestI4cAndroidLongOffline(t *testing.T) {
	tb := startTablet(t, "I4c")
	n := 25
	if v := os.Getenv("HUB_ANDROID_BACKLOG"); v != "" {
		fmt.Sscan(v, &n)
	}
	before := tb.login(tb.hub)
	tb.refusals("login")

	tb.offline()
	// Central meanwhile: others register people at the facility straight on central
	// (as the superuser: the manifest's other roles may not write the program), and the
	// hub pulls them.
	other := 200
	p := tb.M.Primary()
	for i := 0; i < other; i += 50 {
		var tes []any
		for j := i; j < i+50 && j < other; j++ {
			pr := tb.M.NewPerson(p, fmt.Sprintf("%sc%03d", tb.tag, j))
			tes = append(tes, pr.Payload["trackedEntities"].([]any)...)
		}
		if st, b, err := tb.Central.Do("POST", tk.SyncImport, map[string]any{"trackedEntities": tes}); err != nil || st != 200 {
			t.Fatalf("registering on central: %d %v %.500s", st, err, b)
		}
	}
	tb.cycle()
	saved := tb.register(1, n)

	tb.Offline() // the hub's uplink is down when the tablet comes back
	tb.online()
	start := time.Now()
	nurse := tb.AndroidUser(tb.M.Users[tk.RoleNurse].Username)
	onHub := tb.sendAll(nurse, n+other, 5*time.Minute)
	t.Logf("the hub holds %d of %d of this run's people %s after the tablet's sync", len(onHub), n+other, time.Since(start).Round(time.Second))
	// Offline the hub may refuse reads it cannot answer ("needs connection"); it must
	// take every save.
	for _, r := range tb.refusals("backlog") {
		if strings.Contains(r, " POST ") || strings.Contains(r, " PUT ") {
			t.Errorf("I5: the hub refused a save of the tablet's sync: %s", r)
		}
	}
	pending, issues := tb.queue()
	t.Logf("hub outbox: %d pending, %d refused changes", pending, issues)

	tb.pushAll()
	got := tb.waitPeople(nil, n+other, time.Minute)
	var mine []person // central's own people share the tag; once checks the tablet's
	for _, g := range got {
		if !strings.Contains(g.First, tb.tag+"c") {
			mine = append(mine, g)
		}
	}
	tb.once(saved, mine)
	if _, issues := tb.queue(); issues > 0 {
		t.Errorf("I5: %d refused changes after the push", issues)
	}
	// The tablet's download after the sync: what central gained.
	tb.a.SyncAll()
	time.Sleep(time.Minute)
	tb.a.Home()
	tb.a.ScrollTo(p.Name, 15)
	after, _ := settledCount(tb.a, p.Name)
	t.Logf("records under %q on the tablet: %d before, %d after (+%d of the tablet's, +%d registered on central)", p.Name, before, after, n, other)
	if after < before+n {
		t.Errorf("the tablet shows %d records after its sync, fewer than %d before plus its own %d", after, before, n)
	}
}

// I5c: the tablet's app data is wiped while the hub is offline (a reset or a
// replacement tablet), so at its next login the hub hands it the generated patient
// numbers from its own small pool; the tablet, offline, registers more people than
// it holds numbers. What the app shows when they run out is recorded (I8); then
// everything syncs through the hub to central, where no patient number may appear
// twice. The first login is online: offline, the hub serves a user only what that
// user loaded online (FINDINGS, "Shared metadata cache").
func TestI5cAndroidReservedValuesRunOut(t *testing.T) {
	tb := startTablet(t, "I5c")
	p := tb.M.Primary()
	if p.GeneratedAttr == "" {
		t.Skip("the primary program has no generated attribute")
	}
	tb.login(tb.hub)
	tb.refusals("login-online")
	var gen []string
	for _, a := range tb.App.Hub.Catalog().GeneratedAttributes() {
		gen = append(gen, fmt.Sprintf("%s (%s): %d", a.ID, a.Pattern, tb.App.Hub.Store.CountReservedValues(a.ID, tb.App.Hub.CentralTime(time.Now()))))
	}
	t.Logf("the hub's pool by generated attribute: %v", gen)
	pool := tb.App.Hub.Store.CountReservedValues(p.GeneratedAttr, tb.App.Hub.CentralTime(time.Now()))
	tb.Offline()
	tb.login(tb.hub)
	tb.refusals("login-offline")
	left := tb.App.Hub.Store.CountReservedValues(p.GeneratedAttr, tb.App.Hub.CentralTime(time.Now()))
	handed := pool - left
	t.Logf("the hub's pool of %s: %d before the tablet's login, %d after (%d handed to the tablet)", p.GeneratedAttr, pool, left, handed)

	tb.offline()
	regs := tb.register(1, handed+2)
	var saved []tk.Registration
	numbers := map[string]string{}
	for _, r := range regs {
		t.Logf("%s: saved=%v, patient number %q", r.First, r.Saved, r.Generated)
		if r.Saved {
			saved = append(saved, r)
		} else if !strings.Contains(r.Message, "no more reserved values") {
			t.Errorf("I8: %s not saved, and the app does not say the numbers ran out: %s", r.First, r.Message)
		} else {
			t.Logf("%s not saved; the app says: %q", r.First, "There are no more reserved values. Contact your system administrator")
		}
		if r.Generated != "" {
			if o, ok := numbers[r.Generated]; ok {
				t.Errorf("I2: the tablet gave %s and %s the same patient number %s", o, r.First, r.Generated)
			}
			numbers[r.Generated] = r.First
		}
	}
	tb.online()
	tb.sendAll(tb.AndroidUser(tb.M.Users[tk.RoleNurse].Username), len(saved), 2*time.Minute)
	tb.refusals("sync-offline")
	tb.pushAll()
	got := tb.waitPeople(nil, len(saved), time.Minute)
	tb.once(saved, got)
	// Each number at most once on all of central, not only among this run's people.
	for v := range numbers {
		q := url.Values{"program": {p.ID}, "ouMode": {"ACCESSIBLE"}, "filter": {p.GeneratedAttr + ":eq:" + v}, "fields": {"trackedEntity"}}
		var page struct{ TrackedEntities []any }
		if err := tb.Central.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.TrackedEntities) > 1 {
			t.Errorf("I2: patient number %s on %d people on central", v, len(page.TrackedEntities))
		}
	}
}

// captureAPK returns the path of a Capture release's APK, or skips.
func captureAPK(t *testing.T, version string) string {
	dir := os.Getenv("HUB_CAPTURE_APKS")
	if dir == "" {
		t.Skip("set HUB_CAPTURE_APKS to a directory of Capture release APKs (dhis2-v<version>.apk)")
	}
	p := filepath.Join(dir, "dhis2-v"+version+".apk")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("no %s", p)
	}
	return p
}

// install puts a Capture version on the tablet; keep updates in place.
func (tb *tablet) install(version string, keep bool) {
	tb.t.Helper()
	if err := tb.a.Install(captureAPK(tb.t, version), keep); err != nil {
		tb.t.Fatal(err)
	}
	if v := tb.a.Version(); v != version {
		tb.t.Fatalf("Capture %s on the tablet after installing %s", v, version)
	}
}

// restoreNewest leaves the newest Capture installed, its data erased, when a test that
// installs others ends.
func (tb *tablet) restoreNewest() {
	newest := captureAPK(tb.t, captureNewest)
	tb.t.Cleanup(func() {
		if tb.a.Version() != captureNewest {
			if err := tb.a.Install(newest, false); err != nil {
				tb.t.Errorf("reinstalling Capture %s: %v", captureNewest, err)
			}
		}
		tb.a.Reset()
	})
}

// N21: the tablet runs the previous Capture release, logged in to the hub, and holds
// registrations it has not synced when Capture is updated to the newest release in
// place. After the update the registrations sync through the hub (online) and reach
// central once; what the hub refused the new release is recorded.
func TestN21AndroidCaptureUpdated(t *testing.T) {
	tb := startTablet(t, "N21")
	tb.restoreNewest()
	const n = 3
	tb.install(capturePrevious, false)
	if err := tb.a.Launch(); err != nil {
		t.Fatal(err)
	}
	tb.loginOnly(tb.hub)
	tb.refusals("login-" + capturePrevious)
	tb.offline()
	saved := tb.register(1, n)

	tb.install(captureNewest, true)
	tb.online()
	tb.a.Launch()
	seen, nodes, err := tb.a.Wait(2*time.Minute, "No filters applied", "Password", "Server URL")
	t.Logf("after the update the app opens on %q: %s", seen, tk.Texts(nodes))
	switch {
	case err != nil:
		t.Fatal(err)
	case seen == "Server URL":
		t.Fatalf("I1: after the update the app asks for a server: its account and %d unsynced registrations are gone", n)
	case seen == "Password":
		if _, err := tb.a.SwitchAccount(tb.hub, tb.M.Password, tb.M.Primary().Name, 5*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	got := tb.sendAll(nil, n, 2*time.Minute)
	tb.refusals("sync-" + captureNewest)
	tb.once(saved, got)
	tb.cycle()
	if p, i := tb.queue(); p > 0 || i > 0 {
		t.Errorf("I5: hub outbox %d, refused changes %d", p, i)
	}
	tb.once(saved, tb.waitPeople(nil, n, time.Second))
}

// N22: an older Capture release than the hub was tested with: its first login and
// download through the hub match a login straight to central, and registrations made
// offline sync through the hub and reach central once.
func TestN22AndroidCaptureOlder(t *testing.T) {
	tb := startTablet(t, "N22")
	tb.restoreNewest()
	const n = 3
	tb.install(captureOld, false)
	tb.a.Launch()
	direct := tb.loginOnly(tb.central)
	tb.Requests()
	viaHub := tb.login(tb.hub)
	t.Logf("Capture %s: %d records straight from central, %d through the hub", captureOld, direct, viaHub)
	if viaHub != direct {
		t.Errorf("Capture %s shows %d records through the hub, %d straight from central", captureOld, viaHub, direct)
	}
	tb.refusals("login-" + captureOld)

	tb.offline()
	saved := tb.register(1, n)
	tb.online()
	got := tb.sendAll(nil, n, 2*time.Minute)
	tb.refusals("sync-" + captureOld)
	tb.once(saved, got)
	tb.cycle()
	if p, i := tb.queue(); p > 0 || i > 0 {
		t.Errorf("I5: hub outbox %d, refused changes %d", p, i)
	}
}
