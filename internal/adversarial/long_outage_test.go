package adversarial

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// D9: an outage of three weeks with continuous work, time-compressed. The work comes
// fast instead of over 21 days: the writers save as quickly as the hub answers until
// HUB_D9_SAVES saves (default 3 000: a facility registering about 100 people a day,
// with edits), while central gets work of its own (people registered and edited
// there). Then the hub's clocks are moved back three weeks: its last pull and last
// full pull, and the outbox entries' times, spread over the 21 days, as if each save
// had been made on its day. Central's clock cannot be moved: its tokens keep their
// real expiry (90 days from the setup; a country with 7-day tokens is B4's case: the
// saves wait for an online login). Must hold: the push and the pull complete
// afterwards, by the cycles alone; the tokens still work (every save on central by
// its author); the outbox and the data directory stay within what the disk holds.
func TestD9ThreeWeeksOffline(t *testing.T) {
	const days = 21
	want := 3000
	if n, err := strconv.Atoi(os.Getenv("HUB_D9_SAVES")); err == nil && n > 0 {
		want = n
	}
	r := netPrepare(t)
	e := r.e
	e.Setup()
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if _, ok := e.M.User(role); ok {
			e.Web(role).Login()
		}
	}
	e.Sync()
	h := e.App.Hub
	t.Logf("run tag %s; data directory %.1f MB after the first pull, %d objects", r.tag, dirMB(e.Dir), h.Index.Len())

	e.Offline()
	start := time.Now()
	stop := r.write(0)
	for len(r.ledger.all()) < want {
		time.Sleep(time.Second)
	}
	stop()
	work := time.Since(start)
	t.Logf("offline work: %s in %s; outbox %d entries pending, %d objects pending; data directory %.1f MB",
		r.ledger.summary(), work.Round(time.Second), h.Store.CountOutbox(store.OutboxPending),
		h.Store.CountObjects(store.SyncPending), dirMB(e.Dir))

	// Central's work during the three weeks: people registered there, one edited.
	var onCentral []netSave
	for i := range 100 {
		onCentral = append(onCentral, r.centralPerson(strconv.Itoa(10+i%50)))
	}
	r.centralEdit(onCentral[0], "99")

	// Three weeks pass on the hub.
	back := func(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }
	db := h.Store.DB()
	if _, err := db.Exec(`UPDATE pull_state SET last_pull_at=?, last_full_at=?`, back(days*24*time.Hour), back(days*24*time.Hour)); err != nil {
		t.Fatalf("moving the hub's last pulls back: %v", err)
	}
	// Pending entry n of N made on day 21*n/N.
	rows, err := db.Query(`SELECT seq FROM outbox WHERE state=? ORDER BY seq`, store.OutboxPending)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for rows.Next() {
		var seq int64
		rows.Scan(&seq)
		seqs = append(seqs, seq)
	}
	rows.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	span := time.Duration(days) * 24 * time.Hour
	for i, seq := range seqs {
		at := time.Now().Add(-span + span*time.Duration(i)/time.Duration(len(seqs))).UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`UPDATE outbox SET created_at=? WHERE seq=?`, at, seq); err != nil {
			t.Fatalf("moving the outbox's times back: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	h.Store.SetPrivate("last_pull", back(days*24*time.Hour))
	h.Store.SetPrivate("last_push", back(days*24*time.Hour))

	// The uplink returns: the cycles alone must push and pull everything.
	e.Online()
	e.Net.Calls()
	reconnect := time.Now()
	var cycles []string
	for i := 0; ; i++ {
		c := time.Now()
		err := e.App.Worker.Cycle(context.Background())
		calls := e.Net.Calls()
		pushes := 0
		for _, x := range calls {
			if x.Method == "POST" && x.URL.Path == "/api/tracker" {
				pushes++
			}
		}
		pending := h.Store.CountOutbox(store.OutboxPending)
		cycles = append(cycles, fmt.Sprintf("%s: %d requests, %d pushes, %d pending, err %v",
			time.Since(c).Round(time.Second), len(calls), pushes, pending, err))
		t.Logf("cycle %d: %s", i+1, cycles[len(cycles)-1])
		if pending == 0 && err == nil {
			break
		}
		if i >= 30 {
			t.Fatalf("the outbox did not drain in 30 cycles: %v", cycles)
		}
	}
	t.Logf("after three weeks offline: pushed and pulled in %d cycles, %s; data directory %.1f MB; stuck %d, refused %d",
		len(cycles), time.Since(reconnect).Round(time.Second), dirMB(e.Dir), h.StuckOutbox(), h.Store.CountObjects(store.SyncRejected))
	for _, u := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if mu, ok := e.M.User(u); ok {
			su, _ := h.Store.UserByName(mu.Username)
			if su != nil {
				t.Logf("%s: token expires %s, needs online login %v", mu.Username, su.PATExpires, su.NeedsLogin)
				if su.NeedsLogin {
					t.Errorf("%s needs an online login after the outage: the token did not last", mu.Username)
				}
			}
		}
	}

	c := r.check()
	t.Logf("central: %s", c)
	if !c.ok() {
		t.Errorf("invariants broken: %s", c)
	}
	// Central's three weeks reached the hub: each person registered there is read
	// offline, with the edit.
	e.Offline()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	missing := 0
	for i, s := range onCentral {
		st, b := nurse.Do("GET", "/api/tracker/events/"+s.Event+"?fields=event,dataValues", nil)
		want := s.Value
		if i == 0 {
			want = "99"
		}
		var o map[string]any
		json.Unmarshal(b, &o)
		if st != 200 || value(o, r.de) != want {
			missing++
			if missing <= 3 {
				t.Logf("central's event %s offline: %d %.200s", s.Event, st, b)
			}
		}
	}
	if missing > 0 {
		t.Errorf("%d of %d people registered on central during the outage not on the hub after it", missing, len(onCentral))
	}
}

// dirMB is the size of the files under dir.
func dirMB(dir string) float64 {
	var n int64
	filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return float64(n) / (1 << 20)
}
