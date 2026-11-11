package adversarial

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The hub's own updates (group N): a hub of an older release, set up and filled
// against the real DHIS2, upgrades itself to this tree's release through the real
// mechanism: the country's choice on central (facility-hub-admin/release) and a
// signed release on a release source, here served locally.

// fromRelease is the release the scenarios upgrade from (HUB_UPGRADE_FROM overrides
// it). There are no release tags yet: 2ab5f95 is the last commit before the latest
// store migrations (key file 2, data file 3), so an upgrade from it runs both.
func fromRelease() string {
	if r := os.Getenv("HUB_UPGRADE_FROM"); r != "" {
		return r
	}
	return "2ab5f95"
}

// channel is a release source laid out like GitHub's (v<version>/release.json, its
// signature and the binaries), with a signing key the hubs built here trust.
type channel struct {
	t        *testing.T
	root     string // the repository
	dir      string // work folder: the tool, the key, sources and binaries
	tool     string // this tree's hub binary, for keygen and release
	pub      string
	srv      *httptest.Server
	rate     atomic.Int64  // bytes per second for binaries; 0: unlimited
	fetching chan struct{} // receives when a hub starts downloading a binary

	mu     sync.Mutex
	served []served // what hubs fetched
}

// served is one file a hub fetched from the release source.
type served struct {
	At   time.Time
	Path string
}

// fetched returns the paths fetched since t that contain s.
func (c *channel) fetched(since time.Time, s string) []served {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []served
	for _, f := range c.served {
		if !f.At.Before(since) && strings.Contains(f.Path, s) {
			out = append(out, f)
		}
	}
	return out
}

