package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Object kinds, origins and sync states (spec 9.2).
const (
	KindTrackedEntity = "trackedEntity"
	KindEnrollment    = "enrollment"
	KindEvent         = "event"
	KindRelationship  = "relationship"

	OriginCentral = "central"
	OriginLocal   = "local"

	SyncSynced   = "synced"
	SyncPending  = "pending"
	SyncRejected = "rejected"
)

// Object is one stored tracker object. Payload is the decrypted JSON of the object
// without nested children (enrollments, events and relationships are separate rows).
type Object struct {
	UID          string
	Kind         string
	TE           string
	Enrollment   string
	Program      string
	Stage        string
	TET          string
	OwnerOrgUnit string
	UpdatedAt    string
	Origin       string
	SyncState    string
	// Slot is the object's stable number in the visibility bitmaps (never 0 once stored).
	Slot    int64
	Payload []byte
}

func objAAD(uid string) string { return "obj:" + uid }

// PutObject inserts or replaces an object row. An object keeps the slot it was first
// stored with.
func (s *Store) PutObject(q Execer, o *Object) error {
	if o.Slot <= 0 {
		return fmt.Errorf("object %s has no slot", o.UID)
	}
	ct, err := s.Seal(objAAD(o.UID), o.Payload)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO objects(uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at,
 origin, sync_state, payload_enc, slot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(uid) DO UPDATE SET kind=excluded.kind, te_uid=excluded.te_uid, enrollment_uid=excluded.enrollment_uid,
 program=excluded.program, stage=excluded.stage, tet=excluded.tet, owner_org_unit=excluded.owner_org_unit,
 updated_at=excluded.updated_at, origin=excluded.origin, sync_state=excluded.sync_state, payload_enc=excluded.payload_enc,
 slot=coalesce(objects.slot, excluded.slot)`,
		o.UID, o.Kind, o.TE, o.Enrollment, o.Program, o.Stage, o.TET, o.OwnerOrgUnit, o.UpdatedAt, o.Origin, o.SyncState, ct, o.Slot)
	return err
}

func (s *Store) SetSyncState(q Execer, uid, state string) error {
	_, err := q.Exec(`UPDATE objects SET sync_state=? WHERE uid=?`, state, uid)
	return err
}

// GetObject returns the object or nil.
func (s *Store) GetObject(uid string) (*Object, error) {
	o := &Object{}
	var ct []byte
	err := s.DB().QueryRow(`SELECT uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at, origin,
 sync_state, payload_enc, slot FROM objects WHERE uid=?`, uid).Scan(&o.UID, &o.Kind, &o.TE, &o.Enrollment, &o.Program,
		&o.Stage, &o.TET, &o.OwnerOrgUnit, &o.UpdatedAt, &o.Origin, &o.SyncState, &ct, &o.Slot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.Payload, err = s.OpenBlob(objAAD(o.UID), ct)
	return o, err
}

// Payloads returns the decrypted payloads of these objects (missing ones are left
// out), in batches: a rendered page needs many, and a query per object was most of
// the cost of serving it (docs/PERFORMANCE.md).
func (s *Store) Payloads(uids []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(uids))
	const batch = 500
	for start := 0; start < len(uids); start += batch {
		part := uids[start:min(start+batch, len(uids))]
		args := make([]any, len(part))
		for i, u := range part {
			args[i] = u
		}
		rows, err := s.DB().Query(`SELECT uid, payload_enc FROM objects WHERE uid IN (?`+strings.Repeat(",?", len(part)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uid string
			var ct []byte
			if err := rows.Scan(&uid, &ct); err != nil {
				rows.Close()
				return nil, err
			}
			if out[uid], err = s.OpenBlob(objAAD(uid), ct); err != nil {
				rows.Close()
				return nil, err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AllObjects decrypts every object (used to build the in-memory index on unlock).
func (s *Store) AllObjects(fn func(*Object) error) error {
	rows, err := s.DB().Query(`SELECT uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at,
 origin, sync_state, payload_enc, slot FROM objects`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		o := &Object{}
		var ct []byte
		if err := rows.Scan(&o.UID, &o.Kind, &o.TE, &o.Enrollment, &o.Program, &o.Stage, &o.TET, &o.OwnerOrgUnit,
			&o.UpdatedAt, &o.Origin, &o.SyncState, &ct, &o.Slot); err != nil {
			return err
		}
		if o.Payload, err = s.OpenBlob(objAAD(o.UID), ct); err != nil {
			return err
		}
		if err := fn(o); err != nil {
			return err
		}
	}
	return rows.Err()
}

// VisibilityChunkBits is how many slots one stored chunk of a user's visibility
// bitmap covers (512 bytes).
const VisibilityChunkBits = 4096

// VisibilityChunks returns each user's visibility bitmap chunks: user -> chunk -> bits
// (little-endian 64-bit words). Rows of the per-object table that schema 1 kept are
// converted first.
func (s *Store) VisibilityChunks() (map[int64]map[int64][]byte, error) {
	if err := s.convertLegacyVisibility(); err != nil {
		return nil, fmt.Errorf("visibility: %w", err)
	}
	rows, err := s.DB().Query(`SELECT user_id, chunk, bits FROM visibility_bits`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[int64][]byte{}
	for rows.Next() {
		var user, chunk int64
		var b []byte
		if err := rows.Scan(&user, &chunk, &b); err != nil {
			return nil, err
		}
		if out[user] == nil {
			out[user] = map[int64][]byte{}
		}
		out[user][chunk] = b
	}
	return out, rows.Err()
}

// PutVisibilityChunk stores one chunk of a user's visibility bitmap.
func (s *Store) PutVisibilityChunk(q Execer, user, chunk int64, bits []byte) error {
	_, err := q.Exec(`INSERT INTO visibility_bits(user_id, chunk, bits) VALUES(?,?,?)
ON CONFLICT(user_id, chunk) DO UPDATE SET bits=excluded.bits`, user, chunk, bits)
	return err
}

// TEScopes returns, per tracked entity, the pull scopes (a program, or "tet:<type>")
// it was fetched in full for: central shows a program's attributes only when asked
// with that program.
func (s *Store) TEScopes() (map[string][]string, error) {
	rows, err := s.DB().Query(`SELECT te_uid, scope FROM te_scopes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var te, scope string
		if err := rows.Scan(&te, &scope); err != nil {
			return nil, err
		}
		out[te] = append(out[te], scope)
	}
	return out, rows.Err()
}

func (s *Store) AddTEScope(q Execer, te, scope string) error {
	_, err := q.Exec(`INSERT OR IGNORE INTO te_scopes(te_uid, scope) VALUES(?,?)`, te, scope)
	return err
}

// MaxSlot is the highest slot in use.
func (s *Store) MaxSlot() int64 {
	var n sql.NullInt64
	s.DB().QueryRow(`SELECT max(slot) FROM objects`).Scan(&n)
	return n.Int64
}

// convertLegacyVisibility moves the rows of the per-object visibility table into the
// bitmaps and empties it.
func (s *Store) convertLegacyVisibility() error {
	var n int
	if err := s.DB().QueryRow(`SELECT count(*) FROM visibility`).Scan(&n); err != nil || n == 0 {
		return err
	}
	return s.Tx(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT o.slot, v.user_id FROM visibility v JOIN objects o ON o.uid = v.object_uid`)
		if err != nil {
			return err
		}
		chunks := map[[2]int64][]byte{}
		for rows.Next() {
			var slot, user int64
			if err := rows.Scan(&slot, &user); err != nil {
				rows.Close()
				return err
			}
			k := [2]int64{user, slot / VisibilityChunkBits}
			if chunks[k] == nil {
				var old []byte
				tx.QueryRow(`SELECT bits FROM visibility_bits WHERE user_id=? AND chunk=?`, k[0], k[1]).Scan(&old)
				chunks[k] = make([]byte, VisibilityChunkBits/8)
				copy(chunks[k], old)
			}
			bit := slot % VisibilityChunkBits
			chunks[k][bit/8] |= 1 << (bit % 8)
		}
		rows.Close()
		for k, b := range chunks {
			if err := s.PutVisibilityChunk(tx, k[0], k[1], b); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`DELETE FROM visibility`)
		return err
	})
}

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (s *Store) CountObjects(state string) int {
	var n int
	s.DB().QueryRow(`SELECT count(*) FROM objects WHERE sync_state=?`, state).Scan(&n)
	return n
}
