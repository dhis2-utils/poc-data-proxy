// Package store is the hub's SQLite store (spec 9.2), in two files:
//
//   - the key file (hub.db): hub identity and mode (meta) and one key slot per user.
//     It is plaintext, because it is read while the store is locked: a slot holds the
//     data key (DK) wrapped under a key derived from the user's password.
//   - the data file (hub.db.data): everything else, encrypted page by page with
//     AES-256-XTS under a key derived from DK. It is open only while unlocked.
//
// Encrypting the whole data file, and not only payload columns, matters: which
// patients are enrolled in which program, their visit dates, which staff see whom,
// and rows SQLite has freed all live outside the payloads (review H1). Payloads,
// PATs, access lists and cached bodies are additionally sealed per row with
// AES-256-GCM, whose per-row AAD guards against rows being swapped.
//
// The store is locked at boot: DK is only in memory after a known user logs in and
// unwraps their key slot (spec 9.1).
package store

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/xts"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

var ErrLocked = errors.New("store is locked: a known user must log in")

type Store struct {
	Keys *sql.DB // the key file: meta and key slots
	Path string

	mu    sync.RWMutex
	dk    hubcrypto.Key
	data  *sql.DB // the encrypted data file, while unlocked
	stmts stmtCache
	// writeMu serialises write transactions (Tx).
	writeMu sync.Mutex
	empty   *sql.DB // stands in for the data file while locked: empty and read-only
}

// keyMigrations are applied to the key file in order; PRAGMA user_version records
// how many ran. Never edit a released migration; append a new one.
var keyMigrations = []string{
	// 1: the original single-file schema. Its data tables move to the data file on
	// the first unlock after an upgrade (moveLegacy).
	`
CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE state(key TEXT PRIMARY KEY, value_enc BLOB NOT NULL);
CREATE TABLE users(
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  user_uid TEXT NOT NULL DEFAULT '',
  pw_hash TEXT NOT NULL,
  kek_salt BLOB NOT NULL,
  wrapped_dk BLOB,
  pat_enc BLOB,
  pat_uid TEXT NOT NULL DEFAULT '',
  pat_expires TEXT NOT NULL DEFAULT '',
  pw_enc BLOB,
  access_json_enc BLOB,
  access_refreshed_at TEXT NOT NULL DEFAULT '',
  last_login_at TEXT NOT NULL DEFAULT '',
  needs_login INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE objects(
  uid TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  te_uid TEXT NOT NULL DEFAULT '',
  enrollment_uid TEXT NOT NULL DEFAULT '',
  program TEXT NOT NULL DEFAULT '',
  stage TEXT NOT NULL DEFAULT '',
  tet TEXT NOT NULL DEFAULT '',
  owner_org_unit TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL,
  sync_state TEXT NOT NULL,
  payload_enc BLOB NOT NULL
);
CREATE INDEX objects_te ON objects(te_uid);
CREATE TABLE visibility(object_uid TEXT NOT NULL, user_id INTEGER NOT NULL, PRIMARY KEY(object_uid, user_id));
CREATE TABLE outbox(
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  payload_enc BLOB NOT NULL,
  created_at TEXT NOT NULL,
  state TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error_enc BLOB,
  depends_on_seq INTEGER
);
CREATE TABLE outbox_objects(seq INTEGER NOT NULL, uid TEXT NOT NULL, kind TEXT NOT NULL, PRIMARY KEY(seq, uid));
CREATE INDEX outbox_objects_uid ON outbox_objects(uid);
CREATE TABLE http_cache(
  user_id INTEGER NOT NULL,
  method TEXT NOT NULL,
  url TEXT NOT NULL,
  status INTEGER NOT NULL,
  headers_enc BLOB NOT NULL,
  body_enc BLOB NOT NULL,
  fetched_at TEXT NOT NULL,
  PRIMARY KEY(user_id, method, url)
);
CREATE TABLE issues(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  outbox_seq INTEGER NOT NULL,
  object_uid TEXT NOT NULL,
  kind TEXT NOT NULL,
  user_id INTEGER NOT NULL,
  error_enc BLOB NOT NULL,
  created_at TEXT NOT NULL,
  reported_central_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE pull_state(user_id INTEGER NOT NULL, program TEXT NOT NULL, last_pull_at TEXT NOT NULL DEFAULT '',
  last_full_at TEXT NOT NULL DEFAULT '', PRIMARY KEY(user_id, program));
CREATE TABLE reserved_values(id INTEGER PRIMARY KEY AUTOINCREMENT, attribute TEXT NOT NULL, value_enc BLOB NOT NULL,
  expires_at TEXT NOT NULL DEFAULT '');
CREATE INDEX reserved_values_attr ON reserved_values(attribute);
`,
	// 2: key slots without the password hash (the slot itself verifies a password).
	`
CREATE TABLE key_slots(
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  kek_salt BLOB NOT NULL,
  wrapped_dk BLOB,
  needs_login INTEGER NOT NULL DEFAULT 0
);
INSERT INTO key_slots(id, username, kek_salt, wrapped_dk, needs_login)
  SELECT id, username, kek_salt, wrapped_dk, needs_login FROM users;
`,
}

