package aggregate

import (
	"time"
)

// Lock states, as dataEntry/dataValues answers them.
const (
	Open     = "OPEN"
	Locked   = "LOCKED"
	Approved = "APPROVED"
)

// Window is the periods of the data set the hub holds at now (central's time): from
// the latest open period (the current one shifted by openFuturePeriods-1, as central
// counts: with none, last month is the latest) back, retention periods in all
// counting from the current one or the latest open, whichever is earlier; without
// the periods expiry days or data input periods close (central refuses writes
// there, so the hub holds nothing for them). Oldest first.
func Window(ds *DataSet, now time.Time, retention int) []Period {
	cur, ok := Current(ds.PeriodType, now)
	if !ok {
		return nil
	}
	if retention < 1 {
		retention = 1
	}
	latest := ds.OpenFuturePeriods - 1
	from := min(0, latest) - (retention - 1)
	var out []Period
	for n := from; n <= latest; n++ {
		p := Shift(ds.PeriodType, cur, n)
		if Expired(ds, p, now) || !InputOpen(ds, p, now) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// InWindow finds the period pe in the data set's window.
func InWindow(ds *DataSet, now time.Time, retention int, pe string) (Period, bool) {
	for _, p := range Window(ds, now, retention) {
		if p.ID == pe {
			return p, true
		}
	}
	return Period{}, false
}

// LocksAt is when expiry days lock the period: its last day plus that many days, as
// central counts (ok false without expiry days).
func LocksAt(ds *DataSet, p Period) (time.Time, bool) {
	if ds.ExpiryDays <= 0 {
		return time.Time{}, false
	}
	return p.End.Add(time.Duration(ds.ExpiryDays * float64(24*time.Hour))), true
}

// Expired: expiry days have locked the period at now.
func Expired(ds *DataSet, p Period, now time.Time) bool {
	at, ok := LocksAt(ds, p)
	return ok && Naive(now).After(at)
}

// InputOpen: data input periods, if the data set has any, allow writes to p at now.
// Central 2.42 judges them by the day, not the moment (F5, 2.42.7.1, checked again on
// 17 November 2026): it takes values all of the closing day and from the start of the
// opening day, so the dates compare. Central 2.43 (2.43.2.1, the same day) compares
// the moment: a value ten seconds after a same-day closing time is refused (E8030),
// and one before a same-day opening time too. ds.InputByMoment says which.
func InputOpen(ds *DataSet, p Period, now time.Time) bool {
	if len(ds.InputPeriods) == 0 {
		return true
	}
	n, at := Naive(now), day
	if ds.InputByMoment {
		at = func(t time.Time) time.Time { return t }
	}
	n = at(n)
	for _, ip := range ds.InputPeriods {
		if ip.Period != p.ID {
			continue
		}
		if t, err := parseDHIS2Time(ip.Opening); err == nil && n.Before(at(t)) {
			return false
		}
		if t, err := parseDHIS2Time(ip.Closing); err == nil && n.After(at(t)) {
			return false
		}
		return true
	}
	return false
}

// LockStatus is a form's lock for a user at now. central is the lock status central
// answered when the hub fetched the form ("" if it never did): approval and central's
// own locks stand; expiry days and data input periods are applied at now, because a
// period can expire after the hub fetched it.
func LockStatus(ds *DataSet, p Period, now time.Time, bypass bool, central string) string {
	switch {
	case central == Approved:
		return Approved
	case central == Locked:
		return Locked
	case !InputOpen(ds, p, now):
		return Locked
	case Expired(ds, p, now) && !bypass:
		return Locked
	}
	return Open
}

// Naive is t's wall clock in its own time zone, as UTC: how central's offset-free
// times and periods compare.
func Naive(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// parseDHIS2Time reads central's timestamps ("2026-10-05T00:00:00.000", with or
// without an offset) as wall-clock times.
func parseDHIS2Time(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04:05.000", "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05", time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return Naive(t), nil
		}
	}
	return time.Parse(time.RFC3339, s)
}
