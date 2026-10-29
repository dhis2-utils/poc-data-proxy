package adversarial

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// K2: an event program's stage loses its data write sharing for the user's group
// while the user's events in it wait offline: two new events and an edit of one saved
// online. As B6, for events. Must hold: the waiting events refused and kept; offline
// reads and writes follow central at the next sync; nothing leaks.
func TestK2StageAccessLost(t *testing.T) {
	s := begin(t)
	p := s.eventProgram()
	de := s.numberElement(p)
	u := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	online := tk.UID()
	if st, _ := s.save(c, "an event saved online", s.programEvent(p, online, map[string]string{de: "1"})); st != 200 {
		t.Fatal("the hub refused a valid event online")
	}
	s.Sync()

	s.Offline()
	a, b := tk.UID(), tk.UID()
	for _, x := range []struct {
		what    string
		payload map[string]any
	}{
		{"a new event", s.programEvent(p, a, map[string]string{de: "2"})},
		{"another new event", s.programEvent(p, b, map[string]string{de: "3"})},
		{"an edit of the event saved online", s.programEvent(p, online, map[string]string{de: "4"})},
	} {
		if st, _ := s.save(c, x.what, x.payload); st != 200 {
			t.Fatal("the hub refused valid work offline")
		}
	}
	stage := p.Stages[0]
	s.RemoveSharing("programStage", stage, s.writingGroup(u, "programStage", stage))
	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{a: tk.RefusedKept, b: tk.RefusedKept, online: tk.RefusedKept}, tk.OnCentral, tk.RefusedKept)
	s.shows(c, "events", a)

	q := url.Values{"program": {p.ID}, "orgUnit": {s.M.Facility.ID}, "fields": {"event"}, "pageSize": {"200"}}
	for _, down := range []bool{false, true} {
		if down {
			s.Offline()
		}
		if l := s.leaks(c, "/api/tracker/events?"+q.Encode(), "events", "event"); len(l) > 0 {
			t.Errorf("I3: the hub (offline=%v) lists %d events central does not: %v", down, len(l), l)
		}
	}
	if st, _ := s.save(c, "a new event offline after the sync", s.programEvent(p, tk.UID(), map[string]string{de: "5"})); st == 200 {
		t.Error("the hub takes an event offline in a stage the user may no longer write")
	}
}

// K4: the sharing of an attribute category option changes on central while events
// under it wait offline: the user's group loses data write (and read) of the donor
// option the events carry. As A9. Must hold: as central decides on push, refusals
// kept; and between the change and the hub's learning of it (FINDINGS.md: category
// option sharing reaches the hub at the weekly full pull) what the hub shows and takes
// offline is measured: after the next sync, and after a full pull.
func TestK4AttributeOptionSharingChanged(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("adversarial scenarios run against a real DHIS2 (HUB_MANIFEST)")
	}
	ag := s.M.Aggregate
	var p tk.Program
	for _, x := range s.M.EventPrograms {
		if len(x.AOCs) > 0 {
			p = x
		}
	}
	if p.ID == "" || ag == nil || ag.DonorOptionNurse == "" {
		t.Skip("no event program with an attribute combination and a nurse's option in this setup")
	}
	aoc, option := ag.DonorAOCNurse, ag.DonorOptionNurse
	event := func(uid string) map[string]any {
		pl := s.programEvent(p, uid, nil)
		ev := pl["events"].([]any)[0].(map[string]any)
		ev["attributeOptionCombo"] = aoc
		return pl
	}
	u := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	online := tk.UID()
	if st, _ := s.save(c, "an event under the option, online", event(online)); st != 200 {
		t.Fatal("the hub refused a valid event online")
	}
	s.Sync()

	s.Offline()
	a, b := tk.UID(), tk.UID()
	for _, uid := range []string{a, b} {
		if st, _ := s.save(c, "a new event under the option", event(uid)); st != 200 {
			t.Fatal("the hub refused valid work offline")
		}
	}
	group := s.writingGroup(u, "categoryOption", option)
	s.RemoveSharing("categoryOption", option, group)
	t.Logf("option %s no longer shared with %s", option, group)
	s.ExpireTokenCache() // as an hour later: central's token cache keeps the user's access as it was (B1)
	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{online: tk.OnCentral}, tk.OnCentral, tk.RefusedKept)

	q := url.Values{"program": {p.ID}, "orgUnit": {s.M.Facility.ID}, "fields": {"event,attributeOptionCombo"}, "paging": {"false"}}
	check := func(when string) (leaked []string, took bool) {
		for _, down := range []bool{false, true} {
			if down {
				s.Offline()
			}
			l := s.leaks(c, "/api/tracker/events?"+q.Encode(), "events", "event")
			t.Logf("%s (offline=%v): the hub lists %d events under options central no longer shows the user", when, down, len(l))
			if down {
				leaked = l
			}
		}
		st, _ := s.save(c, when+": a new event under the option, offline", event(tk.UID()))
		s.Online()
		return leaked, st == 200
	}
	afterSync, tookAfterSync := check("after the next sync")
	s.App.Hub.Cfg.FullPullInterval = time.Nanosecond // the weekly full pull, now
	s.Sync()
	s.App.Hub.Cfg.FullPullInterval = 7 * 24 * time.Hour
	afterFull, tookAfterFull := check("after a full pull")
	t.Logf("offline after the sync: %d events shown central hides, a new one taken %v; after a full pull: %d shown, taken %v",
		len(afterSync), tookAfterSync, len(afterFull), tookAfterFull)
	if len(afterFull) > 0 || tookAfterFull {
		t.Errorf("I3: after a full pull the hub still shows (%d) or takes (%v) offline what central no longer lets the user",
			len(afterFull), tookAfterFull)
	}
	if len(afterSync) > 0 || tookAfterSync {
		finding(t, "K4: between a change of category option sharing and the next full pull, the hub shows and takes offline "+
			"events under the option (FINDINGS.md known gap: up to a week)")
		t.Errorf("I3: after the next sync the hub still shows %d events under the option, takes a new one: %v", len(afterSync), tookAfterSync)
	}
}

