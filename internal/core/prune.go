package core

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// PruneEvents removes synced events of event programs whose date fell out of their
// program's retention; events with edits waiting in the outbox are kept.
func (h *Hub) PruneEvents() (int, error) {
	cat, ret, now := h.Catalog(), h.Retention(), h.CentralNow()
	n := 0
	for _, id := range cat.ProgramIDs() {
		cutoff, ok := ret.Cutoff(id, now)
		if pm := cat.Programs[id]; !pm.Event || !ok {
			continue
		}
		uids, err := h.Store.OldProgramEvents(id, h.Facility(), cutoff)
		if err != nil {
			return n, err
		}
		if len(uids) == 0 {
			continue
		}
		d, err := h.Store.DeleteObjects(uids)
		n += d
		if err != nil {
			return n, err
		}
	}
	if n > 0 {
		h.Index.Forget()
	}
	return n, nil
}

// PruneTracker applies retention to tracker programs and tracked entity types: closed
// enrollments whose last change fell out of their program's window become stubs (their
// events go); then tracked entities of types with a window that have nothing left in
// full go, with their stubs and relationships. Nothing pending is touched.
func (h *Hub) PruneTracker() (stubbed, deleted int, err error) {
	cat, ret, now, facility := h.Catalog(), h.Retention(), h.CentralNow(), h.Facility()
	if cat == nil {
		return 0, 0, nil
	}
	defer func() {
		if stubbed+deleted > 0 {
			h.Index.Forget()
		}
	}()
	for _, id := range cat.ProgramIDs() {
		cutoff, ok := ret.Cutoff(id, now)
		if pm := cat.Programs[id]; pm.Event || !ok {
			continue
		}
		uids, err := h.Store.AgedEnrollments(id, facility, cutoff)
		if err != nil {
			return stubbed, deleted, err
		}
		n, err := h.Store.StubEnrollments(uids, stubPayload)
		stubbed += n
		if err != nil {
			return stubbed, deleted, err
		}
	}
	events, err := h.Store.StubEnrollmentsEvents()
	if err != nil {
		return stubbed, deleted, err
	}
	n, err := h.Store.DeleteObjects(events)
	deleted += n
	if err != nil {
		return stubbed, deleted, err
	}
	for _, tet := range slices.Sorted(maps.Keys(ret.TrackedEntityTypes)) {
		cutoff, ok := ret.TypeCutoff(tet, now)
		if !ok {
			continue
		}
		tes, err := h.Store.PrunableEntities(tet, facility, cutoff)
		if err != nil || len(tes) == 0 {
			if err != nil {
				return stubbed, deleted, err
			}
			continue
		}
		left, err := h.Store.EntityLeftovers(tes)
		if err != nil {
			return stubbed, deleted, err
		}
		for _, uids := range [][]string{left, tes} {
			n, err := h.Store.DeleteObjects(uids)
			deleted += n
			if err != nil {
				return stubbed, deleted, err
			}
		}
	}
	return stubbed, deleted, nil
}

// stubPayload is an enrollment's stub, as payload and slim JSON (a stub is all
// scalars, so it is its own slim copy).
func stubPayload(payload []byte) ([]byte, []byte, error) {
	var e tracker.M
	if err := json.Unmarshal(payload, &e); err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(tracker.StubOf(e))
	return b, b, err
}
