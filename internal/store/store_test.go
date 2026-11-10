package store

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

// Review H1: what the hub stores is in the encrypted data file only, which opens with
// its key alone; a backup is encrypted the same way.
func TestDataFileEncrypted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")
	dk := mustKey(t)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetDK(dk); err != nil {
		t.Fatal(err)
	}
	s.SetMeta("hub_id", "H1")
	err = s.Tx(context.Background(), func(tx *sql.Tx) error {
		return s.PutObject(tx, &Object{UID: "SecretTe001", Kind: KindTrackedEntity, Program: "HivProgram1",
			OwnerOrgUnit: "FacilityOu1", Origin: OriginCentral, SyncState: SyncSynced, Slot: 1,
			Slim: []byte(`{}`), Payload: []byte(`{"trackedEntity":"SecretTe001"}`)})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Checked while the store is open: closing checkpoints the WAL and would hide
	// pages written only there.
	for _, needle := range []string{"SecretTe001", "HivProgram1", "FacilityOu1"} {
		if f := filesContain(t, dir, needle); len(f) > 0 {
			t.Fatalf("%q in plain text in %v", needle, f)
		}
	}
	// A key slot, as every set-up hub has: a data file no slot opens is removed (R-09).
	if _, err := s.UpsertCredentials("nurse_a", "uid", []byte("salt"), []byte("slot")); err != nil {
		t.Fatal(err)
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
	if o, _ := s.GetObject("SecretTe001"); o == nil || !bytes.Contains(o.Payload, []byte("SecretTe001")) {
		t.Fatal("object lost on reopen")
	}

	// A backup is encrypted too, and opens with the same key.
	if err := s.BackupTo(filepath.Join(dir, "copy.db")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if f := filesContain(t, dir, "SecretTe001"); len(f) > 0 {
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
	if o, _ := c.GetObject("SecretTe001"); o == nil || c.Meta("hub_id") != "H1" {
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
		wrapped, _, _ := hubcrypto.WrapSlot(dk, name, salt, nil)
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

// The hub records when it first marked a user as needing an online login, so their
// saves can be refused after a while (sync, loginWait); an online login clears it.
func TestNeedsLoginSince(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dk := mustKey(t)
	if err := s.SetDK(dk); err != nil {
		t.Fatal(err)
	}
	salt, _ := hubcrypto.RandomBytes(16)
	wrapped, _, _ := hubcrypto.WrapSlot(dk, "a", salt, nil)
	id, err := s.UpsertCredentials("a", "", salt, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	s.SetNeedsLogin(id, true)
	u, _ := s.UserByID(id)
	first := u.NeedsLoginSince
	if !u.NeedsLogin || time.Since(ParseTime(first)) > time.Minute {
		t.Fatalf("needs login %v since %q", u.NeedsLogin, first)
	}
	time.Sleep(2 * time.Millisecond)
	s.SetNeedsLogin(id, true) // refused again: the first time stands
	if u, _ = s.UserByID(id); u.NeedsLoginSince != first {
		t.Fatalf("since %q, want %q", u.NeedsLoginSince, first)
	}
	if _, err := s.UpsertCredentials("a", "", salt, wrapped); err != nil { // an online login
		t.Fatal(err)
	}
	if u, _ = s.UserByID(id); u.NeedsLogin || u.NeedsLoginSince != "" {
		t.Fatalf("after an online login: needs login %v since %q", u.NeedsLogin, u.NeedsLoginSince)
	}
	s.SetNeedsLogin(id, true)
	s.SetNeedsLogin(id, false)
	if u, _ = s.UserByID(id); u.NeedsLogin || u.NeedsLoginSince != "" {
		t.Fatalf("cleared: needs login %v since %q", u.NeedsLogin, u.NeedsLoginSince)
	}
}

// The replay at login loads for a user what clients of any user asked for recently
// and they have no fresh copy of; copies it loaded itself are not sources, so a
// request seen once is not repeated for ever.
func TestReplaySources(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetDK(mustKey(t)); err != nil {
		t.Fatal(err)
	}
	put := func(user int64, url string, replayed bool) {
		if err := s.PutCache(user, "GET", url, &CachedResponse{Status: 200, Body: []byte("{}"), Replayed: replayed}); err != nil {
			t.Fatal(err)
		}
	}
	put(1, "/api/programs", false)
	put(1, "/api/userSettings", false)
	put(2, "/api/optionSets", true)    // loaded for user 2 at their login
	put(0, "/api/loginConfig", false)  // anonymous
	put(3, "/api/userSettings", false) // user 3 has it fresh
	now := time.Now()
	got, err := s.ReplaySources(3, now.Add(-time.Hour), now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "/api/programs" {
		t.Fatalf("sources for user 3: %v", got)
	}
	if got, _ := s.ReplaySources(3, now.Add(time.Hour), now); len(got) != 0 {
		t.Fatalf("not seen since: %v", got)
	}
}