// dataMigrations are applied to the data file, the same way.
var dataMigrations = []string{
	`
CREATE TABLE kv(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE state(key TEXT PRIMARY KEY, value_enc BLOB NOT NULL);
CREATE TABLE users(
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL,
  user_uid TEXT NOT NULL DEFAULT '',
  pat_enc BLOB,
  pat_uid TEXT NOT NULL DEFAULT '',
  pat_expires TEXT NOT NULL DEFAULT '',
  pw_enc BLOB,
  access_json_enc BLOB,
  access_refreshed_at TEXT NOT NULL DEFAULT '',
  last_login_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE objects(
  uid TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  te_uid TEXT NOT NULL DEFAULT '',
  enrollment_uid TEXT NOT NULL DEFAULT '',
  program TEXT NOT NULL DEFAULT '',
  stage TEXT NOT NULL DEFAULT '',
  tet TEXT NOT NULL DEFAULT '',
  owner_org_unit TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL,
  sync_state TEXT NOT NULL,
  payload_enc BLOB NOT NULL
);
CREATE INDEX objects_te ON objects(te_uid);
CREATE TABLE visibility(object_uid TEXT NOT NULL, user_id INTEGER NOT NULL, PRIMARY KEY(object_uid, user_id));
CREATE TABLE outbox(
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  payload_enc BLOB NOT NULL,
  created_at TEXT NOT NULL,
  state TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error_enc BLOB,
  depends_on_seq INTEGER
);
CREATE TABLE outbox_objects(seq INTEGER NOT NULL, uid TEXT NOT NULL, kind TEXT NOT NULL, PRIMARY KEY(seq, uid));
CREATE INDEX outbox_objects_uid ON outbox_objects(uid);
CREATE TABLE http_cache(
  user_id INTEGER NOT NULL,
  method TEXT NOT NULL,
  url TEXT NOT NULL,
  status INTEGER NOT NULL,
  headers_enc BLOB NOT NULL,
  body_enc BLOB NOT NULL,
  fetched_at TEXT NOT NULL,
  PRIMARY KEY(user_id, method, url)
);
CREATE TABLE issues(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  outbox_seq INTEGER NOT NULL,
  object_uid TEXT NOT NULL,
  kind TEXT NOT NULL,
  user_id INTEGER NOT NULL,
  error_enc BLOB NOT NULL,
  created_at TEXT NOT NULL,
  reported_central_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE pull_state(user_id INTEGER NOT NULL, program TEXT NOT NULL, last_pull_at TEXT NOT NULL DEFAULT '',
  last_full_at TEXT NOT NULL DEFAULT '', PRIMARY KEY(user_id, program));
CREATE TABLE reserved_values(id INTEGER PRIMARY KEY AUTOINCREMENT, attribute TEXT NOT NULL, value_enc BLOB NOT NULL,
  expires_at TEXT NOT NULL DEFAULT '');
CREATE INDEX reserved_values_attr ON reserved_values(attribute);
`,
	// Visibility as per-user bitmaps over object slots instead of a row per object and
	// user (docs/PERFORMANCE.md). Rows of the old table are converted on first load.
	`
ALTER TABLE objects ADD COLUMN slot INTEGER;
UPDATE objects SET slot = rowid;
CREATE UNIQUE INDEX objects_slot ON objects(slot);
CREATE TABLE visibility_bits(user_id INTEGER NOT NULL, chunk INTEGER NOT NULL, bits BLOB NOT NULL,
  PRIMARY KEY(user_id, chunk));
CREATE TABLE te_scopes(te_uid TEXT NOT NULL, scope TEXT NOT NULL, PRIMARY KEY(te_uid, scope));
`,
	// Queries read the data file instead of an in-memory copy of every object
	// (docs/PERFORMANCE.md, "The query index in SQLite"): the columns lists filter and
	// sort on, each object's slim JSON (what the remaining filters read), and a tracked
	// entity's attribute values. Rows of schema 2 have slim NULL until the first unlock
	// fills them (tracker.Index.Load). The indexes are not partial: a query with a
	// bound value cannot use an index restricted to non-empty values. A person's and an
	// enrollment's children are indexed with their kind, so that index wins over the
	// list indexes, which also start with kind (without statistics the planner chose
	// those, and storing a person walked every enrollment).
	`
ALTER TABLE objects ADD COLUMN created_at TEXT NOT NULL DEFAULT '';
ALTER TABLE objects ADD COLUMN status TEXT NOT NULL DEFAULT '';
ALTER TABLE objects ADD COLUMN te_created TEXT NOT NULL DEFAULT '';
ALTER TABLE objects ADD COLUMN rel_from TEXT NOT NULL DEFAULT '';
ALTER TABLE objects ADD COLUMN rel_to TEXT NOT NULL DEFAULT '';
ALTER TABLE objects ADD COLUMN slim TEXT;
DROP INDEX objects_te;
CREATE INDEX objects_te ON objects(te_uid, kind);
CREATE INDEX objects_enrollment ON objects(enrollment_uid, kind);
CREATE INDEX objects_rel_from ON objects(rel_from);
CREATE INDEX objects_rel_to ON objects(rel_to);
CREATE INDEX objects_type ON objects(kind, tet, owner_org_unit, created_at, uid);
CREATE INDEX objects_program ON objects(kind, program, owner_org_unit, te_created, te_uid);
CREATE INDEX objects_program_desc ON objects(kind, program, owner_org_unit, te_created DESC, te_uid);
CREATE INDEX objects_unfilled ON objects(uid) WHERE slim IS NULL;
CREATE INDEX objects_updated ON objects(updated_at, kind, uid, te_uid);
CREATE INDEX objects_state ON objects(uid, updated_at, slot);
CREATE INDEX objects_local ON objects(kind, uid, te_uid) WHERE origin = 'local';
CREATE TABLE te_attrs(te_uid TEXT NOT NULL, attr TEXT NOT NULL, value TEXT NOT NULL, value_lc TEXT NOT NULL,
  PRIMARY KEY(te_uid, attr));
CREATE INDEX te_attrs_value ON te_attrs(attr, value);
`,
}

