package store

import (
	"context"
	"database/sql"
	"os"
)

// The USB stick's copy of the store (docs/RECOVERY.md, C2; internal/stick): sealed
// outbox entries as they are, so the stick holds nothing the disk does not.

// RawOutbox is an outbox entry as stored, its payload still sealed with the data key
// (bound to its seq), with the objects it names.
type RawOutbox struct {
	Seq          int64       `json:"seq"`
	UserID       int64       `json:"userId"`
	PayloadEnc   []byte      `json:"payloadEnc"`
	CreatedAt    string      `json:"createdAt"`
	State        string      `json:"state"`
	DependsOnSeq int64       `json:"dependsOnSeq,omitempty"`
	Params       string      `json:"params,omitempty"`
	Kind         string      `json:"kind"`
	Objects      []OutboxRef `json:"objects"`
}

// PendingRaw returns the pending outbox entries as stored, in seq order.
func (s *Store) PendingRaw() ([]*RawOutbox, error) {
	rows, err := s.DB().Query(`SELECT seq, user_id, payload_enc, created_at, state, coalesce(depends_on_seq, 0), params, kind
 FROM outbox WHERE state = ? ORDER BY seq`, OutboxPending)
	var out []*RawOutbox
	err = scanRows(rows, err, func(r *sql.Rows) error {
		e := &RawOutbox{}
		err := r.Scan(&e.Seq, &e.UserID, &e.PayloadEnc, &e.CreatedAt, &e.State, &e.DependsOnSeq, &e.Params, &e.Kind)
		out = append(out, e)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, e := range out {
		rows, err := s.DB().Query(`SELECT uid, kind FROM outbox_objects WHERE seq = ?`, e.Seq)
		if err := scanRows(rows, err, func(r *sql.Rows) error {
			var o OutboxRef
			err := r.Scan(&o.UID, &o.Kind)
			e.Objects = append(e.Objects, o)
			return err
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RestoreOutbox puts back entries copied to the stick, as pending, unless the store
// already holds their seq (the snapshot had them).
func (s *Store) RestoreOutbox(entries []*RawOutbox) (int, error) {
	n := 0
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, e := range entries {
			var dep any
			if e.DependsOnSeq > 0 {
				dep = e.DependsOnSeq
			}
			res, err := tx.Exec(`INSERT OR IGNORE INTO outbox(seq, user_id, payload_enc, created_at, state, depends_on_seq, params, kind)
 VALUES(?,?,?,?,?,?,?,?)`, e.Seq, e.UserID, e.PayloadEnc, e.CreatedAt, OutboxPending, dep, e.Params, e.Kind)
			if err != nil {
				return err
			}
			if k, _ := res.RowsAffected(); k == 0 {
				continue
			}
			n++
			for _, o := range e.Objects {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO outbox_objects(seq, uid, kind) VALUES(?,?,?)`, e.Seq, o.UID, o.Kind); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return n, err
}

// CopyKeysTo writes a consistent copy of the key file (key slots, identity, settings)
// to dst.
func (s *Store) CopyKeysTo(dst string) error {
	os.Remove(dst)
	_, err := s.Keys.Exec(`VACUUM INTO ?`, dst)
	return err
}

// KeysChanged reports the key file's (and its write-ahead log's) last change, to copy
// it only when something changed.
func (s *Store) KeysChanged() int64 {
	var t int64
	for _, p := range []string{s.Path, s.Path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			t = max(t, fi.ModTime().UnixNano()+fi.Size())
		}
	}
	return t
}
