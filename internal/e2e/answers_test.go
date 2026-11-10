package e2e

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
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
// see of the program, its people included (tracked entities belong to no program).
func TestPullRateLimitKeepsAccess(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	a := e.web("nurse_a")
	a.login()
	e.sync()
	u, _ := h.Store.UserByName("nurse_a")
	before := seenOf(t, h, u.ID)
	if before[fc.Program] == 0 || before[""] == 0 {
		t.Fatalf("the nurse sees too little: %v", before)
	}
	e.central.FailPaths = map[string]int{"/api/tracker/trackedEntities": 429}
	h.Cfg.FullPullInterval = time.Nanosecond // a full pull now
	e.app.Worker.Cycle(context.Background()) // reports the 429s
	h.Cfg.FullPullInterval = 7 * 24 * time.Hour
	e.central.FailPaths = nil
	if after := seenOf(t, h, u.ID); after[fc.Program] != before[fc.Program] || after[""] != before[""] {
		t.Fatalf("the nurse sees %v after a rate limit, %v before (program objects, tracked entities)", after, before)
	}
}

// A 403 page that is not DHIS2's (a proxy's), or a listing answered with {}, says
// nothing about access either (review R-34): the nurse keeps what she sees.
func TestPullForeignAnswerKeepsAccess(t *testing.T) {
	for name, answer := range map[string]func(http.ResponseWriter){
		"proxy 403": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(403)
			w.Write([]byte("<html><body>Access denied by proxy</body></html>"))
		},
		"empty object": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("{}"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			var foreign atomic.Bool
			e.cSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if foreign.Load() && r.URL.Path == "/api/tracker/trackedEntities" {
					answer(w)
					return
				}
				e.central.ServeHTTP(w, r)
			})
			e.setup()
			h := e.app.Hub
			a := e.web("nurse_a")
			a.login()
			e.sync()
			u, _ := h.Store.UserByName("nurse_a")
			before := seenOf(t, h, u.ID)
			if before[fc.Program] == 0 || before[""] == 0 {
				t.Fatalf("the nurse sees too little: %v", before)
			}
			foreign.Store(true)
			h.Cfg.FullPullInterval = time.Nanosecond // a full pull now
			e.app.Worker.Cycle(context.Background())
			h.Cfg.FullPullInterval = 7 * 24 * time.Hour
			foreign.Store(false)
			if after := seenOf(t, h, u.ID); after[fc.Program] != before[fc.Program] || after[""] != before[""] {
				t.Fatalf("the nurse sees %v after the answer, %v before (program objects, tracked entities)", after, before)
			}
		})
	}
}

// A person central no longer shows the nurse disappears from her view at the next
// full pull that completes.
func TestFullPullHidesWhoCentralNoLongerLists(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	a := e.web("nurse_a")
	a.login()
	e.sync()
	u, _ := h.Store.UserByName("nurse_a")
	te := h.Index.Get("SeedTe00001")
	if te == nil || !h.Index.VisibleTo(te, u.ID) {
		t.Fatal("the nurse does not see the seeded person")
	}
	e.central.Move("SeedTe00001", fc.OtherFacility)
	h.Cfg.FullPullInterval = time.Nanosecond // a full pull now
	e.sync()
	h.Cfg.FullPullInterval = 7 * 24 * time.Hour
	if h.Index.VisibleTo(h.Index.Get("SeedTe00001"), u.ID) {
		t.Fatal("the nurse still sees a person central no longer lists for her")
	}
}

