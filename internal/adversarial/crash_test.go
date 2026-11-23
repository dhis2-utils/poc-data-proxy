package adversarial

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// restart starts the killed hub and unlocks its store with a login, as the first
// member of staff to arrive would (the data key lives only in memory).
func (r *netRun) restart() {
	r.t.Helper()
	r.p.Start()
	c := r.e.Web(tk.RoleNurse)
	if st, _, b, err := c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": r.e.M.Password}); err != nil || st != 200 {
		r.t.Fatalf("login after the restart: %d %v %.300s", st, err, b)
	}
	r.p.WaitFor("unlocked", time.Minute, func(s map[string]any) bool { return s["locked"] == false })
}

// E1: power cut during saves, online and offline. Every confirmed save survives; a
// save cut short is on central whole or not at all.
func TestE1KillDuringSaves(t *testing.T) {
	r := netStart(t)
	stop := r.write(100 * time.Millisecond)
	defer stop()
	for i := range 6 {
		if i == 3 {
			r.e.Net.SetDown(true) // offline: saves go only to the hub's store
		}
		time.Sleep(3*time.Second + rand.N(3*time.Second))
		r.p.Kill()
		t.Logf("kill %d at %s (uplink down %v)", i+1, time.Now().Format("15:04:05.000"), r.e.Net.Down())
		time.Sleep(2 * time.Second)
		r.restart()
	}
	r.e.Net.SetDown(false)
	time.Sleep(10 * time.Second)
	stop()
	r.finish(5 * time.Minute)
	if n := r.p.Grep("object rejected"); n > 0 {
		t.Errorf("%d objects rejected", n)
	}
}

// E2: power cut during a push: once while central has stored an entry the hub is
// still waiting to hear about, then at random moments of the drain. No duplicate on
// central after restart, nothing lost.
func TestE2KillDuringPush(t *testing.T) {
	r := netStart(t)
	stop := r.write(100 * time.Millisecond)
	r.outage(30 * time.Second)
	stop()
	lost := r.e.Net.LoseAnswer(1, tk.IsPush)
	r.e.Net.SetDown(false)
	select {
	case <-lost:
		r.p.Kill()
		t.Logf("killed while central had stored a push")
	case <-time.After(2 * time.Minute):
		t.Fatal("no push")
	}
	for i := range 3 {
		r.restart()
		time.Sleep(rand.N(1500 * time.Millisecond))
		r.p.Kill()
		t.Logf("kill %d during the drain", i+2)
	}
	r.restart()
	r.finish(5 * time.Minute)
	if n := r.p.Grep("object rejected"); n > 0 {
		t.Errorf("%d objects rejected", n)
	}
}

// E3: power cut during the first pull. The hub resumes, and holds every person as
// central has them: no half-stored people.
func TestE3KillDuringFirstPull(t *testing.T) {
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("adversarial scenarios run against a real DHIS2 (HUB_MANIFEST)")
	}
	e.SetupOnly(tk.RoleAdmin) // no first sync: the process makes it
	e.Web(tk.RoleNurse).Login()
	e.Net.SetRate(100_000) // a pull long enough to cut
	p := e.Detach()
	r := &netRun{t: t, e: e, p: p, tag: "e3"}
	t.Cleanup(r.keepLog)
	unlock := func() {
		c := e.Web(tk.RoleAdmin)
		if st, _, b, err := c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": e.M.Password}); err != nil || st != 200 {
			t.Fatalf("login: %d %v %.300s", st, err, b)
		}
	}
	unlock()
	// A small facility's whole first pull can be fewer reads than the later kill points
	// (Sierra Leone's is about 27): once a kill finds the pull finished, the kills stop
	// and the comparison with central below still holds.
