package perf

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestFullDownload downloads every person of every program from an offline hub the
// way eRegisters does (docs/FINDINGS.md, "eRegisters through the hub"): 50 per page,
// its nested fields, no limit per program. It reports each program's pages, the
// time of pages at several depths and the total, to show how the cost of one
// device's full download grows with the facility.
func TestFullDownload(t *testing.T) {
	s, _ := scale(t)
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Generate(s)
	e.Setup()
	c := e.AndroidUser(fc.GenUser(0))
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("login: %d %s", st, b)
	}
	e.Sync()
	e.Offline()

	report := []string{fmt.Sprintf("scale: %d TEs, %d enrollments, %d events; %d objects", s.TEs, s.Enrollments, s.Events,
		e.App.Hub.Index.Len())}
	report = append(report, fmt.Sprintf("%-12s %6s %10s %10s %10s %10s %10s", "program", "pages", "page 1", "25 %", "50 %", "last", "total"))
	var all time.Duration
	pages := 0
	for p := 0; p < s.Programs; p++ {
		var times []time.Duration
		for page := 1; ; page++ {
			path := "/api/tracker/trackedEntities?program=" + fc.GenProgram(p) + "&orgUnits=" + fc.Facility +
				"&orgUnitMode=SELECTED&page=" + fmt.Sprint(page) + "&pageSize=50&fields=" + eregistersFields
			start := time.Now()
			st, b := c.Do("GET", path, nil)
			times = append(times, time.Since(start))
			if st != 200 {
				t.Fatalf("%s page %d: %d %.200s", fc.GenProgram(p), page, st, b)
			}
			var res struct {
				Pager struct {
					NextPage string `json:"nextPage"`
				} `json:"pager"`
				TEs []json.RawMessage `json:"trackedEntities"`
			}
			json.Unmarshal(b, &res)
			if res.Pager.NextPage == "" || len(res.TEs) < 50 {
				break
			}
		}
		var total time.Duration
		for _, d := range times {
			total += d
		}
		at := func(f float64) time.Duration { return times[int(f*float64(len(times)-1))].Round(time.Millisecond) }
		report = append(report, fmt.Sprintf("%-12s %6d %10s %10s %10s %10s %10s", fc.GenProgram(p), len(times),
			at(0), at(0.25), at(0.5), at(1), total.Round(time.Millisecond)))
		all += total
		pages += len(times)
	}
	report = append(report, fmt.Sprintf("one device's full download: %d pages in %s", pages, all.Round(time.Millisecond)))
	out := strings.Join(report, "\n")
	t.Log("\n" + out)
	if p := os.Getenv("HUB_PERF_OUT"); p != "" {
		os.WriteFile(p, []byte(out+"\n"), 0o644)
	}
}

// eregistersFields is what eRegisters 1.1.7 asks for in its pull (src/machines/sync.ts).
const eregistersFields = "trackedEntity,createdAt,updatedAt,createdAtClient,updatedAtClient,orgUnit,trackedEntityType,inactive," +
	"deleted,potentialDuplicate,createdBy[uid,username,firstName,surname],updatedBy[uid,username,firstName,surname]," +
	"attributes[attribute,value,createdAt,updatedAt],enrollments[enrollment,createdAt,updatedAt,createdAtClient," +
	"updatedAtClient,orgUnit,program,enrolledAt,occurredAt,completedAt,followUp,status,trackedEntity,geometry," +
	"attributeOptionCombo,deleted,createdBy[uid,username,firstName,surname],updatedBy[uid,username,firstName,surname]," +
	"attributes[attribute,value,createdAt,updatedAt],events[event,enrollment,createdAt,updatedAt,createdAtClient," +
	"updatedAtClient,status,geometry,program,programStage,orgUnit,trackedEntity,occurredAt,completedAt,scheduledAt," +
	"attributeOptionCombo,assignedUser,completedBy,followUp,deleted,createdBy[uid,username,firstName,surname]," +
	"updatedBy[uid,username,firstName,surname],dataValues[dataElement,createdBy,value,createdAt,updatedAt,providedElsewhere]]]"
