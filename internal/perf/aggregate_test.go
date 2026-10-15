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

// TestAggregate measures the aggregate side at a large facility: 12 months of
// values (48 000, every form holding all of its month's values: far larger forms
// than real ones), the first pull and an incremental one, a month's form answered
// offline, and an offline edit.
func TestAggregate(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	const periods, cells = 12, 4000
	e.Fake.GenerateAggregate(periods, cells)
	timed := func(f func()) time.Duration {
		start := time.Now()
		f()
		return time.Since(start).Round(time.Millisecond)
	}
	first := timed(e.Setup) // set-up's sync is the first pull (as the admin, who enters aggregate data too)
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	h := e.App.Hub
	again := timed(e.Sync)
	forms, _ := h.Store.AggForms()
	e.Offline()
	var formTimes []time.Duration
	for p := 0; p < periods; p++ {
		pe := h.CentralNow().AddDate(0, -p, 0).Format("200601")
		path := fmt.Sprintf("/api/%s/dataEntry/dataValues?ds=%s&pe=%s&ou=%s", h.APIMinor(), fc.AggDataSet, pe, fc.Facility)
		formTimes = append(formTimes, timed(func() {
			if st, b := nurse.Do("GET", path, nil); st != 200 {
				t.Fatalf("form %s: %d %.200s", pe, st, b)
			}
		}))
	}
	edit := timed(func() {
		st, b := nurse.PostDataValue("/api/"+h.APIMinor()+"/dataValues", map[string]string{"ds": fc.AggDataSet,
			"pe": h.CentralNow().Format("200601"), "ou": fc.Facility, "de": fc.AggDeNum, "co": "FakeCoc0001", "value": "5"})
		if st != 201 {
			t.Fatalf("offline edit: %d %s", st, b)
		}
	})
	var ft []string
	for _, d := range formTimes {
		ft = append(ft, d.String())
	}
	out := strings.Join([]string{
		fmt.Sprintf("values: %d (%d months x %d cells); forms held: %d (two data sets x 12 months; the donor data set has no values)", periods*cells, periods, cells, len(forms)),
		fmt.Sprintf("set-up with the first pull: %s; next cycle (incremental): %s", first, again),
		fmt.Sprintf("offline form, %d values each: %s", cells, strings.Join(ft, " ")),
		fmt.Sprintf("offline edit: %s; heap %.0f MB; data dir %.1f MB", edit, heapMB(), dirMB(e.Dir)),
	}, "\n")
	t.Log("\n" + out)
	if p := os.Getenv("HUB_PERF_OUT"); p != "" {
		os.WriteFile(p, []byte(out+"\n"), 0o644)
	}
}
