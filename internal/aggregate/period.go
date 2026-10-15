// Package aggregate holds what the hub needs to answer the Aggregate Data Entry app
// offline: periods, windows and locks, the access central gives each user
// (dataEntry/metadata), and edits as they are queued and pushed. It knows nothing of
// the store or of HTTP (docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md).
package aggregate

import (
	"fmt"
	"time"
)

// Period is one period of a period type, in the ISO calendar. Start and End are
// dates (00:00 UTC); End is the period's last day.
type Period struct {
	ID         string
	Start, End time.Time
}

type periodType struct {
	start func(d time.Time) time.Time // first day of the period holding the date d
	next  func(s time.Time) time.Time // first day of the next period
	id    func(s time.Time) string
}

// months: periods of n months, the year's first starting in month first (1 = January).
func months(n, first int, id func(s time.Time) string) periodType {
	return periodType{
		start: func(d time.Time) time.Time {
			since := (int(d.Month()) - first + 12) % 12
			return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -(since % n), 0)
		},
		next: func(s time.Time) time.Time { return s.AddDate(0, n, 0) },
		id:   id,
	}
}

var periodTypes = map[string]periodType{
	"Daily": {
		start: func(d time.Time) time.Time { return d },
		next:  func(s time.Time) time.Time { return s.AddDate(0, 0, 1) },
		id:    func(s time.Time) string { return s.Format("20060102") },
	},
	"Weekly": {
		start: func(d time.Time) time.Time { return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7)) },
		next:  func(s time.Time) time.Time { return s.AddDate(0, 0, 7) },
		id: func(s time.Time) string {
			y, w := s.ISOWeek()
			return fmt.Sprintf("%dW%d", y, w)
		},
	},
	"Monthly":    months(1, 1, func(s time.Time) string { return s.Format("200601") }),
	"BiMonthly":  months(2, 1, func(s time.Time) string { return fmt.Sprintf("%d%02dB", s.Year(), (int(s.Month())-1)/2+1) }),
	"Quarterly":  months(3, 1, func(s time.Time) string { return fmt.Sprintf("%dQ%d", s.Year(), (int(s.Month())-1)/3+1) }),
	"SixMonthly": months(6, 1, func(s time.Time) string { return fmt.Sprintf("%dS%d", s.Year(), (int(s.Month())-1)/6+1) }),
	"SixMonthlyApril": months(6, 4, func(s time.Time) string {
		if s.Month() == time.April {
			return fmt.Sprintf("%dAprilS1", s.Year())
		}
		return fmt.Sprintf("%dAprilS2", s.Year())
	}),
	"Yearly":         months(12, 1, func(s time.Time) string { return fmt.Sprint(s.Year()) }),
	"FinancialApril": months(12, 4, func(s time.Time) string { return fmt.Sprintf("%dApril", s.Year()) }),
	"FinancialJuly":  months(12, 7, func(s time.Time) string { return fmt.Sprintf("%dJuly", s.Year()) }),
	"FinancialOct":   months(12, 10, func(s time.Time) string { return fmt.Sprintf("%dOct", s.Year()) }),
}

// day is the calendar date of t, in t's own time zone, as 00:00 UTC.
func day(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

func period(pt periodType, s time.Time) Period {
	return Period{ID: pt.id(s), Start: s, End: pt.next(s).AddDate(0, 0, -1)}
}

// Current is the period of this type holding t (a time in central's time zone).
// ok is false for period types the hub does not hold.
func Current(typ string, t time.Time) (Period, bool) {
	pt, ok := periodTypes[typ]
	if !ok {
		return Period{}, false
	}
	return period(pt, pt.start(day(t))), true
}

// Shift is the period n periods after p (before it for negative n).
func Shift(typ string, p Period, n int) Period {
	pt := periodTypes[typ]
	s := p.Start
	for ; n > 0; n-- {
		s = pt.next(s)
	}
	for ; n < 0; n++ {
		s = pt.start(s.AddDate(0, 0, -1))
	}
	return period(pt, s)
}