// K5: retention prunes events while staff work. Central holds events of the facility's
// event program from two years ago and from this month; the hub holds all of them,
// and a device has downloaded them. Offline, staff edit two old events and capture an
// old-dated one; then the country sets a retention of 12 months for the program (the
// hub reads it at its next catalog refresh) and the prune runs, while those edits wait,
// and again after they are pushed. Must hold: pending events never pruned; every save
// on central; and what a device holds does not disappear: an event the hub pruned is
// "needs connection" offline, never "not found", and never listed as deleted.
func TestK5RetentionDuringWork(t *testing.T) {
	s := begin(t)
	p := s.eventProgram()
	de := s.numberElement(p)
	h := s.App.Hub
	twoYears := h.CentralNow().AddDate(-2, 0, 0).Format(time.DateOnly)
	var old, recent []string
	for i := range 12 {
		uid := tk.UID()
		pl := s.programEvent(p, uid, map[string]string{de: "1"})
		if i < 8 {
			pl["events"].([]any)[0].(map[string]any)["occurredAt"] = twoYears
			old = append(old, uid)
		} else {
			recent = append(recent, uid)
		}
		s.CentralImport(pl)
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	s.Sync()
	for _, uid := range append(slices.Clone(old), recent...) {
		if h.Index.Get(uid) == nil {
			t.Fatalf("event %s on central is not on the hub before retention", uid)
		}
	}

	s.Offline()
	edited := old[:2]
	for _, uid := range edited {
		pl := s.programEvent(p, uid, map[string]string{de: "2"})
		pl["events"].([]any)[0].(map[string]any)["occurredAt"] = twoYears
		if st, _ := s.save(nurse, "an edit of an old event", pl); st != 200 {
			t.Fatal("the hub refused a valid edit offline")
		}
	}
	captured := tk.UID()
	// A value of this run: the ledger looks for duplicates by values.
	pl := s.programEvent(p, captured, map[string]string{de: fmt.Sprint(time.Now().UnixNano() % 1000000)})
	pl["events"].([]any)[0].(map[string]any)["occurredAt"] = twoYears
	if st, _ := s.save(nurse, "an old-dated event captured", pl); st != 200 {
		t.Fatal("the hub refused a valid event offline")
	}

	s.CentralSetRetention(`{"programs": {"` + p.ID + `": 12}}`)
	s.Online()
	if err := h.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Offline()
	n, err := h.PruneEvents()
	t.Logf("prune while the edits wait: %d events pruned (%v)", n, err)
	for _, uid := range append(slices.Clone(edited), captured) {
		if h.Index.Get(uid) == nil {
			t.Errorf("the pending event %s was pruned", uid)
		}
	}
	for _, uid := range old[2:] {
		if h.Index.Get(uid) != nil {
			t.Logf("old event %s, not edited, still held after the prune", uid)
		}
	}
	s.offlineView(nurse, p, append(slices.Clone(old), recent...), "while the edits wait")

	s.reconnect(2)
	// The edits of central's events are on central with central's author as creator.
	for _, ln := range s.l.Check() {
		t.Logf("ledger: %s", ln)
		edit := slices.Contains(edited, ln.UID) && ln.Outcome == tk.ChangedOnCentral && strings.HasPrefix(ln.Detail, "createdBy")
		if ln.Outcome != tk.OnCentral && !edit {
			t.Errorf("%s %s: %s %s", ln.Kind, ln.UID, ln.Outcome, ln.Detail)
		}
	}
	n, err = h.PruneEvents()
	t.Logf("prune after the push: %d more pruned (%v)", n, err)
	view := s.offlineView(nurse, p, append(append(slices.Clone(old), recent...), captured), "after the push")
	for uid, st := range view {
		if st == 404 || st == -1 {
			t.Errorf("an event a device holds reads offline as gone (%d): %s", st, uid)
		}
	}
}

// offlineView reads each event through the hub offline, as a device would, and lists
// the program's events as Android's download does (includeDeleted): per event its
// status (-1: listed as deleted).
func (s *scene) offlineView(c *tk.Client, p tk.Program, uids []string, when string) map[string]int {
	s.t.Helper()
	s.Offline()
	out := map[string]int{}
	counts := map[int]int{}
	for _, uid := range uids {
		st, _ := c.Do("GET", "/api/tracker/events/"+uid+"?fields=event", nil)
		out[uid] = st
		counts[st]++
	}
	q := url.Values{"program": {p.ID}, "orgUnit": {s.M.Facility.ID}, "fields": {"event,deleted"}, "paging": {"false"},
		"includeDeleted": {"true"}}
	st, b := c.Do("GET", "/api/tracker/events?"+q.Encode(), nil)
	var res struct {
		Events []struct {
			Event   string `json:"event"`
			Deleted bool   `json:"deleted"`
		} `json:"events"`
	}
	json.Unmarshal(b, &res)
	listed := 0
	for _, ev := range res.Events {
		if _, ok := out[ev.Event]; ok {
			listed++
			if ev.Deleted {
				out[ev.Event] = -1
			}
		}
	}
	s.t.Logf("%s, offline: single reads by status %v; Android's download (%d) lists %d of the %d", when, counts, st, listed, len(uids))
	return out
}
