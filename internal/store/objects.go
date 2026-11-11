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
	// SyncSuperseded: a refused object whose version on central changed after the
	// refusal. The hub's copy follows central's, and the refusal stays open, with its
	// values in the outbox entry, until a hub manager resends or discards it (review R-22).
	SyncSuperseded = "superseded"
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
	// Deleted: a tombstone, deleted on central (the slim JSON's deleted).
	Deleted bool
	// Stub: an enrollment kept as a stub after pruning (tracker retention; the slim
	// JSON's hubStub).
	Stub    bool
	Attrs   []Attr // a tracked entity's attribute values
	Payload []byte
}

// Attr is one attribute value of a tracked entity, as queries search it.
type Attr struct{ Attr, Value string }

// The writes run inside transactions. They are prepared when the store unlocks
// (prepareWrites): preparing takes a connection of its own, which a transaction must
// not wait for while other writers hold the rest of the pool waiting for its lock.
const (
	sqlPutObject = `INSERT INTO objects(uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at,
 origin, sync_state, payload_enc, slot, created_at, status, rel_from, rel_to, slim, te_created, occurred_at, deleted, stub)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,
 CASE WHEN ?2 = 'enrollment' THEN coalesce((SELECT created_at FROM objects WHERE uid = ?3 AND kind = 'trackedEntity'), '') ELSE '' END,
 CASE WHEN ?2 = 'event' THEN coalesce(json_extract(?18, '$.occurredAt'), '') ELSE '' END,
 coalesce(json_extract(?18, '$.deleted'), 0), coalesce(json_extract(?18, '$.hubStub'), 0))
ON CONFLICT(uid) DO UPDATE SET kind=excluded.kind, te_uid=excluded.te_uid, enrollment_uid=excluded.enrollment_uid,
 program=excluded.program, stage=excluded.stage, tet=excluded.tet, owner_org_unit=excluded.owner_org_unit,
 updated_at=excluded.updated_at, origin=excluded.origin, sync_state=excluded.sync_state, payload_enc=excluded.payload_enc,
 slot=coalesce(objects.slot, excluded.slot), created_at=excluded.created_at, status=excluded.status,
 rel_from=excluded.rel_from, rel_to=excluded.rel_to, slim=excluded.slim, te_created=excluded.te_created,
 occurred_at=excluded.occurred_at, deleted=excluded.deleted, stub=excluded.stub`
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

// Pending reports, inside a write, whether the object has a local change waiting
// to be pushed.
func (s *Store) Pending(tx *sql.Tx, uid string) (bool, error) {
	var st string
	err := tx.QueryRow(`SELECT sync_state FROM objects WHERE uid=?`, uid).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return st == SyncPending, err
}

// GetObject returns the object or nil.
func (s *Store) GetObject(uid string) (*Object, error) {
	o := &Object{}
	var ct []byte
	err := s.DB().QueryRow(`SELECT uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at, origin,
 sync_state, payload_enc, slot, stub FROM objects WHERE uid=?`, uid).Scan(&o.UID, &o.Kind, &o.TE, &o.Enrollment, &o.Program,
		&o.Stage, &o.TET, &o.OwnerOrgUnit, &o.UpdatedAt, &o.Origin, &o.SyncState, &ct, &o.Slot, &o.Stub)
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
	"updated_at", "origin", "sync_state", "slot", "created_at", "status", "rel_from", "rel_to", "deleted", "stub", "slim"}

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
		&o.UpdatedAt, &o.Origin, &o.SyncState, &o.Slot, &o.CreatedAt, &o.Status, &o.RelFrom, &o.RelTo, &o.Deleted, &o.Stub, &slim}, extra...)...)
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

// Each runs query and calls fn for each row, until fn returns false or ctx ends. fn
// scans the row; it must not query the store, as the row's connection is held until
// Each returns.
func (s *Store) Each(ctx context.Context, query string, args []any, fn func(scan func(...any) error) (bool, error)) error {
	db := s.DB()
	st, err := s.stmt(db, query)
	if err != nil {
		return err
	}
	var rows *sql.Rows
	if st == nil {
		rows, err = db.QueryContext(ctx, query, args...)
	} else {
		rows, err = st.QueryContext(ctx, args...)
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if more, err := fn(rows.Scan); err != nil || !more {
			return err
		}
	}
	return rows.Err()
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
			&b.UpdatedAt, &b.Origin, &b.SyncState, &b.Slot, &b.CreatedAt, &b.Status, &b.RelFrom, &b.RelTo, &b.Deleted, &b.Stub, &slim)
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
// and the visibility slot; Stub marks an enrollment kept as a stub (tracker
// retention), under which a pull counts what central lists as held.
type ObjectState struct {
	UpdatedAt string
	Slot      int64
	Stub      bool
}

