package adversarial

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The hub's own lifecycle (group G) and its releases (group N), against the real
// DHIS2 in HUB_MANIFEST: the country's choice is on central, the releases on a
// release source served by the test (updates_test.go).

// cycles waits for n sync cycles of the hub process to end after now.
func (r *netRun) cycles(n int) {
	r.t.Helper()
	// Counted in the log: with work waiting the hub starts the next cycle at once, so
	// the status rarely shows it idle (Laos 2.42: back-to-back cycles of 20 s).
	done := func() int {
		b, _ := os.ReadFile(r.p.Log)
		return strings.Count(string(b), `msg="sync cycle done"`)
	}
	want := done() + n
	if s := r.p.Status(); s["syncing"] == true {
		want++ // the cycle under way started before the call
	}
	r.p.WaitFor(fmt.Sprintf("%d sync cycles", n), time.Duration(n)*3*time.Minute, func(map[string]any) bool {
		return done() >= want
	})
}

// resultsSince is the hub's upgrade results logged since it had n of them.
func (r *netRun) upgradeResults(from int) []string {
	b, _ := os.ReadFile(r.p.Log)
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		if _, after, ok := strings.Cut(ln, "msg=upgrade result="); ok {
			out = append(out, "result="+after)
		}
	}
	if from > len(out) {
		return nil
	}
	return out[from:]
}

// G3 and G4: releases the hub must refuse, offered on central while clients work: a
// manifest with a bad signature, one signed by another key, a binary that does not
// match its signed checksum, a manifest that names another version, and the country
// choosing an older release or the one the hub runs. Must hold: none of them runs;
// each is recorded once (the heartbeat's lastUpgrade, the log) and not fetched again
// every cycle (I7); the hub keeps serving, nothing is lost; a good release offered
// after them is taken.
func TestG3G4ReleasesRefused(t *testing.T) {
	ch := newChannel(t)
	cur := ch.build("", "0.2.0")
	next := ch.build("", "0.4.0")
	r := releaseStart(t, cur)
	e, p := r.e, r.p
	hubID := p.Status()["hubId"].(string)
	stop := r.write(500 * time.Millisecond)
	defer stop()

	type offer struct {
		name, version, want string
		prepare             func(dir string)
	}
	ch.run(ch.dir, ch.tool, "keygen", "-out", filepath.Join(ch.dir, "other"))
	plat := runtime.GOOS + "/" + runtime.GOARCH
	asset := "hub-" + strings.ReplaceAll(plat, "/", "-")
	offers := []offer{
		{"G3 a bad signature", "0.3.0", "no valid signature", func(dir string) {
			os.WriteFile(filepath.Join(dir, "release.json.sig"), []byte(strings.Repeat("A", 86)+"==\n"), 0o644)
		}},
		{"G3 signed by another key", "0.3.1", "no valid signature", func(dir string) {
			ch.run(ch.dir, ch.tool, "release", "-key", filepath.Join(ch.dir, "other.key"), "-dir", dir, "-version", "0.3.1", plat+"="+next)
		}},
		{"G3 a binary that does not match its checksum", "0.3.2", "checksum", func(dir string) {
			b, _ := os.ReadFile(filepath.Join(dir, asset))
			b[len(b)/2] ^= 0xff
			os.WriteFile(filepath.Join(dir, asset), b, 0o755)
		}},
		{"G3 a manifest of another version", "0.3.3", "says it is", func(dir string) {
			os.RemoveAll(dir)
			ch.run(ch.dir, ch.tool, "release", "-key", filepath.Join(ch.dir, "release.key"), "-dir", dir, "-version", "0.3.4", plat+"="+next)
		}},
		{"G4 an older release", "0.1.0", "", nil},
		{"G4 the release it runs", "0.2.0", "", nil},
	}
	var repeated []string
	for _, o := range offers {
		dir := filepath.Join(ch.dir, "channel", "v"+o.version)
		if o.prepare != nil {
			ch.publish(o.version, next)
			o.prepare(dir)
		} else {
			ch.publish(o.version, cur)
		}
		results := len(r.upgradeResults(0))
		at := time.Now()
		choose(e, o.version, hubID)
		r.cycles(4)
		got := r.upgradeResults(results)
		bins := ch.fetched(at, "/hub-")
		manifests := 0 // release.json fetches only, not release.json.sig
		for _, f := range ch.fetched(at, "release.json") {
			if strings.HasSuffix(f.Path, "/release.json") {
				manifests++
			}
		}
		t.Logf("%s (%s): results %v; manifests fetched %d, binaries %d; version %v", o.name, o.version, got, manifests,
			len(bins), p.Status()["version"])
		if v := p.Status()["version"]; v != "0.2.0" {
			t.Fatalf("%s: the hub runs %v", o.name, v)
		}
		switch {
		case o.want == "":
			if len(got) > 0 || manifests > 0 {
				t.Errorf("%s: fetched %d manifests, recorded %v", o.name, manifests, got)
			}
		case len(got) == 0 || !strings.Contains(got[0], "result=ignored") || !strings.Contains(got[0], o.want):
			t.Errorf("%s: want one result ignored (%s), got %v", o.name, o.want, got)
		case len(got) > 1:
			repeated = append(repeated, fmt.Sprintf("%s: %d results in 4 cycles", o.name, len(got)))
		}
		if o.want != "" && manifests > 1 {
			t.Errorf("G3: %s: the refused manifest was fetched %d times in 4 cycles", o.name, manifests)
		}
		if len(bins) > 1 {
			t.Errorf("I7: %s: the binary fetched %d times", o.name, len(bins))
		}
	}
	if b, err := os.ReadDir(filepath.Join(e.Dir, "bin")); err == nil {
		var names []string
		for _, f := range b {
			names = append(names, f.Name())
		}
		t.Logf("the hub's bin folder: %v", names)
		for _, n := range names {
			if strings.Contains(n, "0.3.") {
				t.Errorf("a refused release's binary is kept in the hub's bin folder: %s", n)
			}
		}
	}

	// A good release afterwards is taken.
	ch.publish("0.4.0", next)
	choose(e, "0.4.0", hubID)
	r.upgraded("0.4.0", 5*time.Minute)
	time.Sleep(10 * time.Second)
	stop()
	r.finish(5 * time.Minute)
	beat := map[string]any{}
	e.Central.JSON("GET", "/api/dataStore/facility-hub-status/"+hubID, nil, &beat)
	t.Logf("heartbeat on central: version %v, lastUpgrade %v", beat["version"], beat["lastUpgrade"])
	if len(repeated) > 0 {
		t.Errorf("G3: refused releases recorded again at every cycle: %v", repeated)
	}
}