// SchemaVersion is the number of key file migrations this binary knows.
func SchemaVersion() int { return len(keyMigrations) }

// DataPath is the data file that belongs to the key file at path.
func DataPath(path string) string { return path + ".data" }

// Open opens (and migrates) the key file at path. The store starts locked.
func Open(path string) (*Store, error) {
	keys, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath()+
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(FULL)&_pragma=secure_delete(1)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	keys.SetMaxOpenConns(4)
	s := &Store{Keys: keys, Path: path}
	if err := migrate(keys, keyMigrations); err != nil {
		keys.Close()
		return nil, err
	}
	if s.empty, err = emptyData(); err != nil {
		keys.Close()
		return nil, err
	}
	return s, nil
}

// emptyData is an in-memory database with the data schema, read-only: while the
// store is locked, reads find nothing and writes fail.
func emptyData() (*sql.DB, error) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(0)
	if err := migrate(db, dataMigrations); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA query_only=1`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *Store) Close() error {
	s.lock()
	s.empty.Close()
	return s.Keys.Close()
}

// DB is the data file, or an empty read-only stand-in while locked.
func (s *Store) DB() *sql.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data != nil {
		return s.data
	}
	return s.empty
}

// migrate applies pending migrations in one transaction each.
func migrate(db *sql.DB, migrations []string) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("store schema %d is newer than this binary (%d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// fileKey derives the data file key (AES-256-XTS takes 64 bytes) from DK.
func fileKey(dk hubcrypto.Key) (string, error) {
	k, err := hkdf.Key(sha256.New, dk, nil, "facility-hub data file aes-256-xts", 64)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(k), nil
}

func dataDSN(path string) string { return "file:" + (&url.URL{Path: path}).EscapedPath() + "?vfs=xts" }

// openData opens (or creates) the encrypted data file at path.
func openData(path string, dk hubcrypto.Key) (*sql.DB, error) {
	key, err := fileKey(dk)
	if err != nil {
		return nil, err
	}
	// The key must be set before anything reads the file, so no _pragma in the DSN.
	// Write transactions take the write lock when they begin (_txlock=immediate): a
	// deferred one that reads first fails at once with "database is locked" when another
	// connection writes meanwhile, whatever the busy timeout (found by cmd/hubload).
	db, err := driver.Open(dataDSN(path)+"&_txlock=immediate", func(c *sqlite3.Conn) error {
		return c.Exec(`PRAGMA hexkey='` + key + `'; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=10000;
