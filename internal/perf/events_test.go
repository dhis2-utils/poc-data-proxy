package perf

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestEventPrograms measures event programs at size: two programs of
// HUB_PERF_EVENTS events each (dates over 36 months): the first pull, an incremental
// cycle, a working list's first and fiftieth page offline, one device's full download
// of a program (Android's paging), and pruning to 12 months.
func TestEventPrograms(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("HUB_PERF_EVENTS"))
	if n == 0 {
		t.Skip(`set HUB_PERF_EVENTS (e.g. 150000) to measure`)
	}
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Generate(fc.Scale{TEs: 1, Enrollments: 1, Events: 1, Programs: 1, Users: 1, EventPrograms: 2, ProgramEvents: n})
	timed := func(f func()) time.Duration {
		start := time.Now()
		f()
		return time.Since(start).Round(time.Millisecond)
	}
	first := timed(e.Setup)
	c := e.AndroidUser(fc.GenUser(0))
	c.Do("GET", "/api/me?fields=id", nil)
	userPull := timed(e.Sync)
	again := timed(e.Sync)
	h := e.App.Hub
	held := h.Store.CountKind("event")
	e.Offline()
	program := "GenEvP00000"
	list := func(page int) time.Duration {
		path := fmt.Sprintf("/api/tracker/events?program=%s&orgUnit=%s&order=occurredAt:desc&page=%d&pageSize=20&fields=event,occurredAt,status",
			program, fc.Facility, page)
		return timed(func() {
			if st, b := c.Do("GET", path, nil); st != 200 {
				t.Fatalf("working list page %d: %d %.200s", page, st, b)
			}
		})
	}
	p1, p50 := list(1), list(50)
	pages := 0
	download := timed(func() {
		for page := 1; ; page++ {
			path := fmt.Sprintf("/api/tracker/events?program=%s&orgUnit=%s&orgUnitMode=DESCENDANTS&includeDeleted=true&paging=true&page=%d&pageSize=50&fields=*",
				program, fc.Facility, page)
			st, b := c.Do("GET", path, nil)
			if st != 200 {
				t.Fatalf("download page %d: %d %.200s", page, st, b)
			}
			pages++
			if strings.Count(string(b), `"event":`) < 50 {
				return
			}
		}
	})
	e.Online()
	h.Store.SetState("retention", []byte(`{"defaultMonths": 12}`))
	var pruned int
	prune := timed(func() {
		var err error
		if pruned, err = h.PruneEvents(); err != nil {
			t.Fatal(err)
		}
	})
	out := strings.Join([]string{
		fmt.Sprintf("event programs: 2 x %d events; events held %d", n, held),
		fmt.Sprintf("set-up with the first pull: %s; the user's first pull: %s; next cycle: %s", first, userPull, again),
		fmt.Sprintf("offline working list (occurredAt desc, 20 a page): page 1 %s, page 50 %s", p1, p50),
		fmt.Sprintf("one device's download of a program: %d pages of 50 in %s", pages, download),
		fmt.Sprintf("retention 12 months: %d events pruned in %s; heap %.0f MB; data dir %.1f MB", pruned, prune, heapMB(), dirMB(e.Dir)),
	}, "\n")
	t.Log("\n" + out)
	if p := os.Getenv("HUB_PERF_OUT"); p != "" {
		os.WriteFile(p, []byte(out+"\n"), 0o644)
	}
}
