package adversarial

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// lanSwitch is the facility network between the devices and the hub: up, down (a
// request never leaves the device), or losing answers (the request reaches the hub,
// the answer never reaches the device).
type lanSwitch struct {
	mode atomic.Int32
	lost atomic.Int64 // answers lost on their way back
}

const (
	lanUp int32 = iota
	lanDown
	lanLoseAnswers
)

var errLANDown = errors.New("facility network down: no route to the hub")

func (l *lanSwitch) wrap(c *tk.Client) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	c.HTTP.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		switch l.mode.Load() {
		case lanDown:
			return nil, errLANDown
		case lanLoseAnswers:
			resp, err := base.RoundTrip(req)
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				l.lost.Add(1)
			}
			base.CloseIdleConnections()
			return nil, errors.New("facility network dropped: the answer was lost")
		}
		return base.RoundTrip(req)
	})
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// lanDevice is a device that keeps what it could not send, as the Android app keeps
// unsynced records in its database: a save without an answer stays queued on the
// device with its UIDs and is sent again, unchanged, when the hub answers again.
type lanDevice struct {
	mu    sync.Mutex // held while it works
	r     *netRun
	c     *tk.Client
	web   bool
	mine  []netSave // confirmed registrations
	queue []lanItem
	sent  int // resends that got an answer
	most  int // longest queue
}

type lanItem struct {
	s       netSave
	payload map[string]any
	tries   int
}

// once sends the queue first (oldest first, stopping at the first that gets no
// answer), then makes one new save: every third an update of an earlier registration.
func (d *lanDevice) once() {
	for len(d.queue) > 0 {
		it := d.queue[0]
		it.tries++
		if !d.send(&it) {
			d.queue[0] = it
			break
		}
		d.queue = d.queue[1:]
		d.sent++
	}
	r := d.r
	s := netSave{User: d.c.User, Value: strconv.Itoa(30 + rand.N(60)), At: time.Now()}
	var payload map[string]any
	if len(d.mine) > 0 && rand.N(3) == 0 {
		prev := d.mine[rand.N(len(d.mine))]
		s.TE, s.Enr, s.Event, s.Update = prev.TE, prev.Enr, prev.Event, true
		payload = map[string]any{"events": []any{r.event(s)}}
	} else {
		pr := r.e.M.NewPerson(r.prog, r.tag+strings.ToLower(tk.UID()[:5]))
		s.TE, s.Enr, s.Event = pr.TE, pr.Enrollment, tk.UID()
		te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
		te["enrollments"].([]any)[0].(map[string]any)["events"] = []any{r.event(s)}
		payload = pr.Payload
	}
	it := lanItem{s: s, payload: payload, tries: 1}
	if len(d.queue) > 0 || !d.send(&it) { // keep the order: behind what waits
		d.queue = append(d.queue, it)
		d.most = max(d.most, len(d.queue))
	}
}

// send posts one save; false when no answer came (the device keeps it). An answer
// is recorded in the ledger, confirmed or not; a session that ended is renewed with
// an online login and the save sent again.
func (d *lanDevice) send(it *lanItem) bool {
	r := d.r
	for range 2 {
		start := time.Now()
		st, h, b, err := d.c.Try("POST", tk.SyncImport, it.payload)
		if err != nil {
			return false
		}
		if (st == 401 || st == 302) && d.web {
			if st, _, _, err := d.c.Try("POST", "/api/auth/login", map[string]string{"username": d.c.User, "password": r.e.M.Password}); err != nil || st != 200 {
				return false
			}
			continue
		}
		s := it.s
		s.Status, s.Dur = st, time.Since(start)
		s.Confirmed = accepted(st, b)
		s.Online = h.Get("X-Hub-Mode") == ""
		if !s.Confirmed {
			s.Err = fmt.Sprintf("%d %.300s", st, b)
		}
		r.ledger.add(s)
		if s.Confirmed && !s.Update {
			d.mine = append(d.mine, s)
		}
		return true
	}
	return false
}