PRAGMA synchronous=FULL; PRAGMA temp_store=memory; PRAGMA cache_size=-16384`)
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := migrate(db, dataMigrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("data file: %w", err)
	}
	return db, nil
}

// BackupTo writes a consistent copy of the store: the key file to dst and the data
// file, still encrypted, to DataPath(dst).
func (s *Store) BackupTo(dst string) error {
	dk, err := s.DK()
	if err != nil {
		return err
	}
	key, err := fileKey(dk)
	if err != nil {
		return err
	}
	os.Remove(DataPath(dst))
	if _, err := s.DB().Exec(`VACUUM INTO ?`, dataDSN(DataPath(dst))+"&hexkey="+key); err != nil {
		return fmt.Errorf("data file: %w", err)
	}
	_, err = s.Keys.Exec(`VACUUM INTO ?`, dst)
	return err
}

// --- locking --------------------------------------------------------------

func (s *Store) Unlocked() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dk != nil
}

// SetDK unlocks the store with the data key: it opens the data file (creating it at
// setup) and runs its migrations. Passing nil locks the store.
func (s *Store) SetDK(k hubcrypto.Key) error {
	if k == nil {
		s.lock()
		return nil
	}
	if s.Unlocked() {
		return nil
	}
	path := DataPath(s.Path)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && s.hasSlots() && !s.hasLegacy() {
		return fmt.Errorf("data file %s is missing", path)
	}
	db, err := openData(path, k)
	if err != nil {
		return err
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
		db.Close()
		return fmt.Errorf("data file does not open with this key: %w", err)
	}
	if err := s.moveLegacy(db); err != nil {
		db.Close()
		return fmt.Errorf("moving data out of the key file: %w", err)
	}
	if err := s.prepareWrites(db); err != nil {
		db.Close()
		return fmt.Errorf("preparing statements: %w", err)
	}
	s.mu.Lock()
	s.dk, s.data = k, db
	s.mu.Unlock()
	return nil
}

func (s *Store) lock() {
	s.mu.Lock()
	db := s.data
	s.dk, s.data = nil, nil
	s.mu.Unlock()
	if db != nil {
		db.Close()
	}
}

func (s *Store) hasSlots() bool {
	var n int
	s.Keys.QueryRow(`SELECT count(*) FROM key_slots`).Scan(&n)
	return n > 0
}

// legacyTables held data in the key file before it was split (key migration 1).
var legacyTables = []string{"state", "objects", "visibility", "outbox", "outbox_objects", "http_cache", "issues",
	"pull_state", "reserved_values"}

func (s *Store) hasLegacy() bool {
	var n int
	s.Keys.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='objects'`).Scan(&n)
	return n > 0
}

