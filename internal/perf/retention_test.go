package perf

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestTrackerRetention fills a hub at HUB_PERF_SCALE without retention and another
// with a 12-month window on every program (docs/superpowers/specs/2026-10-16-tracker-
// retention-design.md): the first fill's time, objects, disk and requests to central;
// on the second, a full pull after pruning, which must fetch no one again; on the
// first, the time to prune it to the same window.
func TestTrackerRetention(t *testing.T) {
	s, users := scale(t)
	const window = `{"defaultMonths": 12}`
	var report []string
	fill := func(t *testing.T, retention string) *tk.Env {
		e := tk.Start(t)
		e.NeedFake()
		e.Fake.Generate(s)
		// The fill starts with set-up: the setup user's pull and each user's first login
		// pull, as TestCentralLoad counts it. Retention counts only signed for an approved
		// hub, so it is set (approving the hub) between set-up and the first pull.
		e.Net.Calls()
		start := time.Now()
		e.SetupOnly(tk.RoleAdmin)
		if retention != "" {
			e.CentralSetRetention(retention)
		}
		e.Sync()
		for i := 0; i < users; i++ {
			if st, b := e.AndroidUser(fc.GenUser(i)).Do("GET", "/api/me?fields=id", nil); st != 200 {
				t.Fatalf("%s: %d %s", fc.GenUser(i), st, b)
			}
		}
		e.Sync()
		took, calls := time.Since(start), e.Net.Calls()
		name := "without retention"
		if retention != "" {
			name = "with " + retention
		}
		report = append(report, "", fmt.Sprintf("first fill %s: %s; %s; store on disk %.0f MB",
			name, took.Round(time.Millisecond), counts(t, e), dirMB(e.Dir)))
		report = append(report, table(calls)...)
		return e
	}
	// fullPull forces a full pull and reports it, with the tracked entities fetched.
	fullPull := func(t *testing.T, e *tk.Env, name string) int {
		if _, err := e.App.Hub.Store.DB().Exec(`UPDATE pull_state SET last_full_at = ''`); err != nil {
			t.Fatal(err)
		}
		e.Net.Calls()
		start := time.Now()
		e.Sync()
		took, calls := time.Since(start), e.Net.Calls()
		fetched := 0
		for _, c := range calls {
			if v := c.URL.Query().Get("trackedEntities"); v != "" {
				fetched += len(strings.Split(v, ","))
			}
		}
		report = append(report, "", fmt.Sprintf("full pull %s: %s, %d tracked entities fetched; %s",
			name, took.Round(time.Millisecond), fetched, counts(t, e)))
		report = append(report, table(calls)...)
		return fetched
	}
	t.Run("with", func(t *testing.T) {
		e := fill(t, window)
		if fetched := fullPull(t, e, "with retention, after pruning"); fetched != 0 {
			t.Errorf("a full pull after pruning fetched %d tracked entities again", fetched)
		}
	})
	t.Run("without", func(t *testing.T) {
		e := fill(t, "")
		fullPull(t, e, "without retention")
		e.App.Hub.Store.SetState("retention", []byte(window))
		start := time.Now()
		stubbed, deleted, err := e.App.Hub.PruneTracker()
		if err != nil {
			t.Fatal(err)
		}
		report = append(report, "", fmt.Sprintf("pruning to %s: %s, %d enrollments stubbed, %d objects deleted; %s",
			window, time.Since(start).Round(time.Millisecond), stubbed, deleted, counts(t, e)))
		if stubbed == 0 || deleted == 0 {
			t.Errorf("nothing pruned: %d stubbed, %d deleted", stubbed, deleted)
		}
	})
	out := fmt.Sprintf("scale: %d TEs, %d enrollments, %d events, %d users, %d programs",
		s.TEs, s.Enrollments, s.Events, users, s.Programs) + "\n" + strings.Join(report, "\n")
	t.Log("\n" + out)
	if p := os.Getenv("HUB_PERF_OUT"); p != "" {
		os.WriteFile(p, []byte(out+"\n"), 0o644)
	}
}

// counts reports the hub's rows by kind, and its stubs.
func counts(t *testing.T, e *tk.Env) string {
	rows, err := e.App.Hub.Store.DB().Query(`SELECT kind, count(*), sum(stub) FROM objects GROUP BY kind ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var kind string
		var n, stubs int
		rows.Scan(&kind, &n, &stubs)
		if stubs > 0 {
			parts = append(parts, fmt.Sprintf("%d %ss (%d stubs)", n, kind, stubs))
		} else {
			parts = append(parts, fmt.Sprintf("%d %ss", n, kind))
		}
	}
	return strings.Join(parts, ", ")
}
