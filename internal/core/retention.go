package core

import (
	"github.com/dhis2/facility-hub/internal/access"
)

const retentionKey = "retention"

// Retention is the retention table read from central (facility-hub-config/retention)
// at the last catalog refresh; none keeps everything.
func (h *Hub) Retention() access.Retention {
	var r access.Retention
	h.configState(retentionKey, &r)
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
