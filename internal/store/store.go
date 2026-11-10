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
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	// NewDataFile lets an unlock create a data file although key slots exist: a hub
	// restored from its USB stick, whose cached data central sends again.
	NewDataFile bool
}

// keyMigrations are applied to the key file in order; PRAGMA user_version records
// how many ran. Never edit a released migration; append a new one.
var keyMigrations = []string{
	// 1: identity and mode, and a key slot per user (the slot itself verifies a password).
	`
CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE key_slots(
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  kek_salt BLOB NOT NULL,
  wrapped_dk BLOB,
  needs_login INTEGER NOT NULL DEFAULT 0
);
`,
	// 2: when a user was first marked as needing an online login, so their saves are
	// refused after a while (sync, loginWait). A new column with a constant default:
	// existing rows are not rewritten; a user marked before gets the time at the next
	// refusal of their token.
	`ALTER TABLE key_slots ADD COLUMN needs_login_at TEXT NOT NULL DEFAULT '';`,
}

// dataMigrations are applied to the data file, the same way. A migration that
// touches existing rows (a new index, a rewritten column) is timed at the hc4-5y
// size before it is committed (TestMigrationOnFullStore, AGENTS.md).
var dataMigrations = []string{
	// 1: each user's visibility is a bitmap over objects' slots (visibility_bits).
	// Queries read the indexed columns, each object's slim JSON (what the remaining
	// filters read) and a tracked entity's attribute values (te_attrs) instead of an
	// in-memory copy of every object (docs/PERFORMANCE.md, "How the hub scales").
	//
	// The list indexes are not partial: a query with a bound value cannot use an index
	// restricted to non-empty values. A person's and an enrollment's children are
	// indexed with their kind, so that index wins over the list indexes, which also
	// start with kind (without statistics the planner chose those, and storing a person
	// walked every enrollment). Event lists come in the orders the apps ask for,
	// Android's download (creation) and working lists (event date, newest first): with
	// 150 000 events in a program, sorting per page cost 0.5 to 1.6 s.
	//
	// deleted: a tombstone of what central deleted, for Android's download
	// (includeDeleted=true). stub: an enrollment pruned by tracker retention, hidden
	// from reads and kept for the enrol-once check. objects_stubs is partial so that
	// building it on an existing hub sorts nothing: rebuilding objects_state with the
	// stub flag instead ran out of SQLite's memory (4 GB, sorts in memory) at 4.6
	// million objects (docs/PERFORMANCE.md).
	//
	// outbox.params: the import flags a client sent with an offline write, sent along
	// on push. agg_forms: central's answer per aggregate form
	// (docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md).
	// users.stick_slot: each user's key slot with the password alone, for the USB
	// stick's copy of the key file, whose own slots are bound to the device key
	// (internal/devkey).
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
  last_login_at TEXT NOT NULL DEFAULT '',
  stick_slot BLOB
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
  payload_enc BLOB NOT NULL,
  slot INTEGER,
  created_at TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  te_created TEXT NOT NULL DEFAULT '',
  rel_from TEXT NOT NULL DEFAULT '',
  rel_to TEXT NOT NULL DEFAULT '',
  slim TEXT,
  occurred_at TEXT NOT NULL DEFAULT '',
  deleted INTEGER NOT NULL DEFAULT 0,
  stub INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX objects_slot ON objects(slot);
CREATE INDEX objects_te ON objects(te_uid, kind);
CREATE INDEX objects_enrollment ON objects(enrollment_uid, kind);
CREATE INDEX objects_rel_from ON objects(rel_from);
CREATE INDEX objects_rel_to ON objects(rel_to);
CREATE INDEX objects_type ON objects(kind, tet, owner_org_unit, created_at, uid DESC);
CREATE INDEX objects_type_desc ON objects(kind, tet, owner_org_unit, created_at DESC, uid DESC);
CREATE INDEX objects_program ON objects(kind, program, owner_org_unit, te_created, te_uid DESC);
CREATE INDEX objects_program_desc ON objects(kind, program, owner_org_unit, te_created DESC, te_uid DESC);
CREATE INDEX objects_events ON objects(kind, program, owner_org_unit, created_at, uid DESC);
CREATE INDEX objects_events_desc ON objects(kind, program, owner_org_unit, created_at DESC, uid DESC);
CREATE INDEX objects_events_occurred ON objects(kind, program, owner_org_unit, occurred_at, created_at DESC, uid DESC);
CREATE INDEX objects_events_occurred_desc ON objects(kind, program, owner_org_unit, occurred_at DESC, created_at DESC, uid DESC);
CREATE INDEX objects_updated ON objects(updated_at, kind, uid, te_uid);
CREATE INDEX objects_state ON objects(uid, updated_at, slot);
CREATE INDEX objects_local ON objects(kind, uid, te_uid) WHERE origin = 'local';
CREATE INDEX objects_stubs ON objects(uid) WHERE stub = 1;
CREATE TABLE visibility_bits(user_id INTEGER NOT NULL, chunk INTEGER NOT NULL, bits BLOB NOT NULL,
  PRIMARY KEY(user_id, chunk));
CREATE TABLE te_scopes(te_uid TEXT NOT NULL, scope TEXT NOT NULL, PRIMARY KEY(te_uid, scope));
CREATE TABLE te_attrs(te_uid TEXT NOT NULL, attr TEXT NOT NULL, value TEXT NOT NULL, value_lc TEXT NOT NULL,
  PRIMARY KEY(te_uid, attr));
CREATE INDEX te_attrs_value ON te_attrs(attr, value);
CREATE TABLE outbox(
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL,
  payload_enc BLOB NOT NULL,
  created_at TEXT NOT NULL,
  state TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  last_error_enc BLOB,
  depends_on_seq INTEGER,
  params TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'tracker'
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
CREATE TABLE agg_forms(
  key TEXT PRIMARY KEY,
  data_set TEXT NOT NULL,
  period TEXT NOT NULL,
  org_unit TEXT NOT NULL,
  aoc TEXT NOT NULL,
  body_enc BLOB NOT NULL,
  reg TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL,
  stale INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX agg_forms_ds ON agg_forms(data_set, fetched_at);
`,
	// 2: the unsynced objects alone, for counting pending and refused ones (status page,
	// heartbeat, each sync cycle) without reading every object (stress test on Windows,
	// 2026-10-21). Only unsynced rows are in it, so it stays small.
	`CREATE INDEX objects_unsynced ON objects(sync_state) WHERE sync_state <> 'synced';`,
	// 3: when an outbox entry first got an answer that was not an import report, so
	// such an entry is refused after a day instead of retried for ever (push.go). A
	// new column with a constant default: existing rows are not rewritten.
	`ALTER TABLE outbox ADD COLUMN failing_since TEXT NOT NULL DEFAULT '';`,
	// 4: when central accepted an outbox entry, so sent entries leave the outbox
	// SentOutboxKept later (PruneSentOutbox). A new column with a constant default:
	// existing rows are not rewritten.
	`ALTER TABLE outbox ADD COLUMN sent_at TEXT NOT NULL DEFAULT '';`,
	// 5: cached responses the hub loaded for a user at their login rather than for a
	// request of theirs (proxy/replay.go): only requests clients made are loaded for
	// others, so one seen once is not repeated for ever. A new column with a constant
	// default: existing rows are not rewritten.
	`ALTER TABLE http_cache ADD COLUMN replayed INTEGER NOT NULL DEFAULT 0;`,
	// 6: a search by an attribute value finds its few matches from this index rather
	// than by reading every enrollment of the program (tracker.searchCandidates): a name
	// nobody has took 151 ms at 100 000 people, growing with the facility (2026-11-05).
	`CREATE INDEX te_attrs_lc ON te_attrs(attr, value_lc, te_uid);`,
}

// DataPath is the data file that belongs to the key file at path.
func DataPath(path string) string { return path + ".data" }

// Open opens (and migrates) the key file at path. The store starts locked.
func Open(path string) (*Store, error) {
	// SQLite's temporary files (sorts that spill, as migrations do) go next to the
	// store, on its disk: the system's temp directory may be in RAM. They are
	// encrypted by the xts VFS. An operator's SQLITE_TMPDIR wins.
	if d := os.Getenv("SQLITE_TMPDIR"); d == "" || !dirExists(d) {
		tmp := filepath.Join(filepath.Dir(path), "tmp")
		if err := os.MkdirAll(tmp, 0o700); err != nil {
			return nil, err
		}
		os.Setenv("SQLITE_TMPDIR", tmp)
	}
	keys, err := sql.Open("sqlite3", "file:"+(&url.URL{Path: path}).EscapedPath()+
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(FULL)&_pragma=secure_delete(1)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	keys.SetMaxOpenConns(4)
	s := &Store{Keys: keys, Path: path}
	if err := migrate(keys, keyMigrations, false); err != nil {
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
	if err := migrate(db, dataMigrations, true); err != nil {
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

// migrate runs the migrations not yet applied, one transaction each, on one
// connection. With spill (the data file), that connection sorts through temporary
// files instead of memory while it migrates: a migration over millions of existing rows (a new index) needs more than
// SQLite's 4 GB of memory otherwise, and a hub that cannot migrate cannot upgrade
// (docs/PERFORMANCE.md, "Upgrading a large hub"). The xts VFS encrypts temporary files
// with a random key, so no plaintext reaches the disk either way.
func migrate(db *sql.DB, migrations []string, spill bool) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var v int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("store schema %d is newer than this binary (%d)", v, len(migrations))
	}
	if v == len(migrations) {
		return nil
	}
	if spill {
		if _, err := conn.ExecContext(ctx, `PRAGMA temp_store=file`); err != nil {
			return err
		}
		defer conn.ExecContext(ctx, `PRAGMA temp_store=memory`)
	}
	for i := v; i < len(migrations); i++ {
		tx, err := conn.BeginTx(ctx, nil)
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
	if err := migrate(db, dataMigrations, true); err != nil {
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
	// The driver takes the key only in the URI, and SQLite quotes the URI in some
	// errors, which the upgrade logs (review R-24).
	if _, err := s.DB().Exec(`VACUUM INTO ?`, dataDSN(DataPath(dst))+"&hexkey="+key); err != nil {
		return fmt.Errorf("data file: %s", strings.ReplaceAll(err.Error(), key, "..."))
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
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && s.hasSlots() && !s.NewDataFile {
		return fmt.Errorf("data file %s is missing", path)
	}
	db, err := openData(path, k)
	if _, serr := os.Stat(path); err != nil && serr == nil && !s.hasSlots() {
		// A set-up that stopped before its first key slot left a data file whose key
		// was only in memory: nothing can open it, and set-up failed on it for ever
		// (review R-09). It holds nothing that is not on central; start a new one.
		slog.Warn("removing a data file no key slot opens, left by an interrupted set-up", "err", err)
		if err = removeData(path); err == nil {
			db, err = openData(path, k)
		}
	}
	if err != nil {
		return err
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
		db.Close()
		return fmt.Errorf("data file does not open with this key: %w", err)
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
	_, err := s.write(`INSERT INTO kv(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
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
	_, err = s.write(`INSERT INTO state(key,value_enc) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value_enc=excluded.value_enc`, key, ct)
	return err
}

// Wipe deletes everything (reset hub, spec 6.2): the data file and the key file's
// rows. The store is locked afterwards.
func (s *Store) Wipe() error {
	s.lock()
	if err := removeData(DataPath(s.Path)); err != nil {
		return err
	}
	if _, err := s.Keys.Exec(`DELETE FROM meta; DELETE FROM key_slots`); err != nil {
		return err
	}
	return s.vacuumKeys()
}

// removeData deletes a data file with its write-ahead log.
func removeData(path string) error {
	for _, sfx := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + sfx); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
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

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
