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
