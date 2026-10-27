package e2e

import (
	"context"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/store"
)

// Answers that say nothing about the saves (a rate limit, a redirect, an answer cut
// off after central imported) are not refusals: the save stays pending and reaches
// central, once, on the next cycle.
func TestPushRetriesAnswersFromTheWay(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*fc.Central)
	}{
		{"429", func(c *fc.Central) { c.FailPaths = map[string]int{"/api/tracker": 429} }},
		{"302", func(c *fc.Central) { c.FailPaths = map[string]int{"/api/tracker": 302} }},
		{"200 cut off", func(c *fc.Central) { c.CutResponse = map[string]int{"POST /api/tracker": 1} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.setup()
			h := e.app.Hub
			a := e.web("nurse_a")
			a.login()
			e.offline()
			a.json("POST", postSync, newPatient("WayTe000001", "WayEn000001", "Way", "", nil), 200)
			c.set(e.central)
			e.online()
			e.sync()
			if n := h.Store.CountOutbox(store.OutboxPending); n != 1 {
				t.Fatalf("pending after the first answer: %d", n)
			}
			if n := h.Store.CountIssues(); n != 0 {
				t.Fatalf("%d refusals recorded", n)
			}
			if o := h.Index.Get("WayTe000001"); o == nil || o.SyncState == store.SyncRejected {
				t.Fatalf("marked refused: %+v", o)
			}
			e.central.FailPaths = nil
			e.sync()
			if n := h.Store.CountOutbox(store.OutboxPending); n != 0 {
				t.Fatalf("pending after central answered: %d", n)
			}
			if e.central.Object("WayTe000001") == nil {
				t.Fatal("not on central")
			}
			if n := countOf(e.central.Order(), "WayTe000001"); n != 1 {
				t.Fatalf("created %d times on central", n)
			}
			if n := h.Store.CountIssues(); n != 0 {
				t.Fatalf("%d refusals recorded", n)
			}
		})
	}
}

// A rate limit during a full pull says nothing about access: the users keep what they
// see of the program.
func TestPullRateLimitKeepsAccess(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	a := e.web("nurse_a")
	a.login()
	e.sync()
	u, _ := h.Store.UserByName("nurse_a")
	seen := func() int { // objects of the program the nurse sees
		var slots []int64
		for _, s := range h.Index.VisibleSlots(u.ID) {
			slots = append(slots, int64(s))
		}
		o, _ := h.Store.SlotOrigins(slots)
		n := 0
		for _, v := range o {
			if v[1] == fc.Program {
				n++
			}
		}
		return n
	}
	before := seen()
	if before == 0 {
		t.Fatal("the nurse sees nothing of the program")
	}
	e.central.FailPaths = map[string]int{"/api/tracker/trackedEntities": 429}
	h.Cfg.FullPullInterval = time.Nanosecond // a full pull now
	e.app.Worker.Cycle(context.Background()) // reports the 429s
	h.Cfg.FullPullInterval = 7 * 24 * time.Hour
	e.central.FailPaths = nil
	if n := seen(); n != before {
		t.Fatalf("the nurse sees %d objects of the program after a rate limit, %d before", n, before)
	}
}

func countOf(s []string, v string) int {
	n := 0
	for i := range s {
		if s[i] == v {
			n++
		}
	}
	return n
}

// A non-report answer that is not a temporary one (a proxy's 413 page, say) keeps the
// save pending, but not for ever: after a day of such answers the entry is refused,
// with the answer, so a manager sees it and it no longer holds up the saves after it.
func TestPushRefusesAfterADayOfOtherAnswers(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient("DayTe000001", "DayEn000001", "Day", "", nil), 200)
	e.central.FailHTML = map[string]int{"/api/tracker": 413}
	e.online()
	e.sync()
	if n := h.Store.CountOutbox(store.OutboxPending); n != 1 {
		t.Fatalf("pending after the first 413 page: %d", n)
	}
	if n := h.Store.CountIssues(); n != 0 {
		t.Fatalf("%d refusals after the first 413 page", n)
	}
	// A day later, still the same answer.
	day := time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := h.Store.DB().Exec(`UPDATE outbox SET failing_since = ? WHERE failing_since <> ''`, day); err != nil {
		t.Fatal(err)
	}
	e.sync()
	if n := h.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("still pending after a day of 413 pages: %d", n)
	}
	if n := h.Store.CountIssues(); n == 0 {
		t.Fatal("no refusal recorded after a day of 413 pages")
	}
	if o := h.Index.Get("DayTe000001"); o == nil || o.SyncState != store.SyncRejected {
		t.Fatalf("not marked refused: %+v", o)
	}
}