// G2: the new release fails. First one that starts but fails its health check, taken
// while clients save online with saves queued: it must roll back on its own with
// nothing lost, report the failure, and not be tried again. Then one that cannot
// start at all: it exits before its health check, and the service manager starts the
// hub again and again (systemd: Restart=always, every 5 s). Must hold: back on the old
// release by itself, data intact, the version not tried again.
func TestG2NewReleaseFails(t *testing.T) {
	ch := newChannel(t)
	cur := ch.build("", "0.2.0")
	bad := ch.build("", "0.3.0", "-X main.healthFail=true")
	good := ch.build("", "0.4.0")
	r := releaseStart(t, cur)
	e, p := r.e, r.p
	hubID := p.Status()["hubId"].(string)
	// Writers at drainPause throughout: each release is taken with the outbox empty, and
	// at the usual pace it never emptied on Laos 2.42 (155 to 264 waiting for 4 minutes).
	stop := r.write(drainPause)
	defer func() { stop() }()

	// 1. A release that fails its health check.
	r.outage(15 * time.Second)
	e.Net.SetDown(false)
	ch.publish("0.3.0", bad)
	at := time.Now()
	choose(e, "0.3.0", hubID)
	end := time.Now().Add(5 * time.Minute)
	for p.Grep("upgrade result=failed from=0.2.0 to=0.3.0") == 0 && time.Now().Before(end) {
		time.Sleep(500 * time.Millisecond)
	}
	if p.Grep("upgrade result=failed from=0.2.0 to=0.3.0") == 0 {
		t.Fatalf("no rollback from 0.3.0:\n%s", p.Tail(40))
	}
	s := p.WaitFor("back on 0.2.0", time.Minute, func(s map[string]any) bool { return s["version"] == "0.2.0" })
	if s["locked"] == true {
		c := e.Web(tk.RoleNurse)
		c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": e.M.Password})
	}
	rolled := time.Now()
	t.Logf("rolled back %s after the choice: %v", rolled.Sub(at).Round(time.Second), r.upgradeResults(0))
	r.cycles(4)
	if n := len(ch.fetched(rolled, "v0.3.0/")); n > 0 {
		t.Errorf("I7: the failed release is fetched again (%d requests in the last cycles)", n)
	}
	stop()
	r.finish(5 * time.Minute)
	stop = r.write(drainPause)

	// 2. A release that cannot start: it exits at every start, before its health check.
	bad = ch.build("", "0.3.5", "-X main.startFail=true")
	ch.publish("0.3.5", bad)
	choose(e, "0.3.5", hubID)
	end = time.Now().Add(5 * time.Minute)
	for p.Running() && time.Now().Before(end) {
		time.Sleep(200 * time.Millisecond)
	}
	if p.Running() {
		t.Fatalf("the hub did not switch to 0.3.5:\n%s", p.Tail(30))
	}
	died := time.Now()
	t.Logf("the new release exited at its start: %s", lastLine(p.Tail(5), "cannot start"))
	// The service manager starts the hub again, 5 s after each exit, for 2 minutes.
	starts, answered := 0, false
	for time.Since(died) < 2*time.Minute {
		time.Sleep(5 * time.Second)
		if p.Running() {
			if p.Status() != nil {
				answered = true
				break
			}
			continue
		}
		p.Launch()
		starts++
		time.Sleep(3 * time.Second)
		if p.Status() != nil {
			answered = true
			break
		}
	}
	stop()
	marker, _ := os.ReadFile(filepath.Join(e.Dir, "upgrade.json"))
	files, _ := filepath.Glob(filepath.Join(e.Dir, "hub.db*"))
	t.Logf("after %d starts by the service manager: the hub answers %v; marker %s; store files %v",
		starts, answered, strings.Join(strings.Fields(string(marker)), " "), names(files))
	// Found 28 October: upgrade.Recover, which puts the old store back, runs in the new
	// binary and left a swapped-in To version alone; AfterBoot's health check never ran.
	// Fixed 29 October: the new binary counts its starts (upgrade.CountStart) and rolls
	// back after three.
	if !answered {
		t.Fatalf("I6: a release that cannot start leaves the hub down: %d starts in 2 minutes, none answered", starts)
	}
	if v := p.Status()["version"]; v != "0.2.0" {
		t.Fatalf("the hub answers on %v, want 0.2.0", v)
	}
	if p.Grep("upgrade result=failed from=0.2.0 to=0.3.5") == 0 {
		t.Errorf("the rollback from 0.3.5 is not reported:\n%s", p.Tail(20))
	}
	p.Stop() // the service manager's last start runs the rolled-back hub
	r.restart()
	rolled = time.Now()
	r.cycles(4)
	if n := len(ch.fetched(rolled, "v0.3.5/")); n > 0 {
		t.Errorf("I7: the release that cannot start is fetched again (%d requests)", n)
	}
	r.finish(5 * time.Minute)

	// A good release afterwards is taken.
	ch.publish("0.4.0", good)
	choose(e, "0.4.0", hubID)
	r.upgraded("0.4.0", 5*time.Minute)
}

