//go:build !windows

package adversarial

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// upgradeFrom is the hub release a step behind this tree: the commit before key
// migration 2 (813761b, needs_login_at), so an upgrade from it runs a real migration.
const upgradeFrom = "813761b^"

// upgradeRun is a netRun whose hub runs that older release, and a release source the
// hub trusts, as GitHub's releases are (scripts/test-upgrade.sh, on a real central).
type upgradeRun struct {
	*netRun
	tool    string // a hub binary for keygen and release
	ldflags string // the release key and source, built into every release
	channel string // the release source's folder
	src     *releaseSource
	from    string // the version the hub runs
}

// releaseSource serves the channel. While slow is set, a binary is sent at 1 MB/s, and
// sending receives once it is under way.
type releaseSource struct {
	dir     string
	slow    atomic.Bool
	sending chan struct{}
}

func (s *releaseSource) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.URL.Path)
	if !s.slow.Load() || !strings.HasPrefix(name, "hub-") {
		http.FileServer(http.Dir(s.dir)).ServeHTTP(w, r)
		return
	}
	b, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(r.URL.Path)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	for i := 0; len(b) > 0; i++ {
		n := min(len(b), 100<<10)
		if _, err := w.Write(b[:n]); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		b = b[n:]
		if i == 10 {
			select {
			case s.sending <- struct{}{}:
			default:
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// upgradeStart builds the older release, sets the hub up with it in its own process,
// on central with HUB_MANIFEST, and logs the writers in.
func upgradeStart(t *testing.T) *upgradeRun {
	t.Helper()
	r := netPrepare(t)
	dir := t.TempDir()
	u := &upgradeRun{netRun: r, tool: filepath.Join(dir, "hub-tool"), channel: filepath.Join(dir, "channel"), from: "0.1.0"}
	u.src = &releaseSource{dir: u.channel, sending: make(chan struct{}, 1)}
	srv := httptest.NewServer(u.src)
	t.Cleanup(srv.Close)
	tk.BuildHub(t, "", u.tool, "")
	u.run(u.tool, "keygen", "-out", filepath.Join(dir, "release"))
	pub, _ := os.ReadFile(filepath.Join(dir, "release.pub"))
	const up = "github.com/dhis2/facility-hub/internal/upgrade"
	u.ldflags = "-X " + up + ".ReleaseKey=" + strings.TrimSpace(string(pub)) + " -X " + up + ".ReleaseBase=" + srv.URL

	// The older release, from git.
	old := filepath.Join(dir, "old")
	os.MkdirAll(old, 0o755)
	extract := exec.Command("sh", "-c", "git -C \"$(git rev-parse --show-toplevel)\" archive \"$0\" | tar -x -C \"$1\"", upgradeFrom, old)
	if out, err := extract.CombinedOutput(); err != nil {
		t.Fatalf("git archive %s: %v %s", upgradeFrom, err, out)
	}
	tk.BuildHub(t, old, filepath.Join(dir, "hub-"+u.from), u.ldflags+" -X main.version="+u.from)

	// Set up through the older release itself.
	r.p = r.e.DetachBinary(filepath.Join(dir, "hub-"+u.from), true)
	t.Cleanup(r.keepLog)
	r.e.SetupOnly(tk.RoleAdmin)
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if _, ok := r.e.M.User(role); ok {
			r.e.Web(role).Login()
		}
	}
	start := time.Now()
	r.p.WaitFor("the first pull", 15*time.Minute, func(s map[string]any) bool { return s["lastPull"] != nil && s["syncing"] == false })
	t.Logf("hub %s set up; first pull %s; run tag %s; hub log %s", u.from, time.Since(start).Round(time.Second), r.tag, r.p.Log)

	// The country's choice on central, put back as it was when the test ends.
	const choice = "/api/dataStore/facility-hub-admin/release"
	st, was, _ := r.e.Central.Do("GET", choice, nil)
	t.Cleanup(func() {
		if st == 200 {
			r.e.Central.Do("PUT", choice, was)
		} else {
			r.e.Central.Do("DELETE", choice, nil)
		}
	})
	return u
}

func (u *upgradeRun) run(name string, args ...string) {
	u.t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		u.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// offer builds this tree as version v, publishes it signed, and chooses it on central
// for every hub.
func (u *upgradeRun) offer(v string) {
	u.t.Helper()
	bin := filepath.Join(u.t.TempDir(), "hub-"+v)
	tk.BuildHub(u.t, "", bin, u.ldflags+" -X main.version="+v)
	u.run(u.tool, "release", "-key", filepath.Join(filepath.Dir(u.tool), "release.key"), "-dir", filepath.Join(u.channel, "v"+v),
		"-version", v, runtime.GOOS+"/"+runtime.GOARCH+"="+bin)
	body := map[string]any{"version": v, "pilotHubs": []string{}, "percent": 100}
	const choice = "/api/dataStore/facility-hub-admin/release"
	if st, b, err := u.e.Central.Do("PUT", choice, body); err != nil || st/100 != 2 { // a PUT creates the key
		u.t.Fatalf("choosing %s on central: %d %v %s", v, st, err, b)
	}
	u.t.Logf("offered %s at %s", v, time.Now().Format("15:04:05.000"))
}

// statusAt is the hub's status as polled, with the time.
type statusAt struct {
	At     time.Time
	Status map[string]any
}

// watchUntil polls the hub's status until ok, and returns what it saw (a nil status
// while the hub did not answer).
func (u *upgradeRun) watchUntil(what string, timeout time.Duration, ok func(map[string]any) bool) []statusAt {
	u.t.Helper()
	var seen []statusAt
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		s := u.p.Status()
		seen = append(seen, statusAt{time.Now(), s})
		if s != nil && ok(s) {
			return seen
		}
	}
	u.t.Fatalf("waiting for %s: timed out after %s; status %v\n%s", what, timeout, u.p.Status(), u.p.Tail(40))
	return nil
}

// upgraded: the hub runs v and has confirmed it (the health check passed).
func (u *upgradeRun) upgraded(v string) func(map[string]any) bool {
	return func(s map[string]any) bool {
		return s["version"] == v && s["locked"] == false && !exists(filepath.Join(u.e.Dir, "upgrade.json"))
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// G1: an upgrade to a new release while saves are queued and clients keep working.
// Must hold: the outbox is pushed before the swap; nothing lost or pushed twice;
// clients see at most a short refusal.
func TestG1UpgradeWithQueuedSaves(t *testing.T) {
	u := upgradeStart(t)
	stop := u.write(300 * time.Millisecond)
	defer stop()
	time.Sleep(10 * time.Second)
	u.outage(30 * time.Second)
	queued := u.p.Status()["pending"]
	u.offer("0.2.0")
	start := time.Now()
	u.e.Net.SetDown(false)
	seen := u.watchUntil("the upgrade", 10*time.Minute, u.upgraded("0.2.0"))
	done := time.Now()
	time.Sleep(20 * time.Second)
	stop()

	// The last status the old process gave, and how long the hub did not answer.
	var last map[string]any
	var gone, back time.Time
	for _, s := range seen {
		switch {
		case s.Status == nil && gone.IsZero():
			gone = s.At
		case s.Status != nil && gone.IsZero():
			last = s.Status
		case s.Status != nil && back.IsZero():
			back = s.At
		}
	}
	t.Logf("%v saves queued; upgraded %s after the uplink returned; the old release's last status: pending %v, version %v",
		queued, done.Sub(start).Round(time.Second), last["pending"], last["version"])
	if !gone.IsZero() {
		t.Logf("the hub did not answer for %s", back.Sub(gone).Round(100*time.Millisecond))
	}
	var refused []string
	for _, s := range u.ledger.all() {
		if !s.Confirmed && s.At.After(start) && s.At.Before(done) {
			refused = append(refused, s.At.Format("15:04:05.000")+" "+s.Err[:min(len(s.Err), 120)])
		}
	}
	t.Logf("saves not confirmed during the upgrade: %d %v", len(refused), first(refused))
	t.Logf("saves during the upgrade waited %s", u.waits(start, done))
	if last["pending"] != float64(0) {
		t.Errorf("the old release swapped with %v saves pending", last["pending"])
	}
	if u.p.Grep("upgrade result=ok from="+u.from+" to=0.2.0") != 1 {
		t.Errorf("the upgrade is not reported ok:\n%s", u.p.Tail(30))
	}
	u.finish(5 * time.Minute)
}

// E4: power cut during an upgrade: while the release downloads, while the new release
// migrates the store copy, while the store and binary are swapped, at the re-exec, and
// during the new release's health check. Must hold: after each, the hub comes back on
// the old or the new release with the store that release migrated, never in between;
// it can still upgrade afterwards; nothing confirmed is lost.
func TestE4KillDuringUpgrade(t *testing.T) {
	u := upgradeStart(t)
	stop := u.write(300 * time.Millisecond)
	defer stop()
	dir := u.e.Dir
	ino := func() uint64 {
		var st syscall.Stat_t
		syscall.Stat(u.p.Bin(), &st)
		return st.Ino
	}
	// waitFor polls cond every millisecond, and kills the hub when it holds.
	waitFor := func(what string, timeout time.Duration, cond func() bool) bool {
		for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(time.Millisecond) {
			if cond() {
				u.p.Kill()
				return true
			}
		}
		t.Errorf("%s: not seen in %s", what, timeout)
		return false
	}
	type outcome struct{ point, from, to, after, note string }
	var outcomes []outcome
	round := func(point, to string, cond func() bool) {
		from := u.p.Status()["version"].(string)
		results := u.p.Grep("msg=upgrade result=")
		if !waitFor(point, 5*time.Minute, cond) {
			return
		}
		files, _ := filepath.Glob(filepath.Join(dir, "hub.db*"))
		marker, _ := os.ReadFile(filepath.Join(dir, "upgrade.json"))
		t.Logf("killed %s (%s to %s) at %s; files %v; marker %s", point, from, to, time.Now().Format("15:04:05.000"),
			names(files), bytes.Join(bytes.Fields(marker), nil))
		u.restart()
		s := u.p.WaitFor("settled", 2*time.Minute, func(s map[string]any) bool {
			return s["locked"] == false && !exists(filepath.Join(dir, "upgrade.json"))
		})
		after := s["version"].(string)
		note := ""
		if u.p.Grep("msg=upgrade result=") > results {
			note = lastLine(u.p.Tail(400), "msg=upgrade result=")
		}
		t.Logf("after the restart: %s; %s", after, note)
		if after != from && after != to {
			t.Errorf("%s: back on %s, neither %s nor %s", point, after, from, to)
		}
		outcomes = append(outcomes, outcome{point, from, to, after, note})
	}
	migrating := func() bool {
		procs, _ := filepath.Glob("/proc/[0-9]*/cmdline")
		for _, p := range procs {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, []byte("migrate\x00-db\x00"+dir)) {
				return true
			}
		}
		return false
	}

	u.offer("0.2.0")
	u.src.slow.Store(true)
	round("during the download", "0.2.0", func() bool {
		select {
		case <-u.src.sending:
			return true
		default:
			return false
		}
	})
	u.src.slow.Store(false)
	round("during the migration", "0.2.0", migrating)
	round("during the swap (old store moved aside)", "0.2.0", func() bool { return exists(filepath.Join(dir, "hub.db.prev")) })

	next := "0.3.0"
	u.offer(next)
	before := ino()
	round("at the re-exec (binary replaced)", next, func() bool { return ino() != before })

	next = "0.4.0"
	u.offer(next)
	round("during the health check", next, func() bool {
		s := u.p.Status()
		return s != nil && s["version"] == next && exists(filepath.Join(dir, "upgrade.json"))
	})

	// Afterwards, an upgrade runs to its end.
	u.offer("0.5.0")
	u.watchUntil("the last upgrade", 10*time.Minute, u.upgraded("0.5.0"))
	time.Sleep(10 * time.Second)
	stop()
	for _, o := range outcomes {
		t.Logf("%-42s %s to %s: back on %s %s", o.point, o.from, o.to, o.after, o.note)
	}
	u.finish(5 * time.Minute)
}

func names(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

// lastLine is the last line of log that contains s, from s on.
func lastLine(log, s string) string {
	lines := strings.Split(log, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if _, after, ok := strings.Cut(lines[i], s); ok {
			return s + after
		}
	}
	return ""
}
