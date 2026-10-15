package access

import "time"

// Retention is how long the hub keeps each program's data, in months, from central's
// dataStore (facility-hub-config/retention). 0 keeps everything; a program without an
// entry uses the default. Event programs use it now (by occurredAt), tracker programs
// will (docs/superpowers/specs/2026-10-15-event-programs-design.md).
type Retention struct {
	DefaultMonths int            `json:"defaultMonths"`
	Programs      map[string]int `json:"programs"`
}

func (r Retention) Months(program string) int {
	if n, ok := r.Programs[program]; ok {
		return n
	}
	return r.DefaultMonths
}

// Cutoff is the oldest date kept for the program at now ("2006-01-02"); ok is false
// when the program keeps everything.
func (r Retention) Cutoff(program string, now time.Time) (string, bool) {
	m := r.Months(program)
	if m <= 0 {
		return "", false
	}
	return now.AddDate(0, -m, 0).Format(time.DateOnly), true
}
