// Package store is the hub's SQLite store (spec 9.2). Tracker payloads, PATs, access
// lists, cached HTTP bodies and runtime state are encrypted with the data key (DK).
// Index columns hold UIDs and timestamps only.
//
// The store is locked at boot: DK is only in memory after a known user logs in and
// unwraps their key slot (spec 9.1).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

var ErrLocked = errors.New("store is locked: a known user must log in")

type Store struct {
	DB   *sql.DB
	Path string

	mu sync.RWMutex
	dk hubcrypto.Key
}

// migrations are applied in order; PRAGMA user_version records how many ran.
// Never edit a released migration; append a new one.
var migrations = []string{
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
}

// SchemaVersion is the number of migrations this binary knows.
func SchemaVersion() int { return len(migrations) }

// Open opens (and migrates) the store at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	s := &Store{DB: db, Path: path}
	if err := s.Migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// Migrate applies pending migrations in one transaction each.
func (s *Store) Migrate() error {
	var v int
	if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("store schema %d is newer than this binary (%d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.DB.Begin()
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

// BackupTo writes a consistent copy of the store to dst.
func (s *Store) BackupTo(dst string) error {
	_, err := s.DB.Exec(`VACUUM INTO ?`, dst)
	return err
}

// --- locking --------------------------------------------------------------

func (s *Store) Unlocked() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dk != nil
}

// SetDK puts the data key in memory (unlock). Passing nil locks the store.
func (s *Store) SetDK(k hubcrypto.Key) {
	s.mu.Lock()
	s.dk = k
	s.mu.Unlock()
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

// --- meta (plaintext, readable while locked) -------------------------------

func (s *Store) Meta(key string) string {
	var v string
	s.DB.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	return v
}

func (s *Store) SetMeta(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// --- encrypted state ------------------------------------------------------

func (s *Store) State(key string) ([]byte, error) {
	var ct []byte
	err := s.DB.QueryRow(`SELECT value_enc FROM state WHERE key=?`, key).Scan(&ct)
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
	_, err = s.DB.Exec(`INSERT INTO state(key,value_enc) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value_enc=excluded.value_enc`, key, ct)
	return err
}

// Wipe deletes all rows (reset hub, spec 6.2).
func (s *Store) Wipe() error {
	for _, t := range []string{"meta", "state", "users", "objects", "visibility", "outbox", "outbox_objects",
		"http_cache", "issues", "pull_state", "reserved_values"} {
		if _, err := s.DB.Exec(`DELETE FROM ` + t); err != nil {
			return err
		}
	}
	_, err := s.DB.Exec(`VACUUM`)
	return err
}

func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func ParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Tx runs fn in a transaction.
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
