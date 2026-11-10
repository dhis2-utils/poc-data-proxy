package core

import (
	"github.com/dhis2/facility-hub/internal/access"
)

const retentionKey = "retention"

// maxMonths is the most months of retention the Facility Hub app lets a country set.
const maxMonths = 1200

// Retention is the retention table read from central (facility-hub-config/retention)
// at the last catalog refresh; none keeps everything. Months are kept within the app's
// limits (0–1200), whatever the entry holds (review R-07).
func (h *Hub) Retention() access.Retention {
	var r access.Retention
	h.configState(retentionKey, &r)
	clamp := func(m int) int { return min(max(m, 0), maxMonths) }
	r.DefaultMonths = clamp(r.DefaultMonths)
	for _, t := range []map[string]int{r.Programs, r.TrackedEntityTypes} {
		for k, m := range t {
			t[k] = clamp(m)
		}
	}
	return r
}

const localReadsKey = "localReads"

// LocalReads is the list of programs answered from the hub while online, read from
// central (facility-hub-config/localReads) at the last catalog refresh.
func (h *Hub) LocalReads() access.LocalReads {
	var l access.LocalReads
	h.configState(localReadsKey, &l)
	return l
}
