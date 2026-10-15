package core

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
		uids, err := h.Store.OldProgramEvents(id, cutoff)
		if err != nil {
			return n, err
		}
		var drop []string
		for _, u := range uids {
			if !h.Store.HasPendingOutboxFor(u, 0) {
				drop = append(drop, u)
			}
		}
		if len(drop) == 0 {
			continue
		}
		if err := h.Store.DeleteObjects(drop); err != nil {
			return n, err
		}
		n += len(drop)
	}
	if n > 0 {
		h.Index.Forget()
	}
	return n, nil
}
