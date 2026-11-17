package access

import "time"

// Retention is how long the hub keeps each program's data, in months, from central's
// dataStore (facility-hub-config/retention). 0 keeps everything; a program without an
// entry uses the default. Event and tracker programs use it (by occurredAt for events
// of event programs, by an enrollment's last change for tracker programs); tracked
// entity types have windows of their own, with no default
// (docs/design/2026-10-16-tracker-retention-design.md).
type Retention struct {
	DefaultMonths      int            `json:"defaultMonths"`
	Programs           map[string]int `json:"programs"`
	TrackedEntityTypes map[string]int `json:"trackedEntityTypes,omitempty"`
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

// TypeCutoff is the oldest date kept for tracked entities of this type; ok is false
// when the type keeps everything (no entry or 0: there is no default for types, so
// persons stay unless a country names their type).
func (r Retention) TypeCutoff(tet string, now time.Time) (string, bool) {
	m := r.TrackedEntityTypes[tet]
	if m <= 0 {
		return "", false
	}
	return now.AddDate(0, -m, 0).Format(time.DateOnly), true
}
