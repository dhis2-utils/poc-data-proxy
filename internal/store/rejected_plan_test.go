package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// The check every online read of a record makes (trackerapi.ownRefusedRecord) finds
// the record's refused objects through indexes, with no scan of the objects table.
func TestRecordHasRejectedUsesIndexes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, err := hubcrypto.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DB().Query("EXPLAIN QUERY PLAN "+sqlRecordRejected, "Te000000001", "Te000000001", SyncRejected)
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
	if p := strings.Join(plan, "; "); strings.Contains(p, "SCAN objects") || !strings.Contains(p, "objects_te") {
		t.Errorf("plan: %s", p)
	}
	o := obj(1)
	o.Slim, o.SyncState = []byte(`{}`), SyncRejected
	if err := s.PutObject(s.DB(), o); err != nil {
		t.Fatal(err)
	}
	if !s.RecordHasRejected(o.TE) || !s.RecordHasRejected(o.UID) || s.RecordHasRejected("TeOther0001") {
		t.Error("RecordHasRejected: wrong answer for the person, the event or another person")
	}
}
