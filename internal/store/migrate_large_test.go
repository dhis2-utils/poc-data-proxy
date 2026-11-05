package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// TestMigrationOnFullStore runs a heavy migration (a full index rebuild, which ran out
// of SQLite's 4 GB with sorts in memory at 4.6 million objects) through the
// migration runner on a store of HUB_PERF_MIGRATION objects (docs/PERFORMANCE.md,
// "Upgrading a large hub").
func TestMigrationOnFullStore(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("HUB_PERF_MIGRATION"))
	if n == 0 {
		t.Skip(`set HUB_PERF_MIGRATION (e.g. 4600000, the objects of hc4-5y) to measure`)
	}
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i += 10000 {
		tx, err := s.DB().Begin()
		if err != nil {
			t.Fatal(err)
		}
		for j := i; j < min(i+10000, n); j++ {
			slim := []byte(`{"status":"COMPLETED","occurredAt":"2024-03-01T00:00:00.000"}`)
			if err := s.PutObject(tx, &Object{UID: fmt.Sprintf("Ev%09d", j), Kind: KindEvent, TE: fmt.Sprintf("Te%09d", j/10),
				Enrollment: fmt.Sprintf("En%09d", j/3), Program: "PrgAbcdefgh", OwnerOrgUnit: "OuAbcdefghi",
				UpdatedAt: "2024-03-01T00:00:00.000", Origin: OriginCentral, SyncState: SyncSynced,
				Slot: int64(j + 1), Slim: slim, Payload: slim}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	heavy := `DROP INDEX objects_state; CREATE INDEX objects_state ON objects(uid, updated_at, slot, stub);`
	start := time.Now()
	if err := migrate(s.DB(), append(append([]string{}, dataMigrations...), heavy), true); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d objects: a full index rebuild as a migration took %s", n, time.Since(start).Round(time.Millisecond))
}

// SQLite's temporary files (sorts during migrations) go next to the data file, on the
// same disk: the system's temp directory may be in RAM (tmpfs), which would bring back
// the memory limit the spilling avoids.
func TestTempFilesBesideTheStore(t *testing.T) {
	t.Setenv("SQLITE_TMPDIR", "")
	os.Unsetenv("SQLITE_TMPDIR")
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, want := os.Getenv("SQLITE_TMPDIR"), filepath.Join(dir, "tmp"); got != want {
		t.Fatalf("SQLITE_TMPDIR %q, want %q", got, want)
	}
	if fi, err := os.Stat(filepath.Join(dir, "tmp")); err != nil || !fi.IsDir() || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700) {
		t.Fatalf("temp folder: %v %v", fi, err)
	}
}

// TestAttrIndexMigrationOnFullStore times migration 6 (te_attrs_lc) on a store with
// HUB_PERF_ATTRS attribute values (hc4-5y: 300 000 people, about 2.4 million).
func TestAttrIndexMigrationOnFullStore(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("HUB_PERF_ATTRS"))
	if n == 0 {
		t.Skip(`set HUB_PERF_ATTRS (e.g. 2400000, the attribute values of hc4-5y) to measure`)
	}
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DROP INDEX te_attrs_lc`); err != nil {
		t.Fatal(err)
	}
	attrs := []string{"AttrFirst01", "AttrLast001", "AttrSex0001", "AttrBirth01", "AttrPhone01", "AttrVillag1", "AttrIdNum01", "AttrNext001"}
	for i := 0; i < n; i += 10000 {
		tx, err := s.DB().Begin()
		if err != nil {
			t.Fatal(err)
		}
		for j := i; j < min(i+10000, n); j++ {
			v := fmt.Sprintf("Value %d of person %d", j%len(attrs), j/len(attrs))
			if _, err := tx.Exec(`INSERT INTO te_attrs(te_uid, attr, value, value_lc) VALUES(?,?,?,?)`,
				fmt.Sprintf("Te%09d", j/len(attrs)), attrs[j%len(attrs)], v, strings.ToLower(v)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if _, err := s.DB().Exec(`PRAGMA user_version = ` + strconv.Itoa(len(dataMigrations)-1)); err != nil {
		t.Fatal(err)
	}
	if err := migrate(s.DB(), dataMigrations, true); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d attribute values: migration 6 (te_attrs_lc) took %s", n, time.Since(start).Round(time.Millisecond))
}