func newChannel(t *testing.T) *channel {
	t.Helper()
	if os.Getenv("HUB_MANIFEST") == "" {
		t.Skip("adversarial scenarios run against a real DHIS2 (HUB_MANIFEST)")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	c := &channel{t: t, root: root, dir: t.TempDir(), fetching: make(chan struct{}, 16)}
	c.tool = filepath.Join(c.dir, "hub-tool")
	c.run(root, "go", "build", "-o", c.tool, "./cmd/hub")
	c.run(c.dir, c.tool, "keygen", "-out", filepath.Join(c.dir, "release"))
	b, _ := os.ReadFile(filepath.Join(c.dir, "release.pub"))
	c.pub = strings.TrimSpace(string(b))
	files := http.FileServer(http.Dir(filepath.Join(c.dir, "channel")))
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.served = append(c.served, served{time.Now(), r.URL.Path})
		c.mu.Unlock()
		if strings.Contains(r.URL.Path, "/hub-") {
			select {
			case c.fetching <- struct{}{}:
			default:
			}
			if bps := c.rate.Load(); bps > 0 {
				f, err := os.Open(filepath.Join(c.dir, "channel", filepath.FromSlash(r.URL.Path)))
				if err != nil {
					http.NotFound(w, r)
					return
				}
				defer f.Close()
				for {
					if _, err := io.CopyN(w, f, bps/10); err != nil {
						return
					}
					w.(http.Flusher).Flush()
					time.Sleep(100 * time.Millisecond)
				}
			}
		}
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *channel) run(dir, name string, args ...string) {
	c.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		c.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// build builds the hub at a commit ("" for this tree) as a release of version that
// trusts this channel; extra are more -X flags.
func (c *channel) build(ref, version string, extra ...string) string {
	c.t.Helper()
	src := c.root
	if ref != "" {
		src = filepath.Join(c.dir, "src-"+ref)
		os.MkdirAll(src, 0o755)
		c.run(c.root, "sh", "-c", fmt.Sprintf("git archive %s | tar -x -C %s", ref, src))
		if b, err := os.ReadFile(filepath.Join(c.root, "go.work")); err == nil { // a local workspace, as this tree builds
			os.WriteFile(filepath.Join(src, "go.work"), b, 0o644)
		}
	}
	up := "github.com/dhis2/facility-hub/internal/upgrade"
	flags := append([]string{"-X main.version=" + version, "-X " + up + ".ReleaseKey=" + c.pub,
		"-X " + up + ".ReleaseBase=" + c.srv.URL}, extra...)
	out := filepath.Join(c.dir, fmt.Sprintf("hub-%s-%s", version, strings.NewReplacer("/", "", " ", "").Replace(ref)))
	start := time.Now()
	c.run(src, "go", "build", "-ldflags", strings.Join(flags, " "), "-o", out, "./cmd/hub")
	c.t.Logf("built %s from %s in %s", version, map[bool]string{true: "this tree", false: ref}[ref == ""], time.Since(start).Round(time.Second))
	return out
}

// publish signs a release of version with this binary.
func (c *channel) publish(version, bin string) {
	c.t.Helper()
	c.run(c.dir, c.tool, "release", "-key", filepath.Join(c.dir, "release.key"), "-dir",
		filepath.Join(c.dir, "channel", "v"+version), "-version", version, runtime.GOOS+"/"+runtime.GOARCH+"="+bin)
}

// choose sets the country's choice on central to version, for this hub alone; the
// choice there before is put back when the test ends.
func choose(e *tk.Env, version, hubID string) { chooseSigned(e, version, hubID, nil) }

// chooseSigned is choose for an approved hub: the choice signed with its move key, as
// the Facility Hub app writes it (an approved hub takes no other); nil writes it plain.
func chooseSigned(e *tk.Env, version, hubID string, moveKey []byte) {
	e.T.Helper()
	const path = "/api/dataStore/facility-hub-admin/release"
	st, was, err := e.Central.Do("GET", path, nil)
	if err != nil {
		e.T.Fatal(err)
	}
	method := "PUT"
	if st == 404 {
		method = "POST"
	}
	v, _ := json.Marshal(map[string]any{"version": version, "pilotHubs": []string{hubID}, "percent": 0})
	if moveKey != nil {
		v = signedFor(e, hubID, moveKey, "facility-hub-admin/release", v)
	}
	if st, b, err := e.Central.Do(method, path, v); err != nil || st/100 != 2 {
		e.T.Fatalf("choosing release %s: %d %v %s", version, st, err, b)
	}
	e.T.Cleanup(func() {
		if st == 404 {
			e.Central.Do("DELETE", path, nil)
		} else {
			e.Central.Do("PUT", path, json.RawMessage(was))
		}
	})
}

// releaseStart sets a hub of the old release up for the facility, through its own
// pages as an installer would, logs the writers in and waits for its first pull.
func releaseStart(t *testing.T, old string) *netRun {
	t.Helper()
	e := tk.Start(t)
	r := newRun(t, e)
	r.p = e.DetachRelease(old)
	t.Cleanup(r.keepLog)
	e.SetupOnly(tk.RoleAdmin)
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if _, ok := e.M.User(role); ok {
			e.Web(role).Login()
		}
	}
	start := time.Now()
	r.p.WaitFor("the first pull", 10*time.Minute, func(s map[string]any) bool { return s["lastPull"] != nil && s["syncing"] == false })
	t.Logf("set up on %v; first pull %s", r.p.Status()["version"], time.Since(start).Round(time.Second))
	return r
}

// upgraded waits for the hub to run version, logging what it did meanwhile.
func (r *netRun) upgraded(version string, timeout time.Duration) map[string]any {
	r.t.Helper()
	start := time.Now()
	s := r.p.WaitFor("version "+version, timeout, func(s map[string]any) bool { return s["version"] == version })
	r.t.Logf("on %s after %s: %v", version, time.Since(start).Round(time.Second), s)
	return s
}

// held is what a member of staff reads through the hub offline: the people of the
// primary program and the events of the first event program at the facility.
func (r *netRun) held(c *tk.Client) map[string]bool {
	r.t.Helper()
	out := map[string]bool{}
	q := url.Values{"program": {r.prog.ID}, "orgUnits": {r.e.M.Facility.ID}, "fields": {"trackedEntity"}, "paging": {"false"}}
	st, _, b, err := c.Try("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil)
	if err != nil || st != 200 {
		r.t.Fatalf("people offline: %d %v %.300s", st, err, b)
	}
	var tes struct {
		TEs []struct {
			TE string `json:"trackedEntity"`
		} `json:"trackedEntities"`
	}
	json.Unmarshal(b, &tes)
	for _, x := range tes.TEs {
		out["person "+x.TE] = true
	}
	if len(r.e.M.EventPrograms) > 0 {
		q := url.Values{"program": {r.e.M.EventPrograms[0].ID}, "orgUnit": {r.e.M.Facility.ID}, "fields": {"event"}, "paging": {"false"}}
		st, _, b, err := c.Try("GET", "/api/tracker/events?"+q.Encode(), nil)
		if err != nil || st != 200 {
			r.t.Fatalf("events offline: %d %v %.300s", st, err, b)
		}
		var res map[string]any
		json.Unmarshal(b, &res)
		for ev := range eventsIn(res) {
			out["event "+ev] = true
		}
	}
	return out
}

// offlineRead reads what the nurse holds, with the uplink cut for the read.
func (r *netRun) offlineRead() map[string]bool {
	r.t.Helper()
	r.e.Net.SetDown(true)
	defer r.e.Net.SetDown(false)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	c := r.e.Web(tk.RoleNurse)
	if st, _, b, err := c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": r.e.M.Password}); err != nil || st != 200 {
		r.t.Fatalf("offline login: %d %v %.300s", st, err, b)
	}
	return r.held(c)
}

func missingFrom(before, after map[string]bool) []string {
	var out []string
	for k := range before {
		if !after[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// N1: upgrade from the last release with every kind of record present and clients
// working: registrations and updates saved throughout, an event program's event, an
// aggregate value, a refused change, and the saves of a user who must log in online
// again (they stay in the outbox across the upgrade). Must hold: every record and the
// hub's identity carried over, the outbox pushes after, nothing lost or twice.
func TestN1UpgradeWithEveryKindOfRecord(t *testing.T) {
	ch := newChannel(t)
	old := ch.build(fromRelease(), "0.1.0")
	next := ch.build("", "0.2.0")
	r := releaseStart(t, old)
	e, p := r.e, r.p
	if len(e.M.EventPrograms) == 0 || e.M.Aggregate == nil {
		t.Skip("this setup has no event program or no aggregate data set")
	}
	hubID := p.Status()["hubId"].(string)
	ep := e.M.EventPrograms[0]
	sc := &scene{Env: e, t: t}
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	webNurse := e.Web(tk.RoleNurse)
	webNurse.Login()

	// A user whose saves will wait for an online login (their token revoked).
	u2 := e.NewUserLike(tk.RoleNurse)
	c2 := e.AndroidUser(u2.Username)
	if st, b := c2.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("%s online: %d %s", u2.Username, st, b)
	}
	// An event saved online, deleted on central while edited offline: a refusal.
	deleted := tk.UID()
	if st, b := nurse.Do("POST", tk.SyncImport, sc.programEvent(ep, deleted, nil)); st != 200 {
		t.Fatalf("event online: %d %.300s", st, b)
	}
	pulled := p.Status()["lastPull"]
	p.WaitFor("a pull after the online save", 2*time.Minute, func(s map[string]any) bool { return s["lastPull"] != pulled && s["syncing"] == false })

	e.Net.SetDown(true)
	p.WaitFor("offline", time.Minute, tk.Offline)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	evNew := tk.UID()
	if st, b := nurse.Do("POST", tk.SyncImport, sc.programEvent(ep, evNew, nil)); st != 200 {
		t.Fatalf("new event offline: %d %.300s", st, b)
	}
	if st, b := nurse.Do("POST", tk.SyncImport, sc.programEvent(ep, deleted, nil)); st != 200 {
		t.Fatalf("edit offline: %d %.300s", st, b)
	}
	pe := time.Now().Format("200601") // the data set is monthly in every profile
	aggValue := fmt.Sprint(500 + time.Now().Second())
	values := "/api/" + strings.Join(strings.Split(e.M.DHIS2Version, ".")[1:2], "") + "/dataValues"
	if st, b := webNurse.PostDataValue(values, map[string]string{"ds": e.M.Aggregate.DataSet, "pe": pe, "ou": e.M.Facility.ID,
		"de": e.M.Aggregate.Number, "co": e.M.Aggregate.COC, "value": aggValue}); st/100 != 2 {
		t.Fatalf("aggregate value offline: %d %s", st, b)
	}
	stuck := e.M.NewPerson(r.prog, "Zqupgstuck") // not the run's tag: not in its ledger
	if st, b := c2.Do("POST", tk.SyncImport, stuck.Payload); st != 200 {
		t.Fatalf("%s offline: %d %.300s", u2.Username, st, b)
	}
	e.CentralDelete("events", deleted)
	e.RevokeTokens(u2)
	e.ExpireTokenCache()
	time.Sleep(5 * time.Second)
	before := r.held(nurse)
	e.Net.SetDown(false)
	s := p.WaitFor("everything pushed but the user's", 3*time.Minute, func(s map[string]any) bool {
		return s["mode"] == "online" && s["stuckOutbox"] == float64(1) && s["rejected"] == float64(1) && s["syncing"] == false
	})
	t.Logf("before the upgrade: %v; %d records held offline", s, len(before))

	ch.publish("0.2.0", next)
	choose(e, "0.2.0", hubID)
	upgradeAt := time.Now()
	s = r.upgraded("0.2.0", 5*time.Minute)
	time.Sleep(10 * time.Second) // clients go on working on the new release
	stop()
	t.Logf("clients during the upgrade: %s", r.waits(upgradeAt.Add(-5*time.Second), time.Now()))
	if n := p.Grep("upgrade result=ok"); n != 1 {
		t.Errorf("the log says upgrade result=ok %d times", n)
	}
	if s["hubId"] != hubID || s["locked"] != false || s["stuckOutbox"] != float64(1) || s["rejected"] != float64(1) {
		t.Errorf("not carried over: hub %v (was %s), locked %v, stuck %v, rejected %v", s["hubId"], hubID, s["locked"], s["stuckOutbox"], s["rejected"])
	}
	after := r.offlineRead()
	if m := missingFrom(before, after); len(m) > 0 {
		t.Errorf("I1: %d records held before the upgrade are gone offline after it: %v", len(m), first(m))
	}
	t.Logf("held offline: %d before, %d after", len(before), len(after))

	// The user logs in online again: their save goes, as them.
	p.WaitFor("online", time.Minute, tk.Online)
	w2 := e.Web(tk.RoleNurse)
	w2.User = u2.Username
	w2.Login()
	r.finish(5 * time.Minute)
	if o := e.CentralObject("trackedEntities", stuck.TE); o == nil || username(o, "createdBy") != u2.Username {
		t.Errorf("I1: %s's save from before the upgrade on central: %v", u2.Username, o != nil)
	}
	if e.CentralObject("events", evNew) == nil {
		t.Errorf("I1: the event program's event from before the upgrade is not on central")
	}
	if v, by, _ := e.CentralDataValue(e.M.Aggregate.DataSet, pe, e.M.Aggregate.Number, e.M.Aggregate.COC, e.M.Aggregate.DefaultAOC); v != aggValue || by != nurse.User {
		t.Errorf("I1: the aggregate value on central: %q by %q, want %s by %s", v, by, aggValue, nurse.User)
	}
}

// wrapper is a release binary that runs bin, and holds while a file exists: at
// "migrate" (the upgrade's migration of the store copy) while dir/hold-migrate exists,
// at "serve" (the new release's first start, after the swap) while dir/hold-start
// does. It notes each hold in dir/at-migrate or dir/at-start, with its process ID.
func wrapper(t *testing.T, dir, bin string) string {
	t.Helper()
	path := filepath.Join(dir, "hub-wrapped")
	script := fmt.Sprintf(`#!/bin/sh
D=%q
case "$1" in
migrate) if [ -e "$D/hold-migrate" ]; then echo $$ > "$D/at-migrate"; while [ -e "$D/hold-migrate" ]; do sleep 0.1; done; fi ;;
serve) if [ -e "$D/hold-start" ]; then echo $$ > "$D/at-start"; while [ -e "$D/hold-start" ]; do sleep 0.1; done; fi ;;
esac
exec %q "$@"
`, dir, bin)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// waitFile waits for a file to appear and returns its content.
func waitFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("waiting for %s: timed out after %s", path, timeout)
	return ""
}

func touch(t *testing.T, path string) {
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// N4: power cuts during an upgrade, with clients saving throughout: during the
// download of the new release, during the migration of the store copy (the hub and
// the migration both killed), and during the first start of the new release. Must
// hold: after each, the hub runs the old or the new release with its store, never in
// between; the upgrade completes; nothing confirmed is lost or pushed twice.
func TestN4KilledDuringUpgrade(t *testing.T) {
	ch := newChannel(t)
	old := ch.build(fromRelease(), "0.1.0")
	next := ch.build("", "0.2.0")
	hold := t.TempDir()
	ch.publish("0.2.0", wrapper(t, hold, next))
	r := releaseStart(t, old)
	e, p := r.e, r.p
	stop := r.write(300 * time.Millisecond)
	defer stop()
	state := func(when string) {
		t.Helper()
		s := p.Status()
		t.Logf("after the kill %s: version %v, locked %v, pending %v, rejected %v", when, s["version"], s["locked"], s["pending"], s["rejected"])
		if v := s["version"]; v != "0.1.0" && v != "0.2.0" {
			t.Errorf("after the kill %s the hub runs %v", when, v)
		}
		if s["locked"] != false {
			t.Errorf("after the kill %s the store stays locked after a login", when)
		}
	}

	// 1. During the download: 2 MB/s, killed 3 s in.
	ch.rate.Store(2 << 20)
	touch(t, filepath.Join(hold, "hold-migrate"))
	choose(e, "0.2.0", p.Status()["hubId"].(string))
	select {
	case <-ch.fetching:
	case <-time.After(3 * time.Minute):
		t.Fatal("the hub does not download the release")
	}
	time.Sleep(3 * time.Second)
	p.Kill()
	ch.rate.Store(0)
	time.Sleep(2 * time.Second)
	r.restart()
	state("during the download")

	// 2. During the migration of the store copy: the hub and its migration killed.
	pid := waitFile(t, filepath.Join(hold, "at-migrate"), 3*time.Minute)
	p.Kill()
	var n int
	fmt.Sscan(pid, &n)
	if mig, err := os.FindProcess(n); err == nil {
		mig.Kill()
	}
	touch(t, filepath.Join(hold, "hold-start"))
	os.Remove(filepath.Join(hold, "hold-migrate"))
	time.Sleep(2 * time.Second)
	r.restart()
	state("during the migration")

	// 3. During the first start of the new release, after the swap.
	waitFile(t, filepath.Join(hold, "at-start"), 3*time.Minute)
	p.Kill()
	os.Remove(filepath.Join(hold, "hold-start"))
	time.Sleep(2 * time.Second)
	r.restart()
	state("during the first start")

	r.upgraded("0.2.0", 3*time.Minute)
	time.Sleep(10 * time.Second)
	stop()
	if n := p.Grep("upgrade result=ok"); n != 1 {
		t.Errorf("the log says upgrade result=ok %d times; failed %d times", n, p.Grep("upgrade result=failed"))
	}
	r.finish(5 * time.Minute)
}

// N5: the new release's health check after the migration ran. First central stops
// answering (a half-open uplink) as the new release starts: the check tests the hub
// itself, so the release is kept. Then a release that fails its check (built with
// healthFail) starts with the uplink down, while clients go on saving. Must hold: back
// on the old binary and the old store, and nothing confirmed in between is lost: until
// its check has passed, a new release takes no save of its own (clients get "the hub is
// upgrading" and retry) and pushes nothing.
//
// Found 27 October: the check also required central, so a good release rolled back
// when central was slow, and saves the new release had confirmed were lost with its
// store (53 registrations). Fixed 28 October.
func TestN5HealthCheckFailsAfterMigration(t *testing.T) {
	ch := newChannel(t)
	old := ch.build(fromRelease(), "0.1.0")
	good := ch.build("", "0.2.0")
	bad := ch.build("", "0.3.0", "-X main.healthFail=true")
	holdGood, holdBad := t.TempDir(), t.TempDir()
	ch.publish("0.2.0", wrapper(t, holdGood, good))
	r := releaseStart(t, old)
	e, p := r.e, r.p
	stop := r.write(200 * time.Millisecond)
	defer func() { stop() }()
	login := func(s map[string]any) {
		if s["locked"] == true {
			t.Logf("the hub starts locked: staff log in")
			c := e.Web(tk.RoleNurse)
			c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": e.M.Password})
		}
	}

	// 1. Central stops answering as the new release starts: the release is kept.
	touch(t, filepath.Join(holdGood, "hold-start"))
	choose(e, "0.2.0", p.Status()["hubId"].(string))
	waitFile(t, filepath.Join(holdGood, "at-start"), 3*time.Minute)
	release := e.Net.HalfOpen()
	swapped := time.Now()
	os.Remove(filepath.Join(holdGood, "hold-start"))
	end := time.Now().Add(2 * time.Minute)
	for p.Grep("upgrade result=") == 0 && time.Now().Before(end) {
		time.Sleep(200 * time.Millisecond)
	}
	decided := time.Now()
	release()
	t.Logf("with central half-open, the upgrade decided %s after the swap: ok %d, failed %d; saves meanwhile: %s",
		decided.Sub(swapped).Round(time.Millisecond), p.Grep("upgrade result=ok"), p.Grep("upgrade result=failed"), r.waits(swapped, decided))
	if p.Grep("upgrade result=ok from=0.1.0 to=0.2.0") != 1 || p.Grep("upgrade result=failed") != 0 {
		t.Fatalf("with central half-open the new release was not kept:\n%s", p.Tail(40))
	}
	login(p.WaitFor("0.2.0", time.Minute, func(s map[string]any) bool { return s["version"] == "0.2.0" }))
	// What the half-open uplink held back is pushed first: with clients saving as fast
	// as the hub pushes, the outbox would not empty, and the next upgrade waits for it.
	stop()
	p.WaitFor("the outbox to drain", 5*time.Minute, tk.Drained)
	stop = r.write(200 * time.Millisecond)

	// 2. A release that fails its health check, started with the uplink down: staff
	// save on the hub itself.
	ch.publish("0.3.0", wrapper(t, holdBad, bad))
	touch(t, filepath.Join(holdBad, "hold-start"))
	choose(e, "0.3.0", p.Status()["hubId"].(string))
	waitFile(t, filepath.Join(holdBad, "at-start"), 3*time.Minute)
	e.Net.SetDown(true)
	swapped = time.Now()
	os.Remove(filepath.Join(holdBad, "hold-start"))
	end = time.Now().Add(2 * time.Minute)
	for p.Grep("upgrade result=failed") == 0 && time.Now().Before(end) {
		time.Sleep(200 * time.Millisecond)
	}
	rolled := time.Now()
	if p.Grep("upgrade result=failed from=0.2.0 to=0.3.0") == 0 {
		t.Fatalf("no rollback logged:\n%s", p.Tail(40))
	}
	login(p.WaitFor("the old release", time.Minute, func(s map[string]any) bool { return s["version"] == "0.2.0" }))
	time.Sleep(10 * time.Second) // saves on the old release, offline
	e.Net.SetDown(false)
	var upgrading, byHub int
	for _, s := range r.ledger.all() {
		if s.At.Before(swapped) || !s.At.Before(rolled) {
			continue
		}
		if strings.Contains(s.Err, "upgrading") {
			upgrading++
		}
		if s.Confirmed && !s.Online {
			byHub++
		}
	}
	t.Logf("rolled back %s after the swap; saves meanwhile: %s; refused as upgrading %d, confirmed by the hub %d",
		rolled.Sub(swapped).Round(time.Millisecond), r.waits(swapped, rolled), upgrading, byHub)
	time.Sleep(10 * time.Second)
	stop()
	if v := p.Status()["version"]; v != "0.2.0" {
		t.Errorf("after the rollback the hub runs %v", v)
	}
	r.finish(5 * time.Minute)
}

// N13: central announces a major this hub release does not support, while saves wait
// offline. DHIS2 is not upgraded here (a 2.42 to 2.43 upgrade needs a new instance):
// the uplink answers /api/system/info with 2.44.0, everything else is central's 2.43.
// Must hold: the hub says "needs upgrade", answers nothing offline and passes the rest
// through; no queued save lost; after the hub is upgraded to a release that supports
// central (here: the uplink's answer is central's again), they push.
//
// Found 27 October: online, the hub refused tracker reads (503 "needs connection: hub
// needs upgrade") instead of passing them through; and once the country chose a new
// release, the hub crashed as it drained its outbox for the upgrade (push.go read
// central's import report through h.API(), nil for an unsupported version), at every
// start. Fixed 28 October: online it passes through, and the upgrade leaves the queue
// to the new release.
func TestN13UnsupportedMajor(t *testing.T) {
	ch := newChannel(t)
	cur := ch.build("", "0.1.0")
	next := ch.build("", "0.2.0")
	r := releaseStart(t, cur)
	e, p := r.e, r.p
	e.Net.SetDown(true)
	p.WaitFor("offline", time.Minute, tk.Offline)
	stop := r.write(300 * time.Millisecond)
	time.Sleep(15 * time.Second)
	stop()
	queued := p.Status()["pending"]
	t.Logf("queued offline: %s; pending objects %v", r.ledger.summary(), queued)

	e.Net.SetVersion("2.44.0")
	e.Net.SetDown(false)
	s := p.WaitFor("needs upgrade", 2*time.Minute, func(s map[string]any) bool { return s["needsUpgrade"] == true })
	t.Logf("status: %v", s)
	if s["pending"] != queued {
		t.Errorf("pending objects %v, were %v", s["pending"], queued)
	}
	nurse := e.Android(tk.RoleNurse)
	q := url.Values{"program": {r.prog.ID}, "orgUnits": {e.M.Facility.ID}, "fields": {"trackedEntity"}, "pageSize": {"5"}}
	st, h, b, _ := nurse.Try("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil)
	t.Logf("a read online: %d (answered by the hub: %q) %.200s", st, h.Get("X-Hub-Mode"), b)
	if st != 200 || h.Get("X-Hub-Mode") != "" {
		t.Errorf("online the hub does not pass a read through to central: %d", st)
	}
	e.Net.SetDown(true)
	p.WaitFor("offline", time.Minute, tk.Offline)
	st, _, b, _ = nurse.Try("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil)
	t.Logf("a read offline: %d %.200s", st, b)
	if st == 200 {
		t.Errorf("offline the hub answers a read for an unsupported central")
	}
	pr := e.M.NewPerson(r.prog, "Zqupgrefused")
	st, _, b, _ = nurse.Try("POST", tk.SyncImport, pr.Payload)
	t.Logf("a save offline: %d %.200s", st, b)
	if st == 200 {
		t.Errorf("offline the hub takes a save for an unsupported central")
	}
	e.Net.SetDown(false)
	p.WaitFor("online", time.Minute, tk.Online)

	// The country chooses a new release: the hub drains its outbox first.
	ch.publish("0.2.0", next)
	choose(e, "0.2.0", s["hubId"].(string))
	end := time.Now().Add(3 * time.Minute)
	for time.Now().Before(end) && p.Running() && p.Status()["version"] != "0.2.0" {
		time.Sleep(time.Second)
	}
	if !p.Running() {
		t.Errorf("I6: the hub process died during the upgrade's drain:\n%s", p.Tail(12))
		p.Start() // as a service manager would, and staff log in
		c := e.Web(tk.RoleNurse)
		c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": e.M.Password})
		time.Sleep(30 * time.Second)
		t.Logf("started again: running after 30 s: %v; panics in the log: %d", p.Running(), p.Grep("panic: runtime error"))
		if !p.Running() {
			t.Errorf("I6: the hub crashes again at its next start")
		}
	}
	// Central is supported again (as by a release that supports it): the queued saves
	// push.
	e.Net.SetVersion("")
	if !p.Running() {
		r.restart()
	}
	r.upgraded("0.2.0", 3*time.Minute)
	r.finish(5 * time.Minute)
}
