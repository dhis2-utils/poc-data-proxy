package aggregate

import (
	"testing"
	"time"
)

func TestPeriods(t *testing.T) {
	at := time.Date(2026, 2, 15, 13, 0, 0, 0, time.FixedZone("EAT", 3*3600))
	cases := []struct{ typ, id, start, end string }{
		{"Daily", "20260215", "2026-02-15", "2026-02-15"},
		{"Weekly", "2026W7", "2026-02-09", "2026-02-15"},
		{"Monthly", "202602", "2026-02-01", "2026-02-28"},
		{"BiMonthly", "202601B", "2026-01-01", "2026-02-28"},
		{"Quarterly", "2026Q1", "2026-01-01", "2026-03-31"},
		{"SixMonthly", "2026S1", "2026-01-01", "2026-06-30"},
		{"SixMonthlyApril", "2025AprilS2", "2025-10-01", "2026-03-31"},
		{"Yearly", "2026", "2026-01-01", "2026-12-31"},
		{"FinancialApril", "2025April", "2025-04-01", "2026-03-31"},
		{"FinancialJuly", "2025July", "2025-07-01", "2026-06-30"},
		{"FinancialOct", "2025Oct", "2025-10-01", "2026-09-30"},
	}
	for _, c := range cases {
		p, ok := Current(c.typ, at)
		if !ok || p.ID != c.id || p.Start.Format(time.DateOnly) != c.start || p.End.Format(time.DateOnly) != c.end {
			t.Errorf("%s: %+v %v, want %s %s..%s", c.typ, p, ok, c.id, c.start, c.end)
		}
	}
	if _, ok := Current("WeeklyWednesday", at); ok {
		t.Error("WeeklyWednesday is not supported, Current must say so")
	}
	m, _ := Current("Monthly", at)
	if got := Shift("Monthly", m, -2).ID; got != "202512" {
		t.Errorf("two months before 202602: %s", got)
	}
	if got := Shift("Monthly", m, 11).ID; got != "202701" {
		t.Errorf("eleven months after 202602: %s", got)
	}
	w, _ := Current("Weekly", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if w.ID != "2026W1" || Shift("Weekly", w, -1).ID != "2025W52" {
		t.Errorf("ISO weeks around new year: %s, %s", w.ID, Shift("Weekly", w, -1).ID)
	}
}
