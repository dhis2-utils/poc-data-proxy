package aggregate

import (
	"slices"
	"testing"
	"time"
)

// Offline, the hub's clock decides which periods of a data set are open and when a
// period expires (docs/DEPLOYMENT.md, appendix A). A monthly data set with 10 expiry days and no open
// future periods: on 4 November central takes October; September locked on 10
// October.
func TestNoRTCAggregateWindow(t *testing.T) {
	ds := &DataSet{ID: "DsMonthly01", PeriodType: "Monthly", ExpiryDays: 10}
	ids := func(now time.Time) []string {
		var out []string
		for _, p := range Window(ds, now, 3) {
			out = append(out, p.ID)
		}
		return out
	}
	central := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	if got := ids(central); !slices.Equal(got, []string{"202610"}) {
		t.Fatalf("right clock: %v, want [202610]", got)
	}
	// The Pi 4 image's epoch: the clock a Pi without an RTC starts at with no network.
	epoch := time.Date(2026, 10, 6, 0, 27, 36, 0, time.UTC)
	if got := ids(epoch); !slices.Equal(got, []string{"202609"}) {
		t.Errorf("at the image's epoch: %v, want [202609] (October missing, September open)", got)
	}
	oct, _ := Current("Monthly", central.AddDate(0, -1, 0))
	if s := LockStatus(ds, oct, epoch, false, ""); s != Open {
		t.Errorf("October at the epoch: %s", s)
	}
	// A clock 7 days ahead on 4 November locks October, which central still takes.
	if s, c := LockStatus(ds, oct, central.AddDate(0, 0, 7), false, ""), LockStatus(ds, oct, central, false, ""); s != Locked || c != Open {
		t.Errorf("October with the clock 7 days ahead: %s (central: %s), want LOCKED", s, c)
	}
	// Hours matter only at the boundaries: 3 hours behind just after midnight on
	// 1 November, the hub still counts October as the current month and holds no open
	// form (September locked on 10 October).
	midnight := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	if got := ids(midnight.Add(-3 * time.Hour)); len(got) != 0 {
		t.Errorf("3 hours behind at 01:00 on 1 November: %v, want none", got)
	}
}
