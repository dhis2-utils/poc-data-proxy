package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
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
	Slot int64
	// What queries filter and sort on: the payload's createdAt and status, a
	// relationship's ends, and Slim, the JSON of the fields the remaining filters read
	// (tracker's slim objects). Reads without the payload return Slim instead.
	CreatedAt, Status string
	RelFrom, RelTo    string
	Slim              []byte
	Attrs             []Attr // a tracked entity's attribute values
	Payload           []byte
}

// Attr is one attribute value of a tracked entity, as queries search it.
type Attr struct{ Attr, Value string }

// The writes run inside transactions. They are prepared when the store unlocks
// (prepareWrites): preparing takes a connection of its own, which a transaction must
// not wait for while other writers hold the rest of the pool waiting for its lock.
const (
	sqlPutObject = `INSERT INTO objects(uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at,
 origin, sync_state, payload_enc, slot, created_at, status, rel_from, rel_to, slim, te_created)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,
 CASE WHEN ?2 = 'enrollment' THEN coalesce((SELECT created_at FROM objects WHERE uid = ?3 AND kind = 'trackedEntity'), '') ELSE '' END)
ON CONFLICT(uid) DO UPDATE SET kind=excluded.kind, te_uid=excluded.te_uid, enrollment_uid=excluded.enrollment_uid,
 program=excluded.program, stage=excluded.stage, tet=excluded.tet, owner_org_unit=excluded.owner_org_unit,
 updated_at=excluded.updated_at, origin=excluded.origin, sync_state=excluded.sync_state, payload_enc=excluded.payload_enc,
 slot=coalesce(objects.slot, excluded.slot), created_at=excluded.created_at, status=excluded.status,
 rel_from=excluded.rel_from, rel_to=excluded.rel_to, slim=excluded.slim, te_created=excluded.te_created`
	sqlTECreated     = `UPDATE objects INDEXED BY objects_te SET te_created = ? WHERE te_uid = ? AND kind = 'enrollment' AND te_created <> ?`
	sqlDeleteAttrs   = `DELETE FROM te_attrs WHERE te_uid = ?`
	sqlInsertAttr    = `INSERT OR REPLACE INTO te_attrs(te_uid, attr, value, value_lc) VALUES(?,?,?,?)`
	sqlSetSyncState  = `UPDATE objects SET sync_state=? WHERE uid=?`
	sqlPutVisibility = `INSERT INTO visibility_bits(user_id, chunk, bits) VALUES(?,?,?)
ON CONFLICT(user_id, chunk) DO UPDATE SET bits=excluded.bits`
	sqlAddTEScope = `INSERT OR IGNORE INTO te_scopes(te_uid, scope) VALUES(?,?)`
)

var writeStatements = []string{sqlPutObject, sqlTECreated, sqlDeleteAttrs, sqlInsertAttr, sqlSetSyncState, sqlPutVisibility, sqlAddTEScope}

// prepareWrites prepares the write statements on the data file.
func (s *Store) prepareWrites(db *sql.DB) error {
	for _, q := range writeStatements {
		if _, err := s.stmt(db, q); err != nil {
			return err
		}
	}
	return nil
}

func objAAD(uid string) string { return "obj:" + uid }