// G5 and N10: the country chooses a new release while the hub has been offline a long
// time (here: 3 minutes of saves at 3 a second) and holds a large outbox. Must hold:
// the release is applied only after the outbox is pushed; nothing lost or twice. The
// download is timed against the push.
func TestG5UpgradeAfterLongOutage(t *testing.T) {
	ch := newChannel(t)
	cur := ch.build("", "0.2.0")
	next := ch.build("", "0.3.0")
	r := releaseStart(t, cur)
	e, p := r.e, r.p
	hubID := p.Status()["hubId"].(string)
	stop := r.write(100 * time.Millisecond)
	defer stop()
	r.outage(10 * time.Second)
	ch.publish("0.3.0", next)
	choose(e, "0.3.0", hubID) // made on central while the hub cannot see it
	time.Sleep(3 * time.Minute)
	stop()
	queued := p.Status()["pending"]
	e.Net.SetRate(200_000) // a modest uplink, so the push takes a while
	back := time.Now()
	e.Net.SetDown(false)
	var drained, swapped time.Time
	var lastPending any
	for end := time.Now().Add(15 * time.Minute); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		s := p.Status()
		if s == nil {
			if swapped.IsZero() {
				swapped = time.Now()
			}
			continue
		}
		if s["version"] == "0.3.0" {
			break
		}
		lastPending = s["pending"]
		if drained.IsZero() && s["pending"] == float64(0) {
			drained = time.Now()
		}
	}
	e.Net.SetRate(0)
	bins := ch.fetched(back, "/hub-")
	if len(bins) == 0 {
		t.Fatal("the release was never downloaded")
	}
	t.Logf("%v objects queued; uplink back at %s; binary fetched %s after; outbox drained %s after; the old release went at %s, "+
		"last seen pending %v", queued, back.Format("15:04:05"), bins[0].At.Sub(back).Round(time.Second),
		drained.Sub(back).Round(time.Second), swapped.Sub(back).Round(time.Second), lastPending)
	if drained.IsZero() || (!swapped.IsZero() && swapped.Before(drained)) {
		t.Errorf("the upgrade was applied before the outbox was pushed")
	}
	if bins[0].At.Before(drained) {
		t.Logf("note: the binary was downloaded while %v objects were still to push (beside the push, not before it)", queued)
	}
	r.upgraded("0.3.0", 5*time.Minute)
	r.finish(5 * time.Minute)
}

