package store

import (
	"database/sql"
	"fmt"
	"strings"
)

// Outbox entry states.
const (
	OutboxPending  = "pending"
	OutboxSynced   = "synced"   // every object accepted by central
	OutboxPartial  = "partial"  // some objects rejected or blocked
	OutboxRejected = "rejected" // every object rejected or blocked
)

type OutboxEntry struct {
	Seq          int64
	UserID       int64
	Payload      []byte // flat tracker payload as pushed to central
	CreatedAt    string
	State        string
	Attempts     int
	LastError    string
	DependsOnSeq int64
	UIDs         []string
}

// OutboxRef is an object UID + kind in an outbox entry.
type OutboxRef struct{ UID, Kind string }

// AppendOutbox adds an entry and returns its seq. dependsOn is 0 for none.
func (s *Store) AppendOutbox(tx *sql.Tx, userID int64, payload []byte, refs []OutboxRef, dependsOn int64) (int64, error) {
	var dep any
	if dependsOn > 0 {
		dep = dependsOn
	}
	// Seal after insert so the AAD can include the seq.
	res, err := tx.Exec(`INSERT INTO outbox(user_id, payload_enc, created_at, state, depends_on_seq) VALUES(?,?,?,?,?)`,
		userID, []byte{}, Now(), OutboxPending, dep)
	if err != nil {
		return 0, err
	}
	seq, _ := res.LastInsertId()
	ct, err := s.Seal(fmt.Sprintf("outbox:%d", seq), payload)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE outbox SET payload_enc=? WHERE seq=?`, ct, seq); err != nil {
		return 0, err
	}
	for _, r := range refs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO outbox_objects(seq, uid, kind) VALUES(?,?,?)`, seq, r.UID, r.Kind); err != nil {
			return 0, err
		}
	}
	return seq, nil
}

// LatestPendingSeqFor returns the highest seq of a pending entry that contains any of uids.
func (s *Store) LatestPendingSeqFor(q interface {
	QueryRow(string, ...any) *sql.Row
}, uids []string) int64 {
	if len(uids) == 0 {
		return 0
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(uids)), ",")
	args := []any{OutboxPending}
	for _, u := range uids {
		args = append(args, u)
	}
	var seq sql.NullInt64
	q.QueryRow(`SELECT max(o.seq) FROM outbox o JOIN outbox_objects oo ON oo.seq=o.seq
 WHERE o.state=? AND oo.uid IN (`+ph+`)`, args...).Scan(&seq)
	return seq.Int64
}

// HasPendingOutboxFor reports whether uid is in a pending entry other than exceptSeq.
func (s *Store) HasPendingOutboxFor(uid string, exceptSeq int64) bool {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM outbox o JOIN outbox_objects oo ON oo.seq=o.seq
 WHERE o.state=? AND oo.uid=? AND o.seq<>?`, OutboxPending, uid, exceptSeq).Scan(&n)
	return n > 0
}

func (s *Store) scanOutbox(rows *sql.Rows, withPayload bool) ([]*OutboxEntry, error) {
	defer rows.Close()
	var out []*OutboxEntry
	for rows.Next() {
		e := &OutboxEntry{}
		var ct, errCT []byte
		var dep sql.NullInt64
		if err := rows.Scan(&e.Seq, &e.UserID, &ct, &e.CreatedAt, &e.State, &e.Attempts, &errCT, &dep); err != nil {
			return nil, err
		}
		e.DependsOnSeq = dep.Int64
		if withPayload {
			var err error
			if e.Payload, err = s.OpenBlob(fmt.Sprintf("outbox:%d", e.Seq), ct); err != nil {
				return nil, err
			}
		}
		if len(errCT) > 0 {
			if b, err := s.OpenBlob(fmt.Sprintf("outbox-err:%d", e.Seq), errCT); err == nil {
				e.LastError = string(b)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const outboxCols = `seq, user_id, payload_enc, created_at, state, attempts, last_error_enc, depends_on_seq`

// PendingOutbox returns pending entries in seq order, with payloads.
func (s *Store) PendingOutbox() ([]*OutboxEntry, error) {
	rows, err := s.DB().Query(`SELECT `+outboxCols+` FROM outbox WHERE state=? ORDER BY seq`, OutboxPending)
	if err != nil {
		return nil, err
	}
	return s.scanOutbox(rows, true)
}

// RecentOutbox returns the last n entries without payloads (admin UI).
func (s *Store) RecentOutbox(n int) ([]*OutboxEntry, error) {
	rows, err := s.DB().Query(`SELECT `+outboxCols+` FROM outbox ORDER BY seq DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	es, err := s.scanOutbox(rows, false)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		r, err := s.DB().Query(`SELECT uid FROM outbox_objects WHERE seq=? ORDER BY uid`, e.Seq)
		if err != nil {
			return nil, err
		}
		for r.Next() {
			var u string
			r.Scan(&u)
			e.UIDs = append(e.UIDs, u)
		}
		r.Close()
	}
	return es, nil
}

func (s *Store) OutboxState(seq int64) string {
	var st string
	s.DB().QueryRow(`SELECT state FROM outbox WHERE seq=?`, seq).Scan(&st)
	return st
}

func (s *Store) FinishOutbox(seq int64, state, lastError string) error {
	var ct []byte
	if lastError != "" {
		var err error
		if ct, err = s.Seal(fmt.Sprintf("outbox-err:%d", seq), []byte(lastError)); err != nil {
			return err
		}
	}
	_, err := s.DB().Exec(`UPDATE outbox SET state=?, attempts=attempts+1, last_error_enc=? WHERE seq=?`, state, ct, seq)
	return err
}

// NoteAttempt records a failed attempt that will be retried.
func (s *Store) NoteAttempt(seq int64, lastError string) error {
	ct, err := s.Seal(fmt.Sprintf("outbox-err:%d", seq), []byte(lastError))
	if err != nil {
		return err
	}
	_, err = s.DB().Exec(`UPDATE outbox SET attempts=attempts+1, last_error_enc=? WHERE seq=?`, ct, seq)
	return err
}

func (s *Store) CountOutbox(state string) int {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM outbox WHERE state=?`, state).Scan(&n)
	return n
}