// ObjectStates returns the state of these stored objects, by uid (missing ones are
// left out). It reads indexes only, in uid order (objects_state, and objects_stubs for
// the stub flag): a pull asks this for everything central lists, and reading table rows
// in central's order was one random page read per object, which an SD card answers
// slowly.
func (s *Store) ObjectStates(uids []string) (map[string]ObjectState, error) {
	sorted := slices.Clone(uids)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	out := make(map[string]ObjectState, len(sorted))
	err := inBatches(sorted, func(args []any, marks string) error {
		rows, err := s.query(sqlObjectStates(marks), args...)
		return scanRows(rows, err, func(r *sql.Rows) error {
			var uid string
			var st ObjectState
			err := r.Scan(&uid, &st.UpdatedAt, &st.Slot, &st.Stub)
			out[uid] = st
			return err
		})
	})
	return out, err
}

// sqlObjectStates reads the state index and, for the stub flag, the partial index of
// stubs: no table rows.
func sqlObjectStates(marks string) string {
	return `SELECT o.uid, o.updated_at, o.slot, EXISTS (SELECT 1 FROM objects s INDEXED BY objects_stubs WHERE s.uid = o.uid AND s.stub = 1)
 FROM objects o INDEXED BY objects_state WHERE o.uid IN (` + marks + `)`
}

// SlotOrigin is an object's origin, program and update time, read by slot.
type SlotOrigin struct {
	Origin, Program, UpdatedAt string
}

