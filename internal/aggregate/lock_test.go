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
	if got := ids; len(got) != 4 || got[0] != "202608" || got[3] != "202611" {
		t.Fatalf("window, 3 periods kept and one future: %v", got)
	}
	ds.ExpiryDays = 10 // September locks on 2026-10-10
	ids = nil
	for _, p := range Window(ds, now, 3) {
		ids = append(ids, p.ID)
	}
	if len(ids) != 2 || ids[0] != "202610" {
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
	aug, _ := Current("Monthly", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if InputOpen(ds, aug, before) {
		t.Error("a period without a data input period is closed when the data set has any")
	}
}
