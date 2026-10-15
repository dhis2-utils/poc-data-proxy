package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// AggForm is central's dataEntry/dataValues answer for one form (Body, sealed), with
// the completion registration seen when it was fetched (Reg) and whether it must be
// fetched again (Stale).
type AggForm struct {
	Key, DataSet, Period, OrgUnit, AOC string
	Body                               []byte
	Reg                                string
	FetchedAt                          string
	Stale                              bool
}

func (s *Store) PutAggForm(f *AggForm) error {
	ct, err := s.Seal("agg_form:"+f.Key, f.Body)
	if err != nil {
		return err
	}
	_, err = s.write(`INSERT INTO agg_forms(key, data_set, period, org_unit, aoc, body_enc, reg, fetched_at, stale)
 VALUES(?,?,?,?,?,?,?,?,0) ON CONFLICT(key) DO UPDATE SET body_enc=excluded.body_enc, reg=excluded.reg,
 fetched_at=excluded.fetched_at, stale=0`, f.Key, f.DataSet, f.Period, f.OrgUnit, f.AOC, ct, f.Reg, f.FetchedAt)
	return err
}

func (s *Store) scanAggForm(sc interface{ Scan(...any) error }, withBody bool) (*AggForm, error) {
	f := &AggForm{}
	var ct []byte
	if err := sc.Scan(&f.Key, &f.DataSet, &f.Period, &f.OrgUnit, &f.AOC, &ct, &f.Reg, &f.FetchedAt, &f.Stale); err != nil {
		return nil, err
	}
	if withBody {
		b, err := s.OpenBlob("agg_form:"+f.Key, ct)
		if err != nil {
			return nil, err
		}
		f.Body = b
	}
	return f, nil
}

const aggFormCols = `key, data_set, period, org_unit, aoc, body_enc, reg, fetched_at, stale`

