package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
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

// RestoreOutbox puts back entries copied to the stick, as pending. One the store
// already holds (the snapshot had it) takes the stick's version when that is newer: a
// coalesced aggregate edit rewrites its entry in place.
func (s *Store) RestoreOutbox(entries []*RawOutbox) (int, error) {
	n := 0
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, e := range entries {
			var dep any
			if e.DependsOnSeq > 0 {
				dep = e.DependsOnSeq
			}
			res, err := tx.Exec(`INSERT INTO outbox(seq, user_id, payload_enc, created_at, state, depends_on_seq, params, kind)
 VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(seq) DO UPDATE SET payload_enc = excluded.payload_enc, created_at = excluded.created_at
 WHERE outbox.state = ? AND excluded.created_at > outbox.created_at`,
				e.Seq, e.UserID, e.PayloadEnc, e.CreatedAt, OutboxPending, dep, e.Params, e.Kind, OutboxPending)
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

// UnwrapFromKeyFile checks a user's password against a key file that is not open (a
// copy on the USB stick) and returns the data key it unwraps.
func UnwrapFromKeyFile(path, username, password string) (hubcrypto.Key, error) {
	db, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var salt, wrapped []byte
	if err := db.QueryRow(`SELECT kek_salt, wrapped_dk FROM key_slots WHERE username = ?`, username).Scan(&salt, &wrapped); err != nil {
		return nil, errors.New("wrong username or password")
	}
	dk, err := hubcrypto.UnwrapDK(wrapped, password, salt)
	if err != nil {
		return nil, errors.New("wrong username or password")
	}
	return dk, nil
}

// Size is the store's size on disk (key file and data file), for checking a snapshot
// fits.
func (s *Store) Size() int64 {
	var n int64
	for _, p := range []string{s.Path, DataPath(s.Path)} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// usersCols are the data file's users columns a restore needs: tokens and passwords
// stay sealed as stored.
var usersCols = "id, username, user_uid, pat_enc, pat_uid, pat_expires, pw_enc, access_json_enc, access_refreshed_at, last_login_at"

type userRow struct {
	ID                             int64
	Username, UserUID              string
	PATEnc                         []byte
	PATUID, PATExpires             string
	PwEnc, AccessEnc               []byte
	AccessRefreshedAt, LastLoginAt string
}

type stateRow struct {
	Key      string
	ValueEnc []byte
}

// ExportState is what a restore needs of the data file besides the pending saves:
// the users (their tokens, still sealed) and the hub's state (settings, catalog, hub
// token), as JSON. The cached data is left out: central has it.
func (s *Store) ExportState() ([]byte, error) {
	var out struct {
		Users []userRow
		State []stateRow
	}
	rows, err := s.DB().Query(`SELECT ` + usersCols + ` FROM users ORDER BY id`)
	err = scanRows(rows, err, func(r *sql.Rows) error {
		var u userRow
		err := r.Scan(&u.ID, &u.Username, &u.UserUID, &u.PATEnc, &u.PATUID, &u.PATExpires, &u.PwEnc, &u.AccessEnc,
			&u.AccessRefreshedAt, &u.LastLoginAt)
		out.Users = append(out.Users, u)
		return err
	})
	if err != nil {
		return nil, err
	}
	rows, err = s.DB().Query(`SELECT key, value_enc FROM state ORDER BY key`)
	err = scanRows(rows, err, func(r *sql.Rows) error {
		var st stateRow
		err := r.Scan(&st.Key, &st.ValueEnc)
		out.State = append(out.State, st)
		return err
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// ImportState puts back what ExportState wrote, into a restored hub's new data file.
func (s *Store) ImportState(b []byte) error {
	var in struct {
		Users []userRow
		State []stateRow
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	return s.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, u := range in.Users {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO users(`+usersCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`, u.ID, u.Username,
				u.UserUID, u.PATEnc, u.PATUID, u.PATExpires, u.PwEnc, u.AccessEnc, u.AccessRefreshedAt, u.LastLoginAt); err != nil {
				return err
			}
		}
		for _, st := range in.State {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO state(key, value_enc) VALUES(?,?)`, st.Key, st.ValueEnc); err != nil {
				return err
			}
		}
		return nil
	})
}

// PendingWithoutUser counts pending saves whose user has no row on this hub (a hub
// restored from a copy older than that user): they cannot be pushed as their author.
func (s *Store) PendingWithoutUser() int {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM outbox o WHERE o.state = ?
 AND NOT EXISTS (SELECT 1 FROM users u WHERE u.id = o.user_id)`, OutboxPending).Scan(&n)
	return n
}