// seenOf counts what userID sees by program; "" counts tracked entities.
func seenOf(t *testing.T, h *core.Hub, userID int64) map[string]int {
	t.Helper()
	tes := map[int64]bool{}
	rows, err := h.Store.DB().Query(`SELECT slot FROM objects WHERE kind = 'trackedEntity'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s int64
		rows.Scan(&s)
		tes[s] = true
	}
	rows.Close()
	var slots []int64
	for _, s := range h.Index.VisibleSlots(userID) {
		slots = append(slots, int64(s))
	}
	o, err := h.Store.SlotOrigins(slots)
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for s, v := range o {
		switch {
		case tes[s]:
			n[""]++
		case v.Program != "":
			n[v.Program]++
		}
	}
	return n
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

// One import central answers 500 to (a bug or a lock its payload trips) does not hold
// up the saves after it (H8): they go on; it is tried again each cycle, and refused
// after a day of 500s, as an answer that is no import report is.
func TestPushGoesOnPastAFailingImport(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient("BadTe000001", "BadEn000001", "Bad", "", nil), 200)
	a.json("POST", postSync, newPatient("GooTe000001", "GooEn000001", "Good", "", nil), 200)
	e.central.FailImportsOf = map[string]int{"BadTe000001": 500}
	e.online()
	e.sync()
	if e.central.Object("GooTe000001") == nil {
		t.Fatal("the save after the failing import did not reach central")
	}
	if n := h.Store.CountOutbox(store.OutboxPending); n != 1 {
		t.Fatalf("pending after the 500: %d", n)
	}
	if n := h.Store.CountIssues(); n != 0 {
		t.Fatalf("%d refusals after the first 500", n)
	}
	// A day later, still 500.
	day := time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := h.Store.DB().Exec(`UPDATE outbox SET failing_since = ? WHERE failing_since <> ''`, day); err != nil {
		t.Fatal(err)
	}
	e.sync()
	if n := h.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("still pending after a day of 500s: %d", n)
	}
	if o := h.Index.Get("BadTe000001"); o == nil || o.SyncState != store.SyncRejected {
		t.Fatalf("not marked refused: %+v", o)
	}
}

// An import report of a shape the hub does not know (bundleReport renamed, as a new
// major might) is no import report: it says nothing about the saves, which stay
// pending and are not marked refused (N14).
func TestPushReshapedReportIsNoReport(t *testing.T) {
	e := newEnv(t)
	var reshape atomic.Bool
	e.cSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !reshape.Load() || r.Method != "POST" || r.URL.Path != "/api/tracker" {
			e.central.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		e.central.ServeHTTP(rec, r)
		w.Header().Set("Content-Type", rec.Header().Get("Content-Type"))
		w.WriteHeader(rec.Code)
		w.Write(bytes.ReplaceAll(rec.Body.Bytes(), []byte(`"bundleReport"`), []byte(`"importReport"`)))
	})
	e.setup()
	h := e.app.Hub
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient("ShpTe000001", "ShpEn000001", "Shape", "", nil), 200)
	reshape.Store(true)
	e.online()
	e.sync()
	if n := h.Store.CountIssues(); n != 0 {
		t.Fatalf("%d refusals after a reshaped report", n)
	}
	if o := h.Index.Get("ShpTe000001"); o == nil || o.SyncState == store.SyncRejected {
		t.Fatalf("marked refused: %+v", o)
	}
	if n := h.Store.CountOutbox(store.OutboxPending); n != 1 {
		t.Fatalf("pending after a reshaped report: %d", n)
	}
	reshape.Store(false)
	e.sync()
	if n := h.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("pending after central answered as before: %d", n)
	}
	if o := h.Index.Get("ShpTe000001"); o == nil || o.SyncState != store.SyncSynced {
		t.Fatalf("not synced: %+v", o)
	}
}

// An aggregate edit central answers 500 to follows the tracker rule (review R-21): the
// saves after it go on, and it is refused after a day of 500s, with an issue.
func TestAggregatePushGoesOnPastAFailingEdit(t *testing.T) {
	e := newEnv(t)
	e.setup()
	h := e.app.Hub
	a := e.web("nurse_a")
	a.login()
	e.sync()
	e.offline()
	q := url.Values{"de": {fc.AggDeNum}, "co": {fc.AggCOC}, "ds": {fc.AggDataSet}, "ou": {fc.Facility},
		"pe": {time.Now().Format("200601")}, "value": {"7"}}
	if st, b := a.do("POST", "/api/dataValues?"+q.Encode(), nil); st/100 != 2 {
		t.Fatalf("offline value: %d %s", st, b)
	}
	a.json("POST", postSync, newPatient("AggTe000001", "AggEn000001", "After", "", nil), 200)
	e.central.FailPaths = map[string]int{"/api/dataValues": 500}
	e.online()
	e.sync()
	if e.central.Object("AggTe000001") == nil {
		t.Fatal("the save after the failing aggregate edit did not reach central")
	}
	if n := h.Store.CountAggregatePending(); n != 1 {
		t.Fatalf("aggregate edits pending after the 500: %d", n)
	}
	day := time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := h.Store.DB().Exec(`UPDATE outbox SET failing_since = ? WHERE failing_since <> ''`, day); err != nil {
		t.Fatal(err)
	}
	e.sync()
	if n := h.Store.CountAggregatePending(); n != 0 {
		t.Fatalf("still pending after a day of 500s: %d", n)
	}
	if n := h.Store.CountIssues(); n == 0 {
		t.Fatal("no refusal recorded after a day of 500s")
	}
}