// GetAggForm returns a form with its body, or nil.
func (s *Store) GetAggForm(key string) (*AggForm, error) {
	f, err := s.scanAggForm(s.DB().QueryRow(`SELECT `+aggFormCols+` FROM agg_forms WHERE key=?`, key), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return f, err
}

// AnyAggForm is the data set's most recently fetched form (its min-max values serve
// the data set's forms central has nothing for).
func (s *Store) AnyAggForm(dataSet string) (*AggForm, error) {
	f, err := s.scanAggForm(s.DB().QueryRow(`SELECT `+aggFormCols+` FROM agg_forms WHERE data_set=?
 ORDER BY fetched_at DESC LIMIT 1`, dataSet), true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return f, err
}

// AggForms lists every form, without bodies.
func (s *Store) AggForms() ([]*AggForm, error) {
	rows, err := s.DB().Query(`SELECT ` + aggFormCols + ` FROM agg_forms ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AggForm
	for rows.Next() {
		f, err := s.scanAggForm(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) MarkAggFormStale(key string) error {
	_, err := s.write(`UPDATE agg_forms SET stale=1 WHERE key=?`, key)
	return err
}

func (s *Store) DeleteAggForm(key string) error {
	_, err := s.write(`DELETE FROM agg_forms WHERE key=?`, key)
	return err
}

// AppendAggregate queues an aggregate edit of a form. cell names what it sets
// (aggregate.Edit.CellKey: the form, data element, option combination and field). An
// edit replaces the same user's pending edit of the same cell and field when that is
// the latest pending entry touching the cell (any field, any user) and no completion
// of the form was queued after it: repeated edits push once, and the order of edits
// to one cell holds (a delete never moves before a later comment). A completion
// depends on the form's pending entries before it, so it is pushed after them.
func (s *Store) AppendAggregate(tx *sql.Tx, userID int64, payload []byte, cell, form string, completion bool) (int64, error) {
	base := cell // the cell without its field
	if i := strings.LastIndex(cell, "|"); i > 0 && !completion {
		base = cell[:i]
	}
	if !completion {
		var seq int64
		err := tx.QueryRow(`SELECT o.seq FROM outbox o JOIN outbox_objects oo ON oo.seq=o.seq
 WHERE o.state=? AND o.kind=? AND o.user_id=? AND oo.uid=?
   AND o.seq > coalesce((SELECT max(c.seq) FROM outbox c JOIN outbox_objects cc ON cc.seq=c.seq
     WHERE c.state=? AND cc.uid=? AND cc.kind='aggregate-completion'), 0)
   AND o.seq >= (SELECT max(b.seq) FROM outbox b JOIN outbox_objects bb ON bb.seq=b.seq WHERE b.state=? AND bb.uid=?)
 ORDER BY o.seq DESC LIMIT 1`, OutboxPending, OutboxAggregate, userID, cell, OutboxPending, form, OutboxPending, base).Scan(&seq)
		if err == nil {
			ct, err := s.Seal(fmt.Sprintf("outbox:%d", seq), payload)
			if err != nil {
				return 0, err
			}
			_, err = tx.Exec(`UPDATE outbox SET payload_enc=?, created_at=? WHERE seq=?`, ct, Now(), seq)
			return seq, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	var dep any
	if completion {
		if d := s.LatestPendingSeqFor(tx, []string{form}); d > 0 {
			dep = d
		}
	}
	res, err := tx.Exec(`INSERT INTO outbox(user_id, payload_enc, created_at, state, depends_on_seq, params, kind)
 VALUES(?,?,?,?,?,'',?)`, userID, []byte{}, Now(), OutboxPending, dep, OutboxAggregate)
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
	kind := "aggregate-cell"
	if completion {
		kind = "aggregate-completion"
	}
	refs := []OutboxRef{{UID: cell, Kind: kind}, {UID: form, Kind: kind}}
	if !completion {
		refs = append(refs, OutboxRef{UID: base, Kind: "aggregate-cellbase"})
	}
	for _, r := range refs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO outbox_objects(seq, uid, kind) VALUES(?,?,?)`, seq, r.UID, r.Kind); err != nil {
			return 0, err
		}
	}
	return seq, nil
}

// PendingAggregate returns the form's pending entries in seq order, with payloads.
func (s *Store) PendingAggregate(form string) ([]*OutboxEntry, error) {
	rows, err := s.DB().Query(`SELECT `+outboxCols+` FROM outbox WHERE state=? AND kind=? AND seq IN
 (SELECT seq FROM outbox_objects WHERE uid=?) ORDER BY seq`, OutboxPending, OutboxAggregate, form)
	if err != nil {
		return nil, err
	}
	return s.scanOutbox(rows, true)
}

// PendingAggregateAll returns every pending aggregate entry, with payloads.
func (s *Store) PendingAggregateAll() ([]*OutboxEntry, error) {
	rows, err := s.DB().Query(`SELECT `+outboxCols+` FROM outbox WHERE state=? AND kind=? ORDER BY seq`,
		OutboxPending, OutboxAggregate)
	if err != nil {
		return nil, err
	}
	return s.scanOutbox(rows, true)
}

// OutboxPayload is an entry's current payload: an aggregate entry's can be replaced
// by a later edit of its cell while it waits (AppendAggregate).
func (s *Store) OutboxPayload(seq int64) ([]byte, error) {
	var ct []byte
	if err := s.DB().QueryRow(`SELECT payload_enc FROM outbox WHERE seq=?`, seq).Scan(&ct); err != nil {
		return nil, err
	}
	return s.OpenBlob(fmt.Sprintf("outbox:%d", seq), ct)
}

// FinishOutboxIfUnchanged finishes an entry only if its payload is still the one
// pushed; if a later edit replaced it meanwhile, the entry stays pending and the newer
// edit is pushed next. It reports whether the entry was finished.
func (s *Store) FinishOutboxIfUnchanged(seq int64, state, lastError string, pushed []byte) (bool, error) {
	finished := false
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		var ct []byte
		if err := tx.QueryRow(`SELECT payload_enc FROM outbox WHERE seq=?`, seq).Scan(&ct); err != nil {
			return err
		}
		cur, err := s.OpenBlob(fmt.Sprintf("outbox:%d", seq), ct)
		if err != nil {
			return err
		}
		if !bytes.Equal(cur, pushed) {
			return nil
		}
		var errCT []byte
		if lastError != "" {
			if errCT, err = s.Seal(fmt.Sprintf("outbox-err:%d", seq), []byte(lastError)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE outbox SET state=?, attempts=attempts+1, last_error_enc=? WHERE seq=?`, state, errCT, seq); err != nil {
			return err
		}
		finished = true
		return nil
	})
	return finished, err
}

// EarlierPendingFor reports whether a pending entry before seq refers to uid (a cell
// without its field, or a form): aggregate entries of one cell, and a form's
// completion, are pushed in the order they were made even when an earlier author's
// entries must wait.
func (s *Store) EarlierPendingFor(seq int64, uid string) bool {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM outbox o JOIN outbox_objects b ON b.seq=o.seq
 WHERE o.state=? AND o.seq<? AND b.uid=?`, OutboxPending, seq, uid).Scan(&n)
	return n > 0
}