// SlotOrigins returns the origin, program and update time of the objects in these
// slots, by slot.
func (s *Store) SlotOrigins(slots []int64) (map[int64]SlotOrigin, error) {
	out := make(map[int64]SlotOrigin, len(slots))
	err := inBatches(slots, func(args []any, marks string) error {
		rows, err := s.query(`SELECT slot, origin, program, updated_at FROM objects WHERE slot IN (`+marks+`)`, args...)
		return scanRows(rows, err, func(r *sql.Rows) error {
			var slot int64
			var so SlotOrigin
			err := r.Scan(&slot, &so.Origin, &so.Program, &so.UpdatedAt)
			out[slot] = so
			return err
		})
	})
	return out, err
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

// VisibilityChunkBits is how many slots one stored chunk of a user's visibility
// bitmap covers (512 bytes).
const VisibilityChunkBits = 4096

// VisibilityChunks returns each user's visibility bitmap chunks: user -> chunk -> bits
// (little-endian 64-bit words).
func (s *Store) VisibilityChunks() (map[int64]map[int64][]byte, error) {
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
	s.DB().QueryRow(`SELECT max(coalesce((SELECT max(slot) FROM objects), 0),
 coalesce((SELECT CAST(value AS INTEGER) FROM kv WHERE key = 'slot_floor'), 0))`).Scan(&n)
	return n.Int64
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

// sqlCountObjects counts the objects in an unsynced state (pending, rejected) from the
// partial index objects_unsynced; the second condition is what lets SQLite use it.
const sqlCountObjects = `SELECT count(*) FROM objects WHERE sync_state=? AND sync_state <> 'synced'`

func (s *Store) CountObjects(state string) int {
	var n int
	s.DB().QueryRow(sqlCountObjects, state).Scan(&n)
	return n
}

// OldProgramEvents lists the facility's synced events of an event program (no person)
// whose event date (or, without one, creation date) is before cutoff and that no
// pending outbox entry refers to. It reads the event-date index.
func (s *Store) OldProgramEvents(program, owner, cutoff string) ([]string, error) {
	rows, err := s.DB().Query(`SELECT uid FROM objects WHERE kind = ? AND program = ? AND owner_org_unit = ? AND te_uid = ''
 AND sync_state = ? AND ((occurred_at <> '' AND occurred_at < ?) OR (occurred_at = '' AND created_at < ?))
 AND NOT EXISTS (SELECT 1 FROM outbox o JOIN outbox_objects oo ON oo.seq = o.seq WHERE o.state = ? AND oo.uid = objects.uid)`,
		KindEvent, program, owner, SyncSynced, cutoff, cutoff, OutboxPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if rows.Scan(&u) == nil {
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

// DeleteObjects removes synced objects (pruning), with a tracked entity's attribute
// values and scopes, a thousand per transaction so that
// offline writes are not held up; a row that turned pending meanwhile stays. The
// highest slot removed is kept as a floor for MaxSlot: users' visibility bitmaps keep
// the removed slots' bits, so a restart must never give those slots to new objects.
func (s *Store) DeleteObjects(uids []string) (int, error) {
	n := 0
	for start := 0; start < len(uids); start += 1000 {
		chunk := uids[start:min(start+1000, len(uids))]
		err := s.Tx(context.Background(), func(tx *sql.Tx) error {
			for _, u := range chunk {
				var slot sql.NullInt64
				var kind string
				err := tx.QueryRow(`DELETE FROM objects WHERE uid = ? AND sync_state = ? RETURNING slot, kind`, u, SyncSynced).Scan(&slot, &kind)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				n++
				if kind == KindTrackedEntity { // its attribute values and scopes go with it
					if _, err := tx.Exec(`DELETE FROM te_attrs WHERE te_uid = ?`, u); err != nil {
						return err
					}
					if _, err := tx.Exec(`DELETE FROM te_scopes WHERE te_uid = ?`, u); err != nil {
						return err
					}
				}
				if _, err := tx.Exec(`INSERT INTO kv(key, value) VALUES('slot_floor', ?)
 ON CONFLICT(key) DO UPDATE SET value = CAST(max(CAST(value AS INTEGER), CAST(excluded.value AS INTEGER)) AS TEXT)`,
					fmt.Sprint(slot.Int64)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// notInPendingOutbox is the condition that no pending outbox entry names the row
// aliased x; its one argument is OutboxPending.
func notInPendingOutbox(x string) string {
	return ` AND NOT EXISTS (SELECT 1 FROM outbox o JOIN outbox_objects oo ON oo.seq = o.seq WHERE o.state = ? AND oo.uid = ` + x + `.uid)`
}

// stubbable is the condition, without arguments, that nothing pending refers to the
// enrollment aliased x: no pending outbox entry names it, and no unsynced event under
// it or relationship with it. Stubbing checks it twice: when listing, and again in
// the transaction that writes the stub, since an offline edit may come in between.
func stubbable(x string) string {
	return ` AND NOT EXISTS (SELECT 1 FROM outbox o JOIN outbox_objects oo ON oo.seq = o.seq WHERE o.state = '` + OutboxPending + `' AND oo.uid = ` + x + `.uid)
 AND NOT EXISTS (SELECT 1 FROM objects ev WHERE ev.enrollment_uid = ` + x + `.uid AND ev.sync_state <> '` + SyncSynced + `')` +
		notReferenced(x)
}

// notReferenced is the condition, without arguments, that no unsynced relationship
// has the row aliased x as an end.
func notReferenced(x string) string {
	return ` AND NOT EXISTS (SELECT 1 FROM objects r WHERE r.rel_from = ` + x + `.uid AND r.sync_state <> '` + SyncSynced + `')
 AND NOT EXISTS (SELECT 1 FROM objects r WHERE r.rel_to = ` + x + `.uid AND r.sync_state <> '` + SyncSynced + `')`
}

// AgedEnrollments lists the facility's synced, closed (completed or cancelled)
// enrollments of a program, not yet stubs, whose last change is before cutoff (a
// date) and that nothing pending refers to (stubbable): what tracker retention turns
// into stubs.
func (s *Store) AgedEnrollments(program, owner, cutoff string) ([]string, error) {
	return s.uids(sqlAgedEnrollments, KindEnrollment, program, owner, SyncSynced, cutoff)
}

var sqlAgedEnrollments = `SELECT uid FROM objects e WHERE kind = ? AND program = ? AND owner_org_unit = ? AND sync_state = ?
 AND stub = 0 AND status IN ('COMPLETED', 'CANCELLED') AND updated_at < ?` + stubbable("e")

// sqlStubEvents lists the synced events under stubs that nothing pending refers to,
// read from the stubs (objects_stubs): it runs each sync cycle.
var sqlStubEvents = `SELECT ev.uid FROM objects e INDEXED BY objects_stubs JOIN objects ev ON ev.enrollment_uid = e.uid
 WHERE e.stub = 1 AND ev.kind = ? AND ev.sync_state = ?` + notInPendingOutbox("ev") + notReferenced("ev")

// StubEnrollmentsEvents lists the synced events whose enrollment is a stub and that
// nothing pending refers to: they go once their enrollment is stubbed.
func (s *Store) StubEnrollmentsEvents() ([]string, error) {
	return s.uids(sqlStubEvents, KindEvent, SyncSynced, OutboxPending)
}

// PrunableEntities lists the facility's synced tracked entities of a type whose last
// change is before cutoff, with no enrollment held in full, that no pending outbox
// entry names and that no unsynced object refers to (as its tracked entity or as a
// relationship's end).
func (s *Store) PrunableEntities(tet, owner, cutoff string) ([]string, error) {
	return s.uids(sqlPrunableEntities, KindTrackedEntity, tet, owner, SyncSynced, cutoff, OutboxPending, KindEnrollment, SyncSynced, SyncSynced, SyncSynced)
}

var sqlPrunableEntities = `SELECT uid FROM objects t WHERE kind = ? AND tet = ? AND owner_org_unit = ? AND sync_state = ?
 AND updated_at < ?` + notInPendingOutbox("t") + `
 AND NOT EXISTS (SELECT 1 FROM objects x WHERE x.te_uid = t.uid AND x.kind = ? AND x.stub = 0)
 AND NOT EXISTS (SELECT 1 FROM objects x WHERE x.te_uid = t.uid AND x.sync_state <> ?)
 AND NOT EXISTS (SELECT 1 FROM objects x WHERE x.rel_from = t.uid AND x.sync_state <> ?)
 AND NOT EXISTS (SELECT 1 FROM objects x WHERE x.rel_to = t.uid AND x.sync_state <> ?)`

// EntityLeftovers lists what goes with these tracked entities when they are pruned:
// their remaining rows (stubs, events under them) and relationships with them.
func (s *Store) EntityLeftovers(tes []string) ([]string, error) {
	var out []string
	err := inBatches(tes, func(args []any, marks string) error {
		got, err := s.uids(sqlEntityLeftovers(marks), append(append(slices.Clone(args), args...), args...)...)
		out = append(out, got...)
		return err
	})
	return out, err
}

// OrphanRelationships lists, after pruning deleted these objects, the synced
// relationships with one of them as an end that is really gone: an object that turned
// pending meanwhile was kept, and so is its relationship.
func (s *Store) OrphanRelationships(uids []string) ([]string, error) {
	var out []string
	err := inBatches(uids, func(args []any, marks string) error {
		got, err := s.uids(sqlOrphanRelationships(marks), append(slices.Clone(args), args...)...)
		out = append(out, got...)
		return err
	})
	return out, err
}

func sqlEntityLeftovers(marks string) string {
	return `SELECT uid FROM objects WHERE te_uid IN (` + marks + `)
 UNION SELECT uid FROM objects WHERE rel_from IN (` + marks + `)
 UNION SELECT uid FROM objects WHERE rel_to IN (` + marks + `)`
}

func sqlOrphanRelationships(marks string) string {
	return `SELECT uid FROM objects r WHERE rel_from IN (` + marks + `) AND sync_state = '` + SyncSynced + `'
 AND NOT EXISTS (SELECT 1 FROM objects o WHERE o.uid = r.rel_from)
 UNION SELECT uid FROM objects r WHERE rel_to IN (` + marks + `) AND sync_state = '` + SyncSynced + `'
 AND NOT EXISTS (SELECT 1 FROM objects o WHERE o.uid = r.rel_to)`
}

// uids runs a query of one uid column.
func (s *Store) uids(query string, args ...any) ([]string, error) {
	rows, err := s.DB().Query(query, args...)
	var out []string
	err = scanRows(rows, err, func(r *sql.Rows) error {
		var u string
		err := r.Scan(&u)
		out = append(out, u)
		return err
	})
	return out, err
}

// StubEnrollments rewrites these synced enrollments as stubs (tracker retention): stub
// returns the new payload and slim JSON from the old payload. A thousand per
// transaction, as DeleteObjects; an enrollment that something pending refers to by
// now stays (stubbable).
func (s *Store) StubEnrollments(uids []string, stub func(payload []byte) ([]byte, []byte, error)) (int, error) {
	n := 0
	for start := 0; start < len(uids); start += 1000 {
		chunk := uids[start:min(start+1000, len(uids))]
		err := s.Tx(context.Background(), func(tx *sql.Tx) error {
			for _, u := range chunk {
				var ct []byte
				err := tx.QueryRow(`SELECT payload_enc FROM objects x WHERE uid = ? AND sync_state = ? AND stub = 0`+stubbable("x"),
					u, SyncSynced).Scan(&ct)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				old, err := s.OpenBlob(objAAD(u), ct)
				if err != nil {
					return err
				}
				payload, slim, err := stub(old)
				if err != nil {
					return err
				}
				sealed, err := s.Seal(objAAD(u), payload)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(`UPDATE objects SET payload_enc = ?1, slim = ?2, stub = coalesce(json_extract(?2, '$.hubStub'), 0)
 WHERE uid = ?3 AND sync_state = ?4`, sealed, string(slim), u, SyncSynced); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