// D10: the facility network goes down while the hub and its uplink stay up: the
// devices cannot reach the hub, central can. First the network drops while answers
// are on their way (the hub stored or passed on the save, the device never heard),
// then it is down for longer than a web session lasts, while central registers a
// person at the facility. The devices keep their unsent saves and send them again,
// unchanged, when the network returns (as the Android app does; Capture web's own
// offline queue needs the real app). Must hold: the hub stays online and keeps
// syncing; every save a device kept reaches central once, by its author (I1, I2);
// the saves whose answers were lost are not duplicated when resent; the person
// central registered meanwhile is on the hub for the devices when they return.
func TestD10FacilityNetworkDown(t *testing.T) {
	r := netPrepare(t)
	r.e.ProcConfig = []string{"sync_interval: 30s", "session_max: 3m", "session_idle: 2m"}
	r.begin()
	e, p := r.e, r.p
	lan := &lanSwitch{}
	var devs []*lanDevice
	for _, w := range r.writers() {
		lan.wrap(w.c)
		devs = append(devs, &lanDevice{r: r, c: w.c, web: w.web})
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for _, d := range devs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				case <-time.After(700*time.Millisecond + rand.N(600*time.Millisecond)):
				}
				d.mu.Lock()
				d.once()
				d.mu.Unlock()
			}
		}()
	}
	queued := func() (n int) {
		for _, d := range devs {
			d.mu.Lock()
			n += len(d.queue)
			d.mu.Unlock()
		}
		return n
	}
	modes := map[string]int{}
	var watchMu sync.Mutex
	stopWatch := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopWatch:
				return
			case <-time.After(time.Second):
			}
			if s := p.Status(); s != nil {
				watchMu.Lock()
				modes[fmt.Sprint(s["mode"])]++
				watchMu.Unlock()
			}
		}
	}()

	time.Sleep(time.Minute)
	t.Logf("before: %s", r.ledger.summary())

	// The network drops while answers are on their way.
	lan.mode.Store(lanLoseAnswers)
	time.Sleep(10 * time.Second)
	t.Logf("10 s losing answers: %d answers lost; %d saves queued on the devices", lan.lost.Load(), queued())

	// Down for 5 minutes: longer than a web session (3 minutes here).
	lan.mode.Store(lanDown)
	down := time.Now()
	e.Net.Calls()
	pullsBefore := p.Status()["lastPull"]
	time.Sleep(time.Minute)
	cen := r.centralPerson("77")
	t.Logf("registered %s on central at %s", cen.TE, time.Now().Format("15:04:05"))
	time.Sleep(4 * time.Minute)
	calls := e.Net.Calls()
	st := p.Status()
	t.Logf("5 min down: hub %v, pending %v, last pull %v (was %v); to central %s; %d saves queued on the devices",
		st["mode"], st["pending"], st["lastPull"], pullsBefore, callStats(calls, time.Since(down)), queued())

	// Back: the devices send what they kept.
	lan.mode.Store(lanUp)
	back := time.Now()
	for queued() > 0 && time.Since(back) < 5*time.Minute {
		time.Sleep(200 * time.Millisecond)
	}
	emptied := time.Since(back)
	time.Sleep(30 * time.Second)
	close(done)
	wg.Wait()
	close(stopWatch)
	var most, resent int
	for _, d := range devs {
		most, resent = max(most, d.most), resent+d.sent
	}
	t.Logf("network back: the devices' queues (longest %d) sent in %s, %d resends answered; hub modes seen (by second): %v",
		most, emptied.Round(time.Second), resent, modes)
	if left := queued(); left > 0 {
		t.Errorf("%d saves still queued on the devices %s after the network returned", left, time.Since(back).Round(time.Second))
	}
	if modes["offline"] > 0 {
		t.Errorf("the hub went offline %d s while only the facility network was down", modes["offline"])
	}

	// The person central registered while the devices were cut off is on the hub, for
	// them, offline too.
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	online, _ := nurse.Do("GET", "/api/tracker/trackedEntities/"+cen.TE+"?program="+r.prog.ID+"&fields=trackedEntity", nil)
	e.Net.SetDown(true)
	p.WaitFor("offline", time.Minute, tk.Offline)
	offline, _ := nurse.Do("GET", "/api/tracker/trackedEntities/"+cen.TE+"?program="+r.prog.ID+"&fields=trackedEntity", nil)
	e.Net.SetDown(false)
	p.WaitFor("online", time.Minute, tk.Online)
	t.Logf("the person central registered during the cut, read by the nurse through the hub: online %d, offline %d", online, offline)
	if offline != 200 {
		t.Errorf("the person registered on central while the devices were cut off is not on the hub offline (%d)", offline)
	}

	// Saves whose answer was lost: answered when resent, on central once.
	var unconfirmed []string
	for _, s := range r.ledger.all() {
		if !s.Confirmed {
			unconfirmed = append(unconfirmed, s.Err[:min(len(s.Err), 120)])
		}
	}
	if len(unconfirmed) > 0 {
		t.Errorf("%d saves answered but not confirmed: %v", len(unconfirmed), first(unconfirmed))
	}
	r.finish(5 * time.Minute)
}
