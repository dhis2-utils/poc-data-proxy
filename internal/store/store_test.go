package store

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

func init() { hubcrypto.ArgonMemory, hubcrypto.ArgonTime = 8*1024, 1 }

func mustKey(t *testing.T) hubcrypto.Key {
	k, err := hubcrypto.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// filesContain reports which of the store's files contain needle in plain text.
func filesContain(t *testing.T, dir, needle string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	var out []string
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if bytes.Contains(b, []byte(needle)) {
			out = append(out, filepath.Base(f))
		}
	}
	return out
}

// Review H1: a key file from before the split (schema 1, everything in plaintext
// index columns) moves its data into the encrypted data file on the first unlock, and
// keeps nothing of it.
func TestLegacyStoreMovesToDataFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")
	dk := mustKey(t)

	// Build a schema-1 store as the previous release wrote it.
	db, err := sql.Open("sqlite3", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, keyMigrations[:1]); err != nil {
		t.Fatal(err)
	}
	salt, _ := hubcrypto.RandomBytes(16)
	wrapped, _ := hubcrypto.WrapDK(dk, "pw", salt)
	payload, _ := hubcrypto.Seal(dk, []byte(`{"trackedEntity":"LegacyTe001"}`), []byte(objAAD("LegacyTe001")))
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO meta(key, value) VALUES('hub_id','H1'), ('last_push','2026-01-01T00:00:00Z')`, nil},
		{`INSERT INTO users(id, username, user_uid, pw_hash, kek_salt, wrapped_dk, last_login_at) VALUES(7,'nurse','UserUid0001','x',?,?,'2026-01-02')`,
			[]any{salt, wrapped}},
		{`INSERT INTO objects(uid, kind, program, owner_org_unit, origin, sync_state, payload_enc)
 VALUES('LegacyTe001','trackedEntity','HivProgram1','FacilityOu1','central','synced',?)`, []any{payload}},
		{`INSERT INTO visibility(object_uid, user_id) VALUES('LegacyTe001', 7)`, nil},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(q.sql, err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.UserByName("nurse")
	if err != nil || u == nil || u.ID != 7 || u.UserUID != "" {
		t.Fatalf("slot before unlock: %+v %v", u, err)
	}
	if err := s.SetDK(dk); err != nil {
		t.Fatal(err)
	}
	o, err := s.GetObject("LegacyTe001")
	if err != nil || o == nil || o.Program != "HivProgram1" || !bytes.Contains(o.Payload, []byte("LegacyTe001")) {
		t.Fatalf("object after move: %+v %v", o, err)
	}
	if u, _ := s.UserByName("nurse"); u.UserUID != "UserUid0001" || u.LastLoginAt != "2026-01-02" {
		t.Fatalf("user after move: %+v", u)
	}
	if s.Private("last_push") == "" || s.Meta("last_push") != "" || s.Meta("hub_id") != "H1" {
		t.Fatal("meta not split")
	}
	// Checked while the store is open: closing checkpoints the WAL and would hide
	// old pages left in the main file.
	for _, needle := range []string{"LegacyTe001", "HivProgram1", "FacilityOu1", "UserUid0001", "2026-01-02"} {
		if f := filesContain(t, dir, needle); len(f) > 0 {
			t.Fatalf("%q still in plain text in %v", needle, f)
		}
	}
	s.SetDK(nil)
	s.Close()

	// The data file opens again, and not with another key.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDK(mustKey(t)); err == nil {
		t.Fatal("data file opened with the wrong key")
	}
	if err := s.SetDK(dk); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.GetObject("LegacyTe001"); o == nil {
		t.Fatal("object lost on reopen")
	}

	// A backup is encrypted too, and opens with the same key.
	if err := s.BackupTo(filepath.Join(dir, "copy.db")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if f := filesContain(t, dir, "LegacyTe001"); len(f) > 0 {
		t.Fatalf("backup in plain text: %v", f)
	}
	c, err := Open(filepath.Join(dir, "copy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetDK(dk); err != nil {
		t.Fatal(err)
	}
	if o, _ := c.GetObject("LegacyTe001"); o == nil || c.Meta("hub_id") != "H1" {
		t.Fatal("backup incomplete")
	}
}

// While locked the data file is closed: reads find nothing and writes fail.
func TestLockedStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := s.CountObjects(SyncPending); n != 0 {
		t.Fatal(n)
	}
	if err := s.SetPrivate("x", "y"); err == nil {
		t.Fatal("write accepted while locked")
	}
}

// Unused key slots are removed, but never the most recently used ones.
func TestPruneKeySlots(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dk := mustKey(t)
	if err := s.SetDK(dk); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"a", "b", "c", "d"} {
		salt, _ := hubcrypto.RandomBytes(16)
		wrapped, _ := hubcrypto.WrapDK(dk, name, salt)
		id, err := s.UpsertCredentials(name, "", salt, wrapped)
		if err != nil {
			t.Fatal(err)
		}
		when := []string{"2020-01-01T00:00:00Z", Now(), "2020-01-02T00:00:00Z", "2020-01-03T00:00:00Z"}[i]
		s.DB().Exec(`UPDATE users SET last_login_at=? WHERE id=?`, when, id)
	}
	pruned, err := s.PruneKeySlots(24*time.Hour, 2)
	if err != nil || len(pruned) != 2 || pruned[0] != "c" || pruned[1] != "a" {
		t.Fatalf("pruned %v %v", pruned, err)
	}
	if u, _ := s.UserByName("d"); len(u.WrappedDK) == 0 {
		t.Fatal("kept slot removed")
	}
}
