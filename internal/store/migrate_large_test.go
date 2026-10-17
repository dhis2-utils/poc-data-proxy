package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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
	if fi, err := os.Stat(filepath.Join(dir, "tmp")); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("temp folder: %v %v", fi, err)
	}
}

// Review gap: the tracker retention migrations run on rows written by the schema
// before them, and the stub flag reads as false for every existing row.
func TestRetentionMigrationFromOldSchema(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	before := len(dataMigrations) - 2 // without the stub column and the index of stubs
	if err := migrate(db, dataMigrations[:before], true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO objects(uid, kind, te_uid, enrollment_uid, program, stage, tet, owner_org_unit, updated_at,
 origin, sync_state, payload_enc, slot, created_at, status, rel_from, rel_to, slim)
 VALUES('EnOld000001','enrollment','TeOld000001','','PrgAbcdefgh','','','OuAbcdefghi','2024-01-01T00:00:00.000',
 'central','synced',x'00',1,'','COMPLETED','','','{"status":"COMPLETED"}')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, dataMigrations, true); err != nil {
		t.Fatal(err)
	}
	var stub int
	if err := db.QueryRow(`SELECT stub FROM objects WHERE uid = 'EnOld000001'`).Scan(&stub); err != nil || stub != 0 {
		t.Fatalf("stub of an existing row: %d %v", stub, err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'objects_stubs'`).Scan(&n)
	if n != 1 {
		t.Fatal("objects_stubs missing after the migration")
	}
	if _, err := db.Exec(`UPDATE objects SET slim = '{"status":"COMPLETED","hubStub":true}', stub = 1 WHERE uid = 'EnOld000001'`); err != nil {
		t.Fatal(err)
	}
	var got int
	if err := db.QueryRow(sqlObjectStates("?"), "EnOld000001").Scan(new(string), new(string), new(int64), &got); err != nil || got != 1 {
		t.Fatalf("the stub flag through objects_stubs: %d %v", got, err)
	}
}
