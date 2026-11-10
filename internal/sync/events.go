package sync

import (
	"context"
	"errors"

	"github.com/dhis2/facility-hub/internal/compat"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// pullEvents pulls an event program for users with the same access (g), listing as the
// first of them: whole events, a page at a time (events are small: no ID listing
// first, as tracker needs). An event central lists as deleted is stored as a
// tombstone, at once, not at the weekly full pull. With retention set for the program,
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
		if res.Events == nil {
			// {} is not an empty listing (review R-34); central answers "events": [].
			return "", total, errors.New("central's answer has no events")
		}
		keep := make([]any, len(res.Events))
		for i, ev := range res.Events {
			maxUpdated = max(maxUpdated, tracker.Str(ev, "updatedAt"))
			keep[i] = ev
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
		if len(res.Events) < listPageSize {
			break
		}
	}
	return maxUpdated, total, nil
}
