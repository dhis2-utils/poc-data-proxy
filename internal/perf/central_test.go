package perf

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestCentralLoad measures what one hub asks of central (docs/PERFORMANCE.md, "Load on
// central"): requests and answer sizes by kind, for setting up and filling the hub,
// for a sync cycle in which nothing changed, and for a full pull, then projects them
// to a day at the default intervals and to many hubs (HUB_PERF_HUBS, default 500).
func TestCentralLoad(t *testing.T) {
	s, users := scale(t)
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Generate(s)
	start := time.Now()
	e.Setup()
	for i := 0; i < users; i++ {
		if st, b := e.AndroidUser(fc.GenUser(i)).Do("GET", "/api/me?fields=id", nil); st != 200 {
			t.Fatalf("%s: %d %s", fc.GenUser(i), st, b)
		}
	}
	e.Sync()
	fill, fillTime := e.Net.Calls(), time.Since(start)

	start = time.Now()
	e.Sync()
	quiet, quietTime := e.Net.Calls(), time.Since(start)

	if _, err := e.App.Hub.Store.DB().Exec(`UPDATE pull_state SET last_full_at = ''`); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	e.Sync()
	full, fullTime := e.Net.Calls(), time.Since(start)

	hubs := 500
	fmt.Sscan(os.Getenv("HUB_PERF_HUBS"), &hubs)
	cfg := config.Default() // the intervals a deployed hub uses (the test runs cycles by hand)
	report := []string{fmt.Sprintf("scale: %d TEs, %d enrollments, %d events, %d users, %d programs",
		s.TEs, s.Enrollments, s.Events, users, s.Programs)}
	phases := []struct {
		name  string
		calls []tk.Call
		took  time.Duration
	}{{"setup and first fill", fill, fillTime}, {"sync cycle, nothing changed", quiet, quietTime}, {"full pull", full, fullTime}}
	for _, p := range phases {
		report = append(report, "", fmt.Sprintf("%s (%s):", p.name, p.took.Round(time.Millisecond)))
		report = append(report, table(p.calls)...)
	}

	// A day at the deployed intervals. The test runs the ownership check and the
	// access refresh in every cycle, so the quiet cycle's requests are counted by what
	// they are for: ownership checks per OwnershipCheckInterval, access and catalog
	// reads per AccessRefreshInterval, the rest per SyncInterval; the full pull's
	// listings per FullPullInterval.
	perDay := func(d time.Duration) float64 { return float64(24*time.Hour) / float64(d) }
	day := map[string][2]float64{}
	add := func(calls []tk.Call, times func(kind string) float64) {
		for _, c := range calls {
			k := kind(c)
			n := times(k)
			if n == 0 {
				continue
			}
			v := day[k]
			day[k] = [2]float64{v[0] + n, v[1] + n*float64(c.Bytes)}
		}
	}
	add(quiet, func(k string) float64 {
		switch {
		case strings.HasPrefix(k, "ownership"):
			return perDay(cfg.OwnershipCheckInterval)
		case refreshKind(k):
			return perDay(cfg.AccessRefreshInterval)
		}
		return perDay(cfg.SyncInterval)
	})
	add(full, func(k string) float64 {
		if k == "list identifiers (full)" {
			return perDay(cfg.FullPullInterval)
		}
		return 0
	})
	var reqs, bytes float64
	keys := make([]string, 0, len(day))
	for k := range day {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	report = append(report, "", fmt.Sprintf("per day (a sync every %s, ownership checked every %s, access refreshed every %s, a full pull every %s):",
		cfg.SyncInterval, cfg.OwnershipCheckInterval, cfg.AccessRefreshInterval, cfg.FullPullInterval))
	for _, k := range keys {
		v := day[k]
		report = append(report, fmt.Sprintf("  %-34s %10.0f requests %10.1f MB", k, v[0], v[1]/1e6))
		reqs += v[0]
		bytes += v[1]
	}
	report = append(report, fmt.Sprintf("  %-34s %10.0f requests %10.1f MB", "total", reqs, bytes/1e6),
		fmt.Sprintf("%d hubs like this one: %.0f requests a second on average, %.1f GB a day from central",
			hubs, reqs*float64(hubs)/86400, bytes*float64(hubs)/1e9))

	out := strings.Join(report, "\n")
	t.Log("\n" + out)
	if p := os.Getenv("HUB_PERF_OUT"); p != "" {
		os.WriteFile(p, []byte(out+"\n"), 0o644)
	}
}

// refreshKind: the access and catalog reads of a refresh.
func refreshKind(k string) bool {
	return k == "GET /api/me" || k == "GET /api/programs" || k == "GET /api/trackedEntityTypes" || k == "GET /api/relationshipTypes"
}

var singleTE = regexp.MustCompile(`^/api/tracker/trackedEntities/[A-Za-z0-9]{11}$`)

// kind names what a request to central is for.
func kind(c tk.Call) string {
	q := c.URL.Query()
	p := c.URL.Path
	switch {
	case p == "/api/tracker/trackedEntities" && strings.HasPrefix(q.Get("fields"), "trackedEntity,updatedAt"):
		if q.Get("enrollmentStatus") != "" {
			return "list active enrollments (full)"
		}
		if q.Get("updatedAfter") != "" {
			return "list changed since (incremental)"
		}
		return "list identifiers (full)"
	case p == "/api/tracker/trackedEntities" && q.Get("trackedEntities") != "":
		return "fetch records"
	case p == "/api/tracker/trackedEntities" && q.Get("fields") == "trackedEntity":
		return "ownership: list owned"
	case singleTE.MatchString(p):
		return "ownership: read one"
	case p == "/api/tracker" && c.Method == "POST":
		return "push"
	}
	// Anything else by its first path segments (metadata, access, tokens).
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 4)
	return c.Method + " /" + strings.Join(parts[:min(len(parts), 2)], "/")
}

func table(calls []tk.Call) []string {
	type agg struct {
		n     int
		bytes int64
	}
	m := map[string]*agg{}
	for _, c := range calls {
		k := kind(c)
		if m[k] == nil {
			m[k] = &agg{}
		}
		m[k].n++
		m[k].bytes += c.Bytes
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out []string
	var n int
	var b int64
	for _, k := range keys {
		out = append(out, fmt.Sprintf("  %-34s %8d requests %10.1f MB", k, m[k].n, float64(m[k].bytes)/1e6))
		n += m[k].n
		b += m[k].bytes
	}
	return append(out, fmt.Sprintf("  %-34s %8d requests %10.1f MB", "total", n, float64(b)/1e6))
}