// moveLegacy copies the data tables of a key file from before the split into the data
// file, then drops them from the key file and vacuums it, so no plaintext stays
// behind. It is idempotent: a crash between the two steps repeats the copy.
func (s *Store) moveLegacy(data *sql.DB) error {
	if !s.hasLegacy() {
		return nil
	}
	ctx := context.Background()
	conn, err := data.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// The key file uses the default VFS, not the data file's.
	if _, err := conn.ExecContext(ctx, `ATTACH DATABASE ? AS legacy`,
		"file:"+(&url.URL{Path: s.Path}).EscapedPath()+"?vfs=os"); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, `DETACH DATABASE legacy`)
	stmts := []string{`INSERT OR REPLACE INTO main.users(id, username, user_uid, pat_enc, pat_uid, pat_expires, pw_enc,
 access_json_enc, access_refreshed_at, last_login_at) SELECT id, username, user_uid, pat_enc, pat_uid, pat_expires,
 pw_enc, access_json_enc, access_refreshed_at, last_login_at FROM legacy.users`,
		`INSERT OR REPLACE INTO main.kv(key, value) SELECT key, value FROM legacy.meta WHERE key IN ('last_push', 'last_pull')`}
	for _, t := range legacyTables {
		if t == "objects" { // the data file's objects table has the slot column too
			stmts = append(stmts, `INSERT OR REPLACE INTO main.objects(uid, kind, te_uid, enrollment_uid, program, stage, tet,
 owner_org_unit, updated_at, origin, sync_state, payload_enc) SELECT uid, kind, te_uid, enrollment_uid, program, stage, tet,
 owner_org_unit, updated_at, origin, sync_state, payload_enc FROM legacy.objects`,
				`UPDATE main.objects SET slot = rowid WHERE slot IS NULL`)
			continue
		}
		stmts = append(stmts, fmt.Sprintf(`INSERT OR REPLACE INTO main.%s SELECT * FROM legacy.%s`, t, t))
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	conn.ExecContext(ctx, `DETACH DATABASE legacy`)
	drop := `DELETE FROM meta WHERE key IN ('last_push', 'last_pull'); DROP TABLE users;`
	for _, t := range legacyTables {
		drop += ` DROP TABLE ` + t + `;`
	}
	if _, err := s.Keys.Exec(drop); err != nil {
		return err
	}
	return s.vacuumKeys()
}

// vacuumKeys rebuilds the key file and checkpoints at once: in WAL mode the
// vacuumed file sits in the WAL, and the old pages stay readable in the main file
// until a checkpoint overwrites and truncates it.
func (s *Store) vacuumKeys() error {
	if _, err := s.Keys.Exec(`VACUUM`); err != nil {
		return err
	}
	_, err := s.Keys.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// DK returns the in-memory data key, or ErrLocked.
func (s *Store) DK() (hubcrypto.Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.dk == nil {
		return nil, ErrLocked
	}
	return s.dk, nil
}

func (s *Store) Seal(aad string, plaintext []byte) ([]byte, error) {
	k, err := s.DK()
	if err != nil {
		return nil, err
	}
	return hubcrypto.Seal(k, plaintext, []byte(aad))
}

func (s *Store) OpenBlob(aad string, ct []byte) ([]byte, error) {
	k, err := s.DK()
	if err != nil {
		return nil, err
	}
	return hubcrypto.Open(k, ct, []byte(aad))
}

// --- meta (key file, readable while locked) --------------------------------
// Only what the hub needs before unlock belongs here: identity, mode, central URL
// and version, upgrade state.

func (s *Store) Meta(key string) string {
	var v string
	s.Keys.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	return v
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.Keys.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Private and SetPrivate are plain values in the data file (sync times and the like).
func (s *Store) Private(key string) string {
	var v string
	s.DB().QueryRow(`SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	return v
}

func (s *Store) SetPrivate(key, value string) error {
	_, err := s.DB().Exec(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// --- encrypted state ------------------------------------------------------

func (s *Store) State(key string) ([]byte, error) {
	var ct []byte
	err := s.DB().QueryRow(`SELECT value_enc FROM state WHERE key=?`, key).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.OpenBlob("state:"+key, ct)
}

func (s *Store) SetState(key string, value []byte) error {
	ct, err := s.Seal("state:"+key, value)
	if err != nil {
		return err
	}
	_, err = s.DB().Exec(`INSERT INTO state(key,value_enc) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value_enc=excluded.value_enc`, key, ct)
	return err
}

// Wipe deletes everything (reset hub, spec 6.2): the data file and the key file's
// rows. The store is locked afterwards.
func (s *Store) Wipe() error {
	s.lock()
	for _, sfx := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(DataPath(s.Path) + sfx); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := s.Keys.Exec(`DELETE FROM meta; DELETE FROM key_slots`); err != nil {
		return err
	}
	return s.vacuumKeys()
}

func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func ParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Tx runs fn in a transaction on the data file.
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	// One write transaction at a time, queued here rather than in SQLite: its busy
	// handler polls, and on a starved CPU (the pi4 hammer run) writers gave up after
	// the busy timeout with "database is locked" while others held the lock.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