kills:
	for i, after := range []int{3, 15, 40} {
		pulls := 0
		for pulls < after {
			for _, c := range e.Net.Calls() {
				if c.Method == "GET" && strings.HasPrefix(c.URL.Path, "/api/tracker/") {
					pulls++
				}
			}
			if s := p.Status(); s["lastPull"] != nil {
				if i == 0 {
					t.Fatalf("the first pull ended before the first kill (%d tracker reads)", pulls)
				}
				t.Logf("the first pull ended before kill %d (%d tracker reads since the last): no more kills", i+1, pulls)
				break kills
			}
			time.Sleep(50 * time.Millisecond)
		}
		p.Kill()
		t.Logf("kill %d after %d tracker reads of the first pull", i+1, pulls)
		p.Start()
		unlock()
	}
	e.Net.SetRate(0)
	p.WaitFor("the first pull", 10*time.Minute, func(s map[string]any) bool { return s["lastPull"] != nil && s["syncing"] == false })

	// The hub, offline, against central, as the nurse.
	nurse, _ := e.M.User(tk.RoleNurse)
	cen := e.Central.As(nurse.Username, e.M.Password)
	type held map[string][]string // person -> enrollments and events
	read := func(get func(path string) []byte, prog string) held {
		q := url.Values{"program": {prog}, "orgUnits": {e.M.Facility.ID}, "paging": {"false"},
			"fields": {"trackedEntity,enrollments[enrollment,program,events[event]]"}}
		var res struct {
			TEs []struct {
				TE   string `json:"trackedEntity"`
				Enrs []struct {
					Enr     string `json:"enrollment"`
					Program string `json:"program"`
					Events  []struct {
						Event string `json:"event"`
					} `json:"events"`
				} `json:"enrollments"`
			} `json:"trackedEntities"`
		}
		if err := json.Unmarshal(get("/api/tracker/trackedEntities?"+q.Encode()), &res); err != nil {
			t.Fatalf("program %s: %v", prog, err)
		}
		out := held{}
		for _, te := range res.TEs {
			var ids []string
			for _, en := range te.Enrs {
				if en.Program != prog {
					continue
				}
				ids = append(ids, en.Enr)
				for _, ev := range en.Events {
					ids = append(ids, ev.Event)
				}
			}
			slices.Sort(ids)
			out[te.TE] = ids
		}
		return out
	}
	central := map[string]held{}
	for _, prog := range e.M.Programs {
		central[prog.ID] = read(func(path string) []byte {
			st, b, err := cen.Do("GET", path, nil)
			if err != nil || st != 200 {
				t.Fatalf("central %s: %d %v", path, st, err)
			}
			return b
		}, prog.ID)
	}
	e.Net.SetDown(true)
	p.WaitFor("offline", time.Minute, tk.Offline)
	c := e.Web(tk.RoleNurse)
	c.Login()
	var missing, half []string
	total := 0
	for _, prog := range e.M.Programs {
		hub := read(func(path string) []byte {
			st, _, b, err := c.Try("GET", path, nil)
			if err != nil || st != 200 {
				t.Fatalf("hub %s: %d %v %.300s", path, st, err, b)
			}
			return b
		}, prog.ID)
		for te, want := range central[prog.ID] {
			total++
			got, ok := hub[te]
			switch {
			case !ok:
				missing = append(missing, prog.ID+"/"+te)
			case !slices.Equal(got, want):
				half = append(half, fmt.Sprintf("%s/%s: hub %v, central %v", prog.ID, te, got, want))
			}
		}
	}
	t.Logf("%d people on central at the facility; %d missing on the hub %v; %d held in part %v", total, len(missing), first(missing), len(half), first(half))
	if len(missing)+len(half) > 0 {
		t.Errorf("after the kills the hub does not hold the facility as central does")
	}
}

// E1, after the restart: the store is locked until a member of staff logs in, and
// several arrive at once (the Android apps sync on their own). Each login unlocks the
// store and rebuilds the index; saves made meanwhile must still be stored.
func TestE1ConcurrentUnlockAfterRestart(t *testing.T) {
	r := netStart(t)
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	failed := 0
	for round := range 10 {
		r.p.Kill()
		r.p.Start()
		// As in TestE1KillDuringSaves: web clients log in again while Android apps
		// save with Basic auth, each a few saves in a row.
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := r.e.Android(tk.RoleNurse)
				if i%2 == 0 {
					c = r.e.Web([]string{tk.RoleNurse, tk.RoleRestricted}[i%4/2])
					c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": r.e.M.Password})
				}
				w := &netWriter{r: r, c: c, web: i%2 == 0}
				for range 5 {
					w.once()
				}
			}()
		}
		wg.Wait()
		for _, s := range r.ledger.all() {
			if s.Status == 500 {
				failed++
			}
			if !s.Confirmed && s.Status != 500 {
				t.Logf("round %d: not confirmed: %d %s", round+1, s.Status, s.Err)
			}
		}
		t.Logf("round %d: %d saves failed with 500 so far", round+1, failed)
		r.ledger = &netLedger{saves: confirmedOnly(r.ledger.all())}
	}
	if n := r.p.Grep("UNIQUE constraint failed: objects.slot"); n > 0 || failed > 0 {
		t.Errorf("%d saves failed with 500 after a restart; %d slot collisions in the log", failed, n)
	}
}

func confirmedOnly(saves []netSave) []netSave {
	var out []netSave
	for _, s := range saves {
		if s.Status != 500 {
			out = append(out, s)
		}
	}
	return out
}
