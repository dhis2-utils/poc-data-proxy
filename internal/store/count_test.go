package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// The hub's status page, its heartbeat and every sync cycle count the pending and
// refused objects. Without an index the count read every object with its payload: at
// 30 000 people a status request took 9-15 s here and about a minute on a 2-CPU
// Windows PC (stress test, 2026-10-21). It reads only the unsynced objects.
func TestCountObjectsReadsOnlyUnsynced(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{SyncPending, SyncRejected} {
		rows, err := s.DB().Query("EXPLAIN QUERY PLAN "+sqlCountObjects, state)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			rows.Scan(&id, &parent, &notused, &detail)
			plan = append(plan, detail)
		}
		rows.Close()
		if p := strings.Join(plan, " | "); !strings.Contains(p, "objects_unsynced") {
			t.Errorf("counting %s objects does not read the unsynced index: %s", state, p)
		}
	}

	tx, _ := s.DB().Begin()
	for i, st := range []string{SyncSynced, SyncSynced, SyncPending, SyncRejected, SyncPending} {
		o := &Object{UID: "Ob" + strings.Repeat("x", 8) + string(rune('a'+i)), Kind: KindEvent, Origin: OriginCentral,
			SyncState: st, Slot: int64(i + 1), Slim: []byte(`{}`), Payload: []byte(`{}`)}
		if err := s.PutObject(tx, o); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
	if p, r := s.CountObjects(SyncPending), s.CountObjects(SyncRejected); p != 2 || r != 1 {
		t.Errorf("counts: %d pending, %d refused; want 2 and 1", p, r)
	}
}
