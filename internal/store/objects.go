package store

import (
	"database/sql"
	"errors"
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
	Payload      []byte
}

func objAAD(uid string) string { return "obj:" + uid }

// PutObject inserts or replaces an object row.
func (s *Store) PutObject(q Execer, o *Object) error {
	ct, err := s.Seal(objAAD(o.UID), o.Payload)
	if err != nil {
		return err
	}
	_, err = q.Exec(`INSERT INTO objects(uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at,
 origin, sync_state, payload_enc) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(uid) DO UPDATE SET kind=excluded.kind, te_uid=excluded.te_uid, enrollment_uid=excluded.enrollment_uid,
 program=excluded.program, stage=excluded.stage, tet=excluded.tet, owner_org_unit=excluded.owner_org_unit,
 updated_at=excluded.updated_at, origin=excluded.origin, sync_state=excluded.sync_state, payload_enc=excluded.payload_enc`,
		o.UID, o.Kind, o.TE, o.Enrollment, o.Program, o.Stage, o.TET, o.OwnerOrgUnit, o.UpdatedAt, o.Origin, o.SyncState, ct)
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
	err := s.DB.QueryRow(`SELECT uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at, origin,
 sync_state, payload_enc FROM objects WHERE uid=?`, uid).Scan(&o.UID, &o.Kind, &o.TE, &o.Enrollment, &o.Program,
		&o.Stage, &o.TET, &o.OwnerOrgUnit, &o.UpdatedAt, &o.Origin, &o.SyncState, &ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.Payload, err = s.OpenBlob(objAAD(o.UID), ct)
	return o, err
}

// AllObjects decrypts every object (used to build the in-memory index on unlock).
func (s *Store) AllObjects(fn func(*Object) error) error {
	rows, err := s.DB.Query(`SELECT uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at,
 origin, sync_state, payload_enc FROM objects`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		o := &Object{}
		var ct []byte
		if err := rows.Scan(&o.UID, &o.Kind, &o.TE, &o.Enrollment, &o.Program, &o.Stage, &o.TET, &o.OwnerOrgUnit,
			&o.UpdatedAt, &o.Origin, &o.SyncState, &ct); err != nil {
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

// Visibility returns object UID -> set of user IDs.
func (s *Store) Visibility() (map[string]map[int64]bool, error) {
	rows, err := s.DB.Query(`SELECT object_uid, user_id FROM visibility`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[int64]bool{}
	for rows.Next() {
		var uid string
		var id int64
		if err := rows.Scan(&uid, &id); err != nil {
			return nil, err
		}
		if out[uid] == nil {
			out[uid] = map[int64]bool{}
		}
		out[uid][id] = true
	}
	return out, rows.Err()
}

func (s *Store) AddVisibility(q Execer, uid string, userID int64) error {
	_, err := q.Exec(`INSERT OR IGNORE INTO visibility(object_uid, user_id) VALUES(?,?)`, uid, userID)
	return err
}

func (s *Store) RemoveVisibility(q Execer, uid string, userID int64) error {
	_, err := q.Exec(`DELETE FROM visibility WHERE object_uid=? AND user_id=?`, uid, userID)
	return err
}

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (s *Store) CountObjects(state string) int {
	var n int
	s.DB.QueryRow(`SELECT count(*) FROM objects WHERE sync_state=?`, state).Scan(&n)
	return n
}