// keySlots opens the hub's key file (its own SQLite, not encrypted) while the hub is
// stopped, for a test to backdate what only time would change.
func keySlots(t *testing.T, dir string, f func(s *store.Store)) {
	t.Helper()
	s, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f(s)
}

// oldestRelease is the oldest release the scenarios upgrade from straight to this tree
// (HUB_UPGRADE_OLDEST overrides it): 559fcdb folded every earlier migration into one
// per file, so no store from before it can be migrated (there are no release tags yet).
func oldestRelease() string {
	if r := os.Getenv("HUB_UPGRADE_OLDEST"); r != "" {
		return r
	}
	return "559fcdb"
}

// N2: an upgrade that skips releases: the oldest release straight to this tree, with
// an outbox queued, clients saving and every key and data migration since to run. Must
// hold: the migrations run in order; every record carried over; the outbox pushes
// after; nothing lost or twice.
func TestN2UpgradeSkippingReleases(t *testing.T) {
	ch := newChannel(t)
	old := ch.build(oldestRelease(), "0.1.0")
	next := ch.build("", "0.9.0")
	r := releaseStart(t, old)
	e, p := r.e, r.p
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	time.Sleep(10 * time.Second)
	r.outage(20 * time.Second)
	before := r.held(nurse)
	queued := p.Status()["pending"]
	e.Net.SetDown(false)
	ch.publish("0.9.0", next)
	at := time.Now()
	choose(e, "0.9.0", p.Status()["hubId"].(string))
	s := r.upgraded("0.9.0", 10*time.Minute)
	t.Logf("%v objects were queued; upgraded from %s in %s; %s", queued, oldestRelease(), time.Since(at).Round(time.Second),
		lastLine(p.Tail(400), "msg=upgrade result="))
	time.Sleep(10 * time.Second)
	stop()
	if p.Grep("upgrade result=ok from=0.1.0 to=0.9.0") != 1 {
		t.Errorf("the upgrade is not reported ok:\n%s", p.Tail(30))
	}
	if s["locked"] != false {
		t.Errorf("the hub is locked after the upgrade")
	}
	r.finish(5 * time.Minute)
	after := r.offlineRead()
	if m := missingFrom(before, after); len(m) > 0 {
		t.Errorf("I1: %d records held before the upgrade are gone offline after it: %v", len(m), first(m))
	}
	t.Logf("held offline: %d before, %d after", len(before), len(after))
}

