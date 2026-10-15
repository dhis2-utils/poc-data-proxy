package sync

import (
	"context"

	"github.com/dhis2/facility-hub/internal/compat"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// pullEvents pulls an event program for users with the same access (g), listing as the
// first of them: whole events, a page at a time (events are small: no ID listing
// first, as tracker needs). An event central lists as deleted is hidden from the
// group at once, not at the weekly full pull. With retention set for the program,
// only events since its cutoff are listed. It returns the newest updatedAt listed.
func (w *Worker) pullEvents(ctx context.Context, api compat.API, g []*puller, facility, program, after string,
	seen map[uint32]bool) (string, int, error) {
	h := w.H
	rep := g[0]
	cutoff, _ := h.Retention().Cutoff(program, h.CentralNow())
	maxUpdated, total := "", 0
	for page := 1; ; page++ {
		var res struct {
			Events []tracker.M `json:"events"`
		}
		if err := h.Central.JSON(ctx, "GET", api.EventsPath(facility, program, after, cutoff, page, listPageSize), nil,
			rep.auth, &res); err != nil {
			return "", total, err
		}
		var keep []any
		var gone []string
		for _, ev := range res.Events {
			maxUpdated = max(maxUpdated, tracker.Str(ev, "updatedAt"))
			if ev["deleted"] == true {
				gone = append(gone, tracker.Str(ev, "event"))
				continue
			}
			keep = append(keep, ev)
		}
		if len(keep) > 0 {
			uids, err := h.UpsertCentral(rep.u.ID, tracker.M{"events": keep}, true)
			if err != nil {
				return "", total, err
			}
			st, err := h.Store.ObjectStates(uids)
			if err != nil {
				return "", total, err
			}
			for _, o := range st {
				seen[uint32(o.Slot)] = true
			}
			total += len(uids)
		}
		if len(gone) > 0 {
			st, err := h.Store.ObjectStates(gone)
			if err != nil {
				return "", total, err
			}
			var slots []uint32
			for _, o := range st {
				slots = append(slots, uint32(o.Slot))
			}
			for _, m := range g {
				if err := h.HideSlots(m.u.ID, slots); err != nil {
					return "", total, err
				}
			}
		}
		if len(res.Events) < listPageSize {
			break
		}
	}
	return maxUpdated, total, nil
}
