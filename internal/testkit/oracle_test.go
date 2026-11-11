package testkit

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// A page of an ordered event list whose boundary falls inside a group of events on the
// same day: membership alone excuses the different members of the group, but the UID
// sequences must still match — a different tie order is a DIFFERS.
func TestJudgePagedTies(t *testing.T) {
	q := Query{Kind: "events", Program: "P", Path: "/api/tracker/events?order=occurredAt%3Adesc&orgUnit=F&page=1&pageSize=4&program=P"}
	ev := func(uid, day string) map[string]any {
		return map[string]any{"event": uid, "occurredAt": "2026-10-" + day + "T00:00:00.000"}
	}
	page := func(evs ...map[string]any) []byte {
		b, _ := json.Marshal(map[string]any{"page": 1, "pageSize": 4, "events": evs})
		return b
	}
	truth := &Truth{Owned: map[string]map[string]bool{"P": {}}}
	for _, uid := range []string{"A", "B", "C", "D", "E", "X"} {
		truth.Owned["P"][uid] = true
	}
	central := page(ev("A", "20"), ev("B", "19"), ev("C", "18"), ev("D", "18"))

	// The same page: a match.
	o := Judge(truth, q, "u", 200, central, 200, page(ev("A", "20"), ev("B", "19"), ev("C", "18"), ev("D", "18")))
	if o.Verdict != Match {
		t.Errorf("the same page: %s %v %s, want MATCH", o.Verdict, o.Objects, o.Detail)
	}
	// A tie group paged differently: the membership excuse applies, but the sequences
	// differ, so the verdict is DIFFERS with the first UIDs of each side.
	o = Judge(truth, q, "u", 200, central, 200, page(ev("A", "20"), ev("B", "19"), ev("C", "18"), ev("E", "18")))
	if o.Verdict != Differs || !strings.Contains(o.Detail, "order differs") ||
		!strings.Contains(o.Detail, "[A B C E]") || !strings.Contains(o.Detail, "[A B C D]") {
		t.Errorf("tie group paged differently: %s %q, want DIFFERS with the order detail", o.Verdict, o.Detail)
	}
	// An event outside the tie group that central does not page: a leak, though it
	// pushes a tied event off the hub's page.
	o = Judge(truth, q, "u", 200, central, 200, page(ev("A", "20"), ev("X", "19"), ev("B", "19"), ev("C", "18")))
	if o.Verdict != Leak || !slices.Equal(o.Objects, []string{"X"}) {
		t.Errorf("extra event outside the tie: %s %v, want LEAK [X]", o.Verdict, o.Objects)
	}
	// One central pages above the tie and the hub does not: missing.
	o = Judge(truth, q, "u", 200, central, 200, page(ev("A", "20"), ev("C", "18"), ev("D", "18"), ev("E", "18")))
	if o.Verdict != Missing || !slices.Equal(o.Objects, []string{"B"}) {
		t.Errorf("missing event above the tie: %s %v, want MISSING [B]", o.Verdict, o.Objects)
	}
	// A short page is the whole list: no tie is excused.
	short := page(ev("A", "20"), ev("B", "19"), ev("C", "18"))
	o = Judge(truth, q, "u", 200, short, 200, page(ev("A", "20"), ev("B", "19"), ev("C", "18"), ev("E", "18")))
	if o.Verdict != Leak || fmt.Sprint(o.Objects) != "[E]" {
		t.Errorf("short page: %s %v, want LEAK [E]", o.Verdict, o.Objects)
	}
	// Unpaged lists are compared whole.
	q.Path = "/api/tracker/events?order=occurredAt%3Adesc&orgUnit=F&paging=false&pageSize=4&program=P"
	o = Judge(truth, q, "u", 200, central, 200, page(ev("A", "20"), ev("B", "19"), ev("C", "18"), ev("E", "18")))
	if o.Verdict != Leak {
		t.Errorf("unpaged list: %s %v, want LEAK", o.Verdict, o.Objects)
	}
}

// An incremental query: the hub may re-send what the user sees anyway (W1), but must
// return everything central returns, and nothing the user may not see.
func TestJudgeSince(t *testing.T) {
	q := Query{Kind: "events", Program: "P", Path: "/api/tracker/events?orgUnit=F&paging=false&program=P&updatedAfter=2026-11-01",
		Unfiltered: "/api/tracker/events?orgUnit=F&paging=false&program=P"}
	list := func(uids ...string) []byte {
		var evs []map[string]any
		for _, u := range uids {
			evs = append(evs, map[string]any{"event": u})
		}
		b, _ := json.Marshal(map[string]any{"events": evs})
		return b
	}
	truth := &Truth{Owned: map[string]map[string]bool{"P": {"A": true, "B": true, "C": true}}}
	visible := VisibleIn(q, list("A", "B", "C"))
	central := list("A")
	for _, c := range []struct {
		name, want string
		hub        []byte
	}{
		{"the same", Match, list("A")},
		{"re-sends a visible event", Match, list("A", "B")},
		{"an event the user may not see", Leak, list("A", "Z")},
		{"misses a changed event", Missing, list("B")},
	} {
		if o := JudgeSince(truth, q, "u", 200, central, 200, c.hub, visible); o.Verdict != c.want {
			t.Errorf("%s: %s %v, want %s", c.name, o.Verdict, o.Objects, c.want)
		}
	}
	if o := Judge(truth, q, "u", 200, central, 200, list("A", "B")); o.Verdict != Leak {
		t.Errorf("Judge without the margin: %s, want LEAK", o.Verdict)
	}
}

// The generator filters and orders each program's lists on attributes of other
// programs only (review R-03), and not on its own or another tracked entity type's.
func TestQueriesAcrossPrograms(t *testing.T) {
	m := &Manifest{Facility: OrgUnit{ID: "Fac"}, Programs: []Program{
		{ID: "Opd", TET: "Person", Stages: []string{"St1"}},
		{ID: "Hiv", TET: "Person", Stages: []string{"St2"}, OnlyAttrs: []string{"HivNo"}},
		{ID: "Hhs", TET: "House", Stages: []string{"St3"}, OnlyAttrs: []string{"HhNo"}},
	}}
	has := func(qs []Query, kind, param, value string) bool {
		for _, q := range qs {
			u, _ := url.Parse(q.Path)
			if q.Kind == kind && u.Query().Get(param) == value {
				return true
			}
		}
		return false
	}
	opd := Queries(m, m.Programs[0], map[string]string{"only:HivNo": "ART-1"})
	for _, want := range [][3]string{{"events", "filterAttributes", "HivNo:!null"}, {"events", "filterAttributes", "HivNo:sw:A"},
		{"trackedEntities", "order", "HivNo:desc"}} {
		if !has(opd, want[0], want[1], want[2]) {
			t.Errorf("no %s query with %s=%s", want[0], want[1], want[2])
		}
	}
	if has(opd, "events", "filterAttributes", "HhNo:!null") {
		t.Error("filtered on another tracked entity type's attribute")
	}
	if has(Queries(m, m.Programs[1], nil), "trackedEntities", "order", "HivNo:desc") {
		t.Error("ordered on the program's own attribute as if it were another's")
	}
}