// N7: a release that refuses what the old one accepted, with such saves queued. Since
// 813761b the hub refuses the saves of a user central stopped accepting after 7 days
// (NO_LOGIN); the release before retried them for ever. A user's saves queue on the
// old release, the user is disabled on central, the hub upgrades, and 7 days pass
// (backdated in the key file). Must hold: the saves are refused with the reason and
// kept, never dropped; other saves unaffected.
func TestN7StricterReleaseWithQueuedSaves(t *testing.T) {
	ch := newChannel(t)
	old := ch.build(fromRelease(), "0.1.0")
	next := ch.build("", "0.2.0")
	r := releaseStart(t, old)
	e, p := r.e, r.p
	u2 := e.NewUserLike(tk.RoleNurse)
	c2 := e.AndroidUser(u2.Username)
	if st, b := c2.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("%s online: %d %s", u2.Username, st, b)
	}
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(10 * time.Second)
	var theirs []string
	for i := range 3 {
		pr := e.M.NewPerson(r.prog, fmt.Sprintf("Zqstrict%d", i))
		if st, b := c2.Do("POST", tk.SyncImport, pr.Payload); st != 200 {
			t.Fatalf("%s offline: %d %.300s", u2.Username, st, b)
		}
		theirs = append(theirs, pr.TE)
	}
	e.SetUserDisabled(u2, true)
	e.ExpireTokenCache()
	e.Net.SetDown(false)
	s := p.WaitFor("their saves stuck", 3*time.Minute, func(s map[string]any) bool {
		return s["stuckOutbox"] != nil && s["stuckOutbox"].(float64) >= 1 && s["syncing"] == false
	})
	t.Logf("on the old release: %v", s)
	ch.publish("0.2.0", next)
	choose(e, "0.2.0", s["hubId"].(string))
	s = r.upgraded("0.2.0", 5*time.Minute)
	stop()
	r.cycles(1) // not "syncing" false: on Laos 2.42 the cycles ran back to back
	p.WaitFor("drained but theirs", 3*time.Minute, func(s map[string]any) bool {
		return s["stuckOutbox"] != nil && s["mode"] == "online"
	})
	t.Logf("after the upgrade: %v", p.Status())

	// A week later.
	p.Stop()
	keySlots(t, e.Dir, func(st *store.Store) {
		at := time.Now().Add(-(7*24*time.Hour + time.Minute)).UTC().Format(time.RFC3339Nano)
		res, err := st.Keys.Exec(`UPDATE key_slots SET needs_login_at=? WHERE username=?`, at, u2.Username)
		if n, _ := res.RowsAffected(); err != nil || n != 1 {
			t.Fatalf("backdating %s: %d rows, %v", u2.Username, n, err)
		}
	})
	r.restart()
	r.cycles(3)
	s = p.Status()
	t.Logf("a week later: rejected %v, stuckOutbox %v, pending %v; NO_LOGIN in the log: %d", s["rejected"], s["stuckOutbox"], s["pending"],
		p.Grep("NO_LOGIN"))
	for _, te := range theirs {
		if r.central("trackedEntities", te, "trackedEntity") != nil {
			t.Errorf("%s's save reached central as someone", u2.Username)
		}
	}
	if s["rejected"] == float64(0) {
		t.Errorf("I1/I5: the disabled user's saves are neither pushed nor refused a week after the upgrade: %v", s)
	}
	r.finish(5 * time.Minute)
}

// N8: the release key changes. A hub trusts one key, built in. Must hold: a release
// signed only with a new key is refused; a release signed with the old key that
// carries the new key is taken, and from then the hub takes releases of the new key
// and refuses those of the old one. (No such path is documented: DEPLOYMENT.md says
// only that releases are signed with the supplier's key.)
func TestN8ReleaseKeyChanges(t *testing.T) {
	ch := newChannel(t)
	ch.run(ch.dir, ch.tool, "keygen", "-out", filepath.Join(ch.dir, "new"))
	b, _ := os.ReadFile(filepath.Join(ch.dir, "new.pub"))
	newKey := "-X github.com/dhis2/facility-hub/internal/upgrade.ReleaseKey=" + strings.TrimSpace(string(b))
	cur := ch.build("", "0.2.0")
	r := releaseStart(t, cur)
	e, p := r.e, r.p
	hubID := p.Status()["hubId"].(string)
	stop := r.write(500 * time.Millisecond)
	defer stop()
	plat := runtime.GOOS + "/" + runtime.GOARCH
	sign := func(version, key, bin string) {
		ch.run(ch.dir, ch.tool, "release", "-key", filepath.Join(ch.dir, key+".key"), "-dir",
			filepath.Join(ch.dir, "channel", "v"+version), "-version", version, plat+"="+bin)
	}
	offer := func(what, version string, taken bool) {
		t.Helper()
		results := len(r.upgradeResults(0))
		choose(e, version, hubID)
		if taken {
			r.upgraded(version, 5*time.Minute)
			t.Logf("%s (%s): taken", what, version)
			return
		}
		r.cycles(3)
		t.Logf("%s (%s): %v; the hub runs %v", what, version, r.upgradeResults(results), p.Status()["version"])
		if v := p.Status()["version"]; v == version {
			t.Errorf("%s: taken", what)
		}
	}
	sign("0.3.0", "new", ch.build("", "0.3.0", newKey))
	offer("signed with the new key only", "0.3.0", false)
	sign("0.3.1", "release", ch.build("", "0.3.1", newKey))
	offer("signed with the old key, carrying the new one", "0.3.1", true)
	sign("0.4.0", "release", ch.build("", "0.4.0", newKey))
	offer("signed with the old key after the change", "0.4.0", false)
	sign("0.4.1", "new", ch.build("", "0.4.1", newKey))
	offer("signed with the new key after the change", "0.4.1", true)
	time.Sleep(10 * time.Second)
	stop()
	r.finish(5 * time.Minute)
}
