package testkit

import (
	"encoding/json"
	"fmt"
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
