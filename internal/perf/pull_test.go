// Package perf measures the hub at scale against generated data (docs/PERFORMANCE.md).
package perf

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// scale reads HUB_PERF_SCALE ("tes,enrollments,events") and HUB_PERF_USERS.
func scale(t *testing.T) (fc.Scale, int) {
	v := os.Getenv("HUB_PERF_SCALE")
	if v == "" {
		t.Skip(`set HUB_PERF_SCALE="tes,enrollments,events" (e.g. "10000,15000,50000") to measure`)
	}
	var n [3]int
	for i, f := range strings.SplitN(v, ",", 3) {
		n[i], _ = strconv.Atoi(strings.TrimSpace(f))
	}
	users, _ := strconv.Atoi(os.Getenv("HUB_PERF_USERS"))
	return fc.Scale{TEs: n[0], Enrollments: n[1], Events: n[2], Programs: 6, Users: max(users, 1), Years: 5}, max(users, 1)
}

func heapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapInuse) / (1 << 20)
}

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

// TestPull fills a hub from the generated central with every user registered, and
// reports how long the first pull takes and what it costs in memory and disk, then
// the time to reopen the store after a reboot and a few offline reads.
func TestPull(t *testing.T) {
	s, users := scale(t)
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Generate(s)
	e.Setup()
	clients := make([]*tk.Client, users)
	for i := range clients {
		clients[i] = e.AndroidUser(fc.GenUser(i))
		if st, b := clients[i].Do("GET", "/api/me?fields=id", nil); st != 200 {
			t.Fatalf("%s: %d %s", fc.GenUser(i), st, b)
		}
	}
	base := heapMB()
	start := time.Now()
	e.Sync()
	pull := time.Since(start)
	objects := e.App.Hub.Index.Len()
	report := []string{
		fmt.Sprintf("scale: %d TEs, %d enrollments, %d events, %d users", s.TEs, s.Enrollments, s.Events, users),
		fmt.Sprintf("first pull: %s (%.0f objects/s), %d objects in the index", pull.Round(time.Millisecond), float64(objects)/pull.Seconds(), objects),
		fmt.Sprintf("heap after pull: %.0f MB (%.0f MB before)", heapMB(), base),
		fmt.Sprintf("store on disk: %.0f MB", dirMB(e.Dir)),
	}

	e.Offline()
	q := "/api/tracker/trackedEntities?program=" + fc.GenProgram(0) + "&orgUnits=" + fc.Facility +
		"&page=1&pageSize=15&order=createdAt:desc&fields=trackedEntity,attributes,enrollments[enrollment,status]"
	for _, path := range []string{q, "/api/tracker/trackedEntities?program=" + fc.GenProgram(0) + "&orgUnits=" + fc.Facility +
		"&filter=" + fc.AttrFirstName + ":like:Amina&pageSize=15&fields=trackedEntity"} {
		start := time.Now()
		st, b := clients[0].Do("GET", path, nil)
		report = append(report, fmt.Sprintf("offline %d in %s (%d bytes): %.90s", st, time.Since(start).Round(time.Millisecond), len(b), path))
	}

	start = time.Now()
	e.Reboot()
	clients[0].Do("GET", "/api/me?fields=id", nil) // the offline login unlocks the store and loads the index
	report = append(report, fmt.Sprintf("reboot to first offline login: %s, heap %.0f MB", time.Since(start).Round(time.Millisecond), heapMB()))

	out := strings.Join(report, "\n")
	t.Log("\n" + out)
	if p := os.Getenv("HUB_PERF_OUT"); p != "" {
		os.WriteFile(p, []byte(out+"\n"), 0o644)
	}
}
