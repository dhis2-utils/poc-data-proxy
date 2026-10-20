package core

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregate"
)

// AggWarning: edits of a form wait while its period locks (expiry days) soon.
type AggWarning struct {
	Label   string
	Pending int
	LocksAt time.Time
}

// AggWarnings lists forms with pending edits whose period locks within the duration.
func (h *Hub) AggWarnings(within time.Duration) []AggWarning {
	es, err := h.Store.PendingAggregateAll()
	if err != nil {
		return nil
	}
	now := h.CentralNow()
	byForm := map[string]*AggWarning{}
	for _, e := range es {
		var ed aggregate.Edit
		if json.Unmarshal(e.Payload, &ed) != nil {
			continue
		}
		acc := h.AggAccess(e.UserID)
		if acc == nil || acc.DataSets[ed.Form.DataSet] == nil {
			continue
		}
		ds := acc.DataSets[ed.Form.DataSet]
		per, ok := aggregate.InWindow(ds, now, h.AggRetention().For(ds.ID), ed.Form.Period)
		if !ok {
			continue
		}
		at, ok := aggregate.LocksAt(ds, per)
		if !ok || at.Sub(aggregate.Naive(now)) > within {
			continue
		}
		w := byForm[ed.Form.String()]
		if w == nil {
			w = &AggWarning{Label: ds.Name + ", " + per.ID, LocksAt: at}
			byForm[ed.Form.String()] = w
		}
		w.Pending++
	}
	out := make([]AggWarning, 0, len(byForm))
	for _, w := range byForm {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LocksAt.Before(out[j].LocksAt) })
	return out
}
