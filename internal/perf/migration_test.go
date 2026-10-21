package perf

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
)

// TestRetentionMigration times, on a data file of HUB_PERF_MIGRATION objects, building
// the partial index of stubs: what the tracker retention migration did to existing
// hubs before the migrations were squashed, and what a migration adding a partial
// index costs. Rebuilding objects_state with the stub flag, as first written, ran out
// of SQLite's memory at 4.6 million.
func TestRetentionMigration(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("HUB_PERF_MIGRATION"))
	if n == 0 {
		t.Skip(`set HUB_PERF_MIGRATION (e.g. 4600000, the objects of hc4-5y) to measure`)
	}
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < n; i += 10000 {
		tx, err := s.DB().Begin()
		if err != nil {
			t.Fatal(err)
		}
		for j := i; j < min(i+10000, n); j++ {
			uid := fmt.Sprintf("Ev%09d", j)
			slim := []byte(`{"status":"COMPLETED","occurredAt":"2024-03-01T00:00:00.000"}`)
			if err := s.PutObject(tx, &store.Object{UID: uid, Kind: store.KindEvent, TE: fmt.Sprintf("Te%09d", j/10),
				Enrollment: fmt.Sprintf("En%09d", j/3), Program: "PrgAbcdefgh", OwnerOrgUnit: "OuAbcdefghi",
				UpdatedAt: "2024-03-01T00:00:00.000", Origin: store.OriginCentral, SyncState: store.SyncSynced,
				Slot: int64(j + 1), Slim: slim, Payload: slim}); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	fill := time.Since(start)
	var took []string
	for _, q := range []string{
		`DROP INDEX objects_stubs`,
		`CREATE INDEX objects_stubs ON objects(uid) WHERE stub = 1`,
	} {
		t.Logf("%s …", q)
		start := time.Now()
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(q, err)
		}
		took = append(took, fmt.Sprintf("%s: %s", q, time.Since(start).Round(time.Millisecond)))
	}
	t.Logf("%d objects (filled in %s, data file %.0f MB):\n  %s", n, fill.Round(time.Second), dirMB(dir),
		fmt.Sprint(took))
}