// PutObject inserts or replaces an object row. An object keeps the slot it was first
// stored with. An enrollment's row carries its person's creation date (te_created),
// so a program's list of people reads in that order from an index.
func (s *Store) PutObject(q Execer, o *Object) error {
	if o.Slot <= 0 {
		return fmt.Errorf("object %s has no slot", o.UID)
	}
	ct, err := s.Seal(objAAD(o.UID), o.Payload)
	if err != nil {
		return err
	}
	_, err = s.exec(q, sqlPutObject,
		o.UID, o.Kind, o.TE, o.Enrollment, o.Program, o.Stage, o.TET, o.OwnerOrgUnit, o.UpdatedAt, o.Origin, o.SyncState, ct,
		o.Slot, o.CreatedAt, o.Status, o.RelFrom, o.RelTo, string(o.Slim))
	if err != nil || o.Kind != KindTrackedEntity {
		return err
	}
	if _, err := s.exec(q, sqlTECreated,
		o.CreatedAt, o.UID, o.CreatedAt); err != nil {
		return err
	}
	if _, err := s.exec(q, sqlDeleteAttrs, o.UID); err != nil {
		return err
	}
	for _, a := range o.Attrs {
		if _, err := s.exec(q, sqlInsertAttr,
			o.UID, a.Attr, a.Value, strings.ToLower(a.Value)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SetSyncState(q Execer, uid, state string) error {
	if q == s.DB() {
		_, err := s.write(sqlSetSyncState, state, uid)
		return err
	}
	_, err := s.exec(q, sqlSetSyncState, state, uid)
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
	sealed, err := s.SealedPayloads(uids)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(sealed))
	for uid, ct := range sealed {
		if out[uid], err = s.OpenPayload(uid, ct); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// OpenPayload decrypts a payload SealedPayloads returned.
func (s *Store) OpenPayload(uid string, sealed []byte) ([]byte, error) {
	return s.OpenBlob(objAAD(uid), sealed)
}

// SealedPayloads returns these objects' payloads still sealed. Each write seals with a
// new random nonce, which comes first, so it identifies the payload's version.
func (s *Store) SealedPayloads(uids []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(uids))
	err := inBatches(uids, func(args []any, marks string) error {
		rows, err := s.query(`SELECT uid, payload_enc FROM objects WHERE uid IN (`+marks+`)`, args...)
		return scanRows(rows, err, func(r *sql.Rows) error {
			var uid string
			var ct []byte
			err := r.Scan(&uid, &ct)
			out[uid] = ct
			return err
		})
	})
	return out, err
}

// objectColumns are the columns ScanObject reads, without the payload.
var objectColumns = []string{"uid", "kind", "te_uid", "enrollment_uid", "program", "stage", "tet", "owner_org_unit",
	"updated_at", "origin", "sync_state", "slot", "created_at", "status", "rel_from", "rel_to", "slim"}

var objectCols = ObjectCols("")

// ObjectCols lists the columns ScanObject reads, of the objects table under alias.
func ObjectCols(alias string) string {
	out := make([]string, len(objectColumns))
	for i, c := range objectColumns {
		if alias != "" {
			c = alias + "." + c
		}
		if strings.HasSuffix(c, "slim") {
			c = "coalesce(" + c + ", '')"
		}
		out[i] = c
	}
	return strings.Join(out, ", ")
}

// ScanObject reads a row of ObjectCols, followed by any extra columns.
func ScanObject(sc interface{ Scan(...any) error }, extra ...any) (*Object, error) {
	o := &Object{}
	var slim string
	err := sc.Scan(append([]any{&o.UID, &o.Kind, &o.TE, &o.Enrollment, &o.Program, &o.Stage, &o.TET, &o.OwnerOrgUnit,
		&o.UpdatedAt, &o.Origin, &o.SyncState, &o.Slot, &o.CreatedAt, &o.Status, &o.RelFrom, &o.RelTo, &slim}, extra...)...)
	o.Slim = []byte(slim)
	return o, err
}

// QueryObjects runs a query whose columns are ObjectCols and returns every row,
// without payloads. The rows are read in full before returning, so the caller may run
// other queries on what it gets: holding a connection while querying on another can
// exhaust the pool under load.
func (s *Store) QueryObjects(query string, args ...any) ([]*Object, error) {
	rows, err := s.query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Object
	for rows.Next() {
		o, err := ScanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// QueryObjectPairs is QueryObjects for rows of two objects: ObjectCols of one, then of
// the other.
func (s *Store) QueryObjectPairs(query string, args ...any) ([][2]*Object, error) {
	rows, err := s.query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]*Object
	for rows.Next() {
		b := &Object{}
		var slim string
		a, err := ScanObject(rows, &b.UID, &b.Kind, &b.TE, &b.Enrollment, &b.Program, &b.Stage, &b.TET, &b.OwnerOrgUnit,
			&b.UpdatedAt, &b.Origin, &b.SyncState, &b.Slot, &b.CreatedAt, &b.Status, &b.RelFrom, &b.RelTo, &slim)
		if err != nil {
			return nil, err
		}
		b.Slim = []byte(slim)
		out = append(out, [2]*Object{a, b})
	}
	return out, rows.Err()
}

// inBatches runs fn over ids in batches of 500 (SQLite's limit on parameters is
// higher, but the statements stay small), with each batch's arguments and "?,?,…".
func inBatches[T any](ids []T, fn func(args []any, marks string) error) error {
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		part := ids[start:min(start+batch, len(ids))]
		args := make([]any, len(part))
		for i, v := range part {
			args[i] = v
		}
		if err := fn(args, "?"+strings.Repeat(",?", len(part)-1)); err != nil {
			return err
		}
	}
	return nil
}

// scanRows reads every row of rows with scan.
func scanRows(rows *sql.Rows, err error, scan func(*sql.Rows) error) error {
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ObjectsByUID returns these objects without payloads (missing ones are left out).
func (s *Store) ObjectsByUID(uids []string) (map[string]*Object, error) {
	out := make(map[string]*Object, len(uids))
	err := inBatches(uids, func(args []any, marks string) error {
		objs, err := s.QueryObjects(`SELECT `+objectCols+` FROM objects WHERE uid IN (`+marks+`)`, args...)
		for _, o := range objs {
			out[o.UID] = o
		}
		return err
	})
	return out, err
}

// ObjectState is what a pull compares with central's list: the stored update time
// and the visibility slot.
type ObjectState struct {
	UpdatedAt string
	Slot      int64
}

// ObjectStates returns the state of these stored objects, by uid (missing ones are
// left out). It reads the objects_state index alone, in uid order: a pull asks this
// for everything central lists, and reading table rows in central's order was one
// random page read per object, which an SD card answers slowly.
func (s *Store) ObjectStates(uids []string) (map[string]ObjectState, error) {
	sorted := slices.Clone(uids)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	out := make(map[string]ObjectState, len(sorted))
	err := inBatches(sorted, func(args []any, marks string) error {
		rows, err := s.query(`SELECT uid, updated_at, slot FROM objects INDEXED BY objects_state WHERE uid IN (`+marks+`)`, args...)
		return scanRows(rows, err, func(r *sql.Rows) error {
			var uid string
			var st ObjectState
			err := r.Scan(&uid, &st.UpdatedAt, &st.Slot)
			out[uid] = st
			return err
		})
	})
	return out, err
}

// SlotOrigins returns the origin and program of the objects in these slots, by slot.
func (s *Store) SlotOrigins(slots []int64) (map[int64][2]string, error) {
	out := make(map[int64][2]string, len(slots))
	err := inBatches(slots, func(args []any, marks string) error {
		rows, err := s.query(`SELECT slot, origin, program FROM objects WHERE slot IN (`+marks+`)`, args...)
		return scanRows(rows, err, func(r *sql.Rows) error {
			var slot int64
			var origin, program string
			err := r.Scan(&slot, &origin, &program)
			out[slot] = [2]string{origin, program}
			return err
		})
	})
	return out, err
}

// UnfilledObjects returns up to limit objects, with payloads, whose query columns
// were never filled (rows stored before schema 3).
func (s *Store) UnfilledObjects(limit int) ([]*Object, error) {
	rows, err := s.DB().Query(`SELECT uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at,
 origin, sync_state, payload_enc, slot FROM objects WHERE slim IS NULL LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Object
	for rows.Next() {
		o := &Object{}
		var ct []byte
		if err := rows.Scan(&o.UID, &o.Kind, &o.TE, &o.Enrollment, &o.Program, &o.Stage, &o.TET, &o.OwnerOrgUnit,
			&o.UpdatedAt, &o.Origin, &o.SyncState, &ct, &o.Slot); err != nil {
			return nil, err
		}
		if o.Payload, err = s.OpenBlob(objAAD(o.UID), ct); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// CountKind is how many objects there are, of one kind or ("") all.
func (s *Store) CountKind(kind string) int {
	var n int
	if kind == "" {
		s.DB().QueryRow(`SELECT count(*) FROM objects`).Scan(&n)
	} else {
		s.DB().QueryRow(`SELECT count(*) FROM objects WHERE kind=?`, kind).Scan(&n)
	}
	return n
}

// AllObjects decrypts every object (the storage benchmarks).
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
	_, err := s.exec(q, sqlPutVisibility, user, chunk, bits)
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
	_, err := s.exec(q, sqlAddTEScope, te, scope)
	return err
}

// SlotOf is the stored object's slot, or 0.
func (s *Store) SlotOf(uid string) int64 {
	var n int64
	if rows, err := s.query(`SELECT slot FROM objects WHERE uid = ?`, uid); err == nil {
		if rows.Next() {
			rows.Scan(&n)
		}
		rows.Close()
	}
	return n
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

// stmtCache holds statements prepared once on the data file: a pull stores thousands
// of objects, and preparing each statement for each of them was a large part of it.
type stmtCache struct {
	mu sync.Mutex
	db *sql.DB
	m  map[string]*sql.Stmt
}

// stmt returns the statement for query, prepared once on the data file. Queries with
// lists of values ("IN (?,?,…)") differ in length and would fill the cache with
// statements used once: they are not kept (nil), nor are more than 1000 others.
func (s *Store) stmt(db *sql.DB, query string) (*sql.Stmt, error) {
	if strings.Contains(query, "IN (?") {
		return nil, nil
	}
	s.stmts.mu.Lock()
	defer s.stmts.mu.Unlock()
	if s.stmts.db != db {
		s.stmts.db, s.stmts.m = db, map[string]*sql.Stmt{}
	}
	if st := s.stmts.m[query]; st != nil {
		return st, nil
	}
	if len(s.stmts.m) >= 1000 {
		return nil, nil
	}
	st, err := db.Prepare(query)
	if err == nil {
		s.stmts.m[query] = st
	}
	return st, err
}

// exec runs query on q, the data file or a transaction on it, through its statement
// prepared at unlock; anything else runs unprepared, so a transaction never needs a
// second connection.
func (s *Store) exec(q Execer, query string, args ...any) (sql.Result, error) {
	db := s.DB()
	s.stmts.mu.Lock()
	var st *sql.Stmt
	if s.stmts.db == db {
		st = s.stmts.m[query]
	}
	s.stmts.mu.Unlock()
	switch t := q.(type) {
	case *sql.Tx:
		if st != nil {
			return t.Stmt(st).Exec(args...)
		}
	case *sql.DB:
		if t == db && st != nil {
			return st.Exec(args...)
		}
	}
	return q.Exec(query, args...)
}

// write runs a single-statement write on the data file, in turn with the write
// transactions (Store.Tx): left to SQLite, writers poll for the lock and give up on a
// starved CPU. Never call it inside a Tx: the lock is not reentrant.
func (s *Store) write(query string, args ...any) (sql.Result, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.exec(s.DB(), query, args...)
}

// query runs a read on the data file through a statement prepared once.
func (s *Store) query(query string, args ...any) (*sql.Rows, error) {
	db := s.DB()
	st, err := s.stmt(db, query)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return db.Query(query, args...)
	}
	return st.Query(args...)
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
