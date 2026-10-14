package perf

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestQueries fills a hub like TestPull and times the queries that matter, offline:
// working lists, name searches, an Android download's first and middle pages, and a
// dashboard. Queries read the store's data file (docs/PERFORMANCE.md, "The query index
// in SQLite"), so their times should not grow with the facility's size, apart from
// the full scans named as such.
func TestQueries(t *testing.T) {
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
	start := time.Now()
	e.Sync()
	pull := time.Since(start)
	e.Offline()
	report := []string{
		fmt.Sprintf("scale: %d TEs, %d enrollments, %d events, %d users; %d objects", s.TEs, s.Enrollments, s.Events, users,
			e.App.Hub.Index.Len()),
		fmt.Sprintf("first pull: %s; heap %.0f MB; store on disk %.0f MB (%s)", pull.Round(time.Millisecond), heapMB(),
			dirMB(e.Dir), files(e.Dir)),
	}

	c := clients[0]
	prog, ou := fc.GenProgram(0), fc.Facility
	list := "/api/tracker/trackedEntities?program=" + prog + "&orgUnits=" + ou
	var people int
	e.App.Hub.Store.DB().QueryRow(`SELECT count(DISTINCT te_uid) FROM objects WHERE kind = 'enrollment' AND program = ?`, prog).Scan(&people)
	deep := people/50/2 + 1
	_, b := c.Do("GET", list+"&page=1&pageSize=1&fields=trackedEntity,enrollments[enrollment]", nil)
	var one struct {
		TEs []struct {
			TE          string `json:"trackedEntity"`
			Enrollments []struct {
				Enrollment string `json:"enrollment"`
			} `json:"enrollments"`
		} `json:"trackedEntities"`
	}
	json.Unmarshal(b, &one)
	if len(one.TEs) == 0 || len(one.TEs[0].Enrollments) == 0 {
		t.Fatalf("no person to open: %.300s", b)
	}
	te, enr := one.TEs[0].TE, one.TEs[0].Enrollments[0].Enrollment

	cases := []struct{ name, path string }{
		{"working list, page 1 of 15", list + "&page=1&pageSize=15&order=createdAt:desc" +
			"&fields=trackedEntity,attributes[attribute,value],enrollments[enrollment,status,enrolledAt]"},
		{"working list, with total (full scan)", list + "&page=1&pageSize=15&order=createdAt:desc&totalPages=true&fields=trackedEntity"},
		{"working list, active only", list + "&enrollmentStatus=ACTIVE&page=1&pageSize=15&order=createdAt:desc&fields=trackedEntity"},
		{"name search, common name", list + "&filter=" + fc.AttrFirstName + ":like:Amina&pageSize=15&fields=trackedEntity,attributes[attribute,value]"},
		{"name search, first and last name", list + "&filter=" + fc.AttrFirstName + ":like:Amina&filter=" + fc.AttrLastName +
			":like:Phiri&pageSize=15&fields=trackedEntity"},
		{"name search, nobody (full scan)", list + "&filter=" + fc.AttrFirstName + ":like:Zelda&pageSize=15&fields=trackedEntity"},
		{"Android download, page 1 of 50", list + "&orgUnitMode=DESCENDANTS&page=1&pageSize=50&paging=true" + androidFields},
		{"Android download, page 10 of 50 (the last with the 500 limit)", list + "&orgUnitMode=DESCENDANTS&page=10&pageSize=50&paging=true" + androidFields},
		{"working list, page 3 of 15", list + "&page=3&pageSize=15&order=createdAt:desc&fields=trackedEntity"},
		{fmt.Sprintf("Android download, page %d of 50", deep), list + "&orgUnitMode=DESCENDANTS&page=" + fmt.Sprint(deep) +
			"&pageSize=50&paging=true" + androidFields},
		{"Android sync, everything changed today (worst case)", list + "&orgUnitMode=DESCENDANTS&page=1&pageSize=50&paging=true&updatedAfter=" +
			time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05.000") + androidFields},
		{"dashboard: one person", "/api/tracker/trackedEntities/" + te + "?program=" + prog + "&fields=*"},
		{"dashboard: their events", "/api/tracker/events?enrollments=" + enr + "&fields=*&paging=false"},
	}
	report = append(report, fmt.Sprintf("%-42s %22s  %s", "query: median (first) of 15", "time", "answer"))
	// HUB_PERF_ONLY (a regular expression) picks cases, HUB_PERF_RUNS the runs of each
	// (15): for profiling one kind of request.
	only := regexp.MustCompile(os.Getenv("HUB_PERF_ONLY"))
	runs := 15
	fmt.Sscan(os.Getenv("HUB_PERF_RUNS"), &runs)
	for _, tc := range cases {
		if !only.MatchString(tc.name) {
			continue
		}
		var size, status int
		d := median(runs, func() {
			var b []byte
			status, b = c.Do("GET", tc.path, nil)
			size = len(b)
		})
		if status != 200 {
			t.Errorf("%s: %d", tc.name, status)
		}
		report = append(report, fmt.Sprintf("%-42s %22s  %d bytes", tc.name, d, size))
	}

	start = time.Now()
	e.Reboot()
	clients[0].Do("GET", "/api/me?fields=id", nil) // the offline login unlocks the store
	report = append(report, fmt.Sprintf("reboot to first offline login: %s; heap %.0f MB", time.Since(start).Round(time.Millisecond), heapMB()))

	out := strings.Join(report, "\n")
	t.Log("\n" + out)
	if p := os.Getenv("HUB_PERF_OUT"); p != "" {
		os.WriteFile(p, []byte(out+"\n"), 0o644)
	}
}

// files lists the store's files over 1 MB with their sizes.
func files(dir string) string {
	var out []string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && fi.Size() > 1<<20 {
			out = append(out, fmt.Sprintf("%s %.0f MB", filepath.Base(p), float64(fi.Size())/(1<<20)))
		}
		return nil
	})
	return strings.Join(out, ", ")
}

const androidFields = "&fields=trackedEntity,orgUnit,attributes[attribute,value],enrollments[enrollment,program,status," +
	"events[event,programStage,status,dataValues[dataElement,value]]]"

type timing struct{ first, median time.Duration }

func (t timing) String() string {
	r := func(d time.Duration) time.Duration { return d.Round(10 * time.Microsecond) }
	return fmt.Sprintf("%s (%s)", r(t.median), r(t.first))
}

// median runs fn n times: the median, and the first run.
func median(n int, fn func()) timing {
	d := make([]time.Duration, n)
	for i := range d {
		start := time.Now()
		fn()
		d[i] = time.Since(start)
	}
	first := d[0]
	slices.Sort(d)
	return timing{first, d[n/2]}
}
