package tracker

import (
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/aggregate"
)

// eventExpired reports whether an event in program pm is past the program's expiry at
// now (in central's time zone): the expiry period (expiryPeriodType) holding
// occurredAt ended more than expiryDays ago. A completed event instead expires
// completeEventsExpiryDays after its completion, when the program sets that.
func eventExpired(pm *access.ProgramMeta, occurredAt, completedAt string, now time.Time) bool {
	if pm == nil {
		return false
	}
	now = aggregate.Naive(now)
	if completedAt != "" && pm.CompleteEventsExpiryDays > 0 {
		if t, ok := parseTime(completedAt); ok && now.After(t.Add(time.Duration(pm.CompleteEventsExpiryDays)*24*time.Hour)) {
			return true
		}
	}
	if pm.ExpiryPeriodType == "" || pm.ExpiryDays <= 0 {
		return false
	}
	t, ok := parseTime(occurredAt)
	if !ok {
		return false
	}
	p, ok := aggregate.Current(pm.ExpiryPeriodType, aggregate.Naive(t))
	if !ok {
		return false // a period type the hub does not hold: central decides on push
	}
	return now.After(p.End.Add(time.Duration(pm.ExpiryDays) * 24 * time.Hour))
}
