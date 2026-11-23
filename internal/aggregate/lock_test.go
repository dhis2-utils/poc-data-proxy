package aggregate

import (
	"testing"
	"time"
)

func TestWindow(t *testing.T) {
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	ds := &DataSet{ID: "A", PeriodType: "Monthly", OpenFuturePeriods: 1}
	var ids []string
	for _, p := range Window(ds, now, 3) {
		ids = append(ids, p.ID)
	}
	// One open future period: the current one is the latest open (central:
	// "is after latest open future period"); none: last month is.
	if got := ids; len(got) != 3 || got[0] != "202608" || got[2] != "202610" {
		t.Fatalf("window, 3 periods kept, one open future period: %v", got)
	}
	ds.OpenFuturePeriods = 0
	ids = nil
	for _, p := range Window(ds, now, 3) {
		ids = append(ids, p.ID)
	}
	if len(ids) != 3 || ids[0] != "202607" || ids[2] != "202609" {
		t.Fatalf("no open future periods: the current month is not open: %v", ids)
	}
	ds.OpenFuturePeriods = 10
	if w := Window(ds, now, 3); w[len(w)-1].ID != "202707" {
		t.Fatalf("ten open future periods end at 202707 (central's answer for 2026-10): %s", w[len(w)-1].ID)
	}
	ds.OpenFuturePeriods = 1
	ds.ExpiryDays = 10 // September locks on 2026-10-10
	ids = nil
	for _, p := range Window(ds, now, 3) {
		ids = append(ids, p.ID)
	}
	if len(ids) != 1 || ids[0] != "202610" {
		t.Fatalf("expired periods leave the window: %v", ids)
	}
	if _, ok := InWindow(ds, now, 3, "202609"); ok {
		t.Fatal("202609 has expired")
	}
	if _, ok := InWindow(&DataSet{PeriodType: "WeeklyWednesday"}, now, 3, "2026WedW42"); ok {
		t.Fatal("unsupported period type: nothing held")
	}
}

func TestLockStatus(t *testing.T) {
	ds := &DataSet{ID: "A", PeriodType: "Monthly", ExpiryDays: 10}
	sep, _ := Current("Monthly", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	before := time.Date(2026, 10, 9, 23, 0, 0, 0, time.UTC)
	after := time.Date(2026, 10, 10, 0, 1, 0, 0, time.UTC)
	if at, ok := LocksAt(ds, sep); !ok || !at.Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("LocksAt: %v %v", at, ok)
	}
	for _, c := range []struct {
		now     time.Time
		bypass  bool
		central string
		want    string
	}{
		{before, false, "", Open},
		{before, false, Open, Open},
		{after, false, Open, Locked}, // expired since central answered
		{after, true, Open, Open},    // F_EDIT_EXPIRED
		{before, false, Approved, Approved},
		{before, true, Locked, Locked}, // central's lock stands (closed org unit, ...)
	} {
		if got := LockStatus(ds, sep, c.now, c.bypass, c.central); got != c.want {
			t.Errorf("%v bypass=%v central=%q: %s, want %s", c.now, c.bypass, c.central, got, c.want)
		}
	}
	ds.InputPeriods = []InputPeriod{{Period: "202609", Opening: "2026-10-01T00:00:00.000", Closing: "2026-10-05T00:00:00.000"}}
	ds.ExpiryDays = 0
	if LockStatus(ds, sep, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), false, "") != Open ||
		LockStatus(ds, sep, time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), false, "") != Locked {
		t.Error("data input period")
	}
	// F5: central judges input periods by the day: values are taken all of the closing
	// day and from the start of the opening day, whatever the moment.
	for _, c := range []struct {
		now  time.Time
		want bool
	}{
		{time.Date(2026, 9, 30, 23, 59, 0, 0, time.UTC), false}, // the day before it opens
		{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), true},    // the opening day, from its start
		{time.Date(2026, 10, 5, 23, 59, 59, 0, time.UTC), true}, // the closing day, all of it
		{time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), false},   // the day after it closes
	} {
		if got := InputOpen(ds, sep, c.now); got != c.want {
			t.Errorf("InputOpen at %v: %v, want %v", c.now, got, c.want)
		}
	}
	// 2.43 compares the moment.
	ds.InputPeriods = []InputPeriod{{Period: "202609", Opening: "2026-10-01T08:00:00.000", Closing: "2026-10-05T17:00:00.000"}}
	ds.InputByMoment = true
	for _, c := range []struct {
		now  time.Time
		want bool
	}{
		{time.Date(2026, 10, 1, 7, 59, 59, 0, time.UTC), false}, // the opening day, before its time
		{time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 5, 17, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 10, 5, 17, 0, 10, 0, time.UTC), false}, // ten seconds after closing
	} {
		if got := InputOpen(ds, sep, c.now); got != c.want {
			t.Errorf("InputOpen by the moment at %v: %v, want %v", c.now, got, c.want)
		}
	}
	ds.InputByMoment = false
	aug, _ := Current("Monthly", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if InputOpen(ds, aug, before) {
		t.Error("a period without a data input period is closed when the data set has any")
	}
}
