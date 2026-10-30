package e2e

import (
	"fmt"
	"net/url"
	"slices"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// The oracle's tie order, fixed: events sharing an event date list by creation time,
// newest first, then by UID descending — as central orders them (measured on 2.42:
// ORDER BY <column> <dir>, created_at DESC, uid DESC, whatever the column's direction).
func TestEventListTieOrderMatchesCentral(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()

	// Six events in the event program sharing one event date, created at distinct
	// times (the hub stamps offline writes with its own time, a millisecond apart).
	e.offline()
	var created []string
	for i := range 6 {
		ev := fmt.Sprintf("TieEv%06d", i) // 11 characters
		created = append(created, ev)
		a.json("POST", postSync, map[string]any{"events": []any{map[string]any{"event": ev,
			"program": fc.EventProgram, "programStage": fc.EventStage, "orgUnit": fc.Facility,
			"occurredAt": "2026-10-25", "status": "ACTIVE", "dataValues": []any{}}}}, 200)
		time.Sleep(1100 * time.Millisecond)
	}
	reversed := slices.Clone(created)
	slices.Reverse(reversed)

	list := func(order string, page, size int) []string {
		t.Helper()
		q := url.Values{"orgUnit": {fc.Facility}, "orgUnitMode": {"SELECTED"}, "program": {fc.EventProgram},
			"pageSize": {fmt.Sprint(size)}, "paging": {"true"}, "fields": {"event"}, "page": {fmt.Sprint(page)}}
		if order != "" {
			q.Set("order", order)
		}
		res := a.json("GET", "/api/tracker/events?"+q.Encode(), nil, 200)
		var out []string
		for _, ev := range res["events"].([]any) {
			uid := ev.(map[string]any)["event"].(string)
			if slices.Contains(created, uid) {
				out = append(out, uid)
			}
		}
		return out
	}

	// Within the shared event date, central lists the newest created first, whatever
	// the date's direction.
	for _, order := range []string{"occurredAt:desc", "occurredAt:asc"} {
		if got := list(order, 1, 50); !slices.Equal(got, reversed) {
			t.Errorf("order=%q: %v, want the newest created first %v", order, got, reversed)
		}
	}
	if got := list("createdAt:desc", 1, 50); !slices.Equal(got, reversed) {
		t.Errorf("order=createdAt:desc: %v, want %v", got, reversed)
	}
	if got := list("createdAt:asc", 1, 50); !slices.Equal(got, created) {
		t.Errorf("order=createdAt:asc: %v, want %v", got, created)
	}

	// Pages read on from the last row of the page before, across the tie boundary:
	// the same order, no gaps, no repeats.
	var paged []string
	for pg := 1; pg <= 3; pg++ {
		paged = append(paged, list("occurredAt:desc", pg, 2)...)
	}
	if !slices.Equal(paged, reversed) {
		t.Errorf("paged 2 at a time: %v, want %v", paged, reversed)
	}
}
