package tracker

import "time"

// NewestClientTime is the latest createdAtClient or updatedAtClient in a payload, as
// the wall-clock time the client wrote. Android Capture stamps each record with the
// device's clock in the device's time zone; Capture web sends neither field, and ok
// is then false.
func NewestClientTime(payload M) (newest time.Time, ok bool) {
	var walk func(m M)
	walk = func(m M) {
		for _, k := range []string{"createdAtClient", "updatedAtClient"} {
			if t, good := parseTime(Str(m, k)); good {
				if t = naive(t); !ok || t.After(newest) {
					newest, ok = t, true
				}
			}
		}
		for _, k := range childKeys {
			for _, c := range list(m, k) {
				walk(c)
			}
		}
	}
	for _, k := range []string{"trackedEntities", "enrollments", "events", "relationships"} {
		for _, o := range list(payload, k) {
			walk(o)
		}
	}
	return newest, ok
}

// naive is t's wall-clock time, read as UTC.
func naive(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}
