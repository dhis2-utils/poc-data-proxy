package core

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// NotePayload is an outbox entry of kind store.OutboxNote: Capture's request, to make
// again as its author at the push (sync/notes.go).
type NotePayload struct {
	Collection string    `json:"collection"` // "events" or "enrollments"
	UID        string    `json:"uid"`
	Note       tracker.M `json:"note"` // {"note", "value"}
}

// PersistNote adds note to the stored record o, so it shows offline at once, and
// queues it for central. The record's own fields and updatedAt stay as they are:
// central's note request changes nothing else either (checked on 2.42.7.1 and
// 2.43.2.1, 17 November 2026), and the overwrite count reads updatedAt as central's.
// The entry waits for any pending save of the record (a record made offline).
func (h *Hub) PersistNote(userID int64, o *tracker.Obj, collection string, note tracker.M) (int64, error) {
	full := h.Index.Full(o)
	data := tracker.Merge(full, tracker.M{"notes": []any{note}})
	upd := o.WithData(data)
	payload, err := json.Marshal(NotePayload{Collection: collection, UID: o.UID, Note: note})
	if err != nil {
		return 0, err
	}
	var seq int64
	err = h.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		if err := h.Store.PutObject(tx, upd.ToStore()); err != nil {
			return err
		}
		dep := h.Store.LatestPendingSeqFor(tx, []string{o.UID})
		if seq, err = h.Store.AppendOutbox(tx, userID, payload, "", []store.OutboxRef{{UID: o.UID, Kind: o.Kind}}, dep); err != nil {
			return err
		}
		return h.Store.SetOutboxKind(tx, seq, store.OutboxNote)
	})
	if err != nil {
		return 0, err
	}
	h.Index.Put(upd)
	return seq, nil
}
