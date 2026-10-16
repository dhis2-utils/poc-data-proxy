package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// The rows tracker retention prunes by: aged closed enrollments, events under stubs,
// tracked entities of a type with nothing left in full.
func TestRetentionQueries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	const prog, ou, old, cutoff = "PrgAbcdefgh", "OuAbcdefghi", "2024-01-01T00:00:00.000", "2025-10-16"
	slot := int64(0)
	put := func(uid, kind, te, en, tet, slim string) {
		t.Helper()
		slot++
		o := &Object{UID: uid, Kind: kind, TE: te, Enrollment: en, Program: prog, TET: tet, OwnerOrgUnit: ou,
			Origin: OriginCentral, SyncState: SyncSynced, Slot: slot, Slim: []byte(slim), Payload: []byte(slim)}
		if kind == KindEnrollment || kind == KindTrackedEntity {
			o.UpdatedAt = old
		}
		if kind == KindEnrollment {
			if st := map[string]string{"EnOld000001": "COMPLETED", "EnAct000001": "ACTIVE", "EnNew000001": "COMPLETED"}[uid]; st != "" {
				o.Status = st
			}
			if uid == "EnNew000001" {
				o.UpdatedAt = "2026-09-01T00:00:00.000"
			}
		}
		if err := s.PutObject(s.DB(), o); err != nil {
			t.Fatal(err)
		}
	}
	put("TeOld000001", KindTrackedEntity, "", "", "Sample", `{}`)
	put("EnOld000001", KindEnrollment, "TeOld000001", "", "", `{"status":"COMPLETED"}`)
	put("EnAct000001", KindEnrollment, "TeAct000001", "", "", `{"status":"ACTIVE"}`)
	put("EnNew000001", KindEnrollment, "TeNew000001", "", "", `{"status":"COMPLETED"}`)
	put("EnStub00001", KindEnrollment, "TeStb000001", "", "", `{"status":"COMPLETED","hubStub":true}`)
	put("TeStb000001", KindTrackedEntity, "", "", "Sample", `{}`)
	put("EvStub00001", KindEvent, "TeStb000001", "EnStub00001", "", `{}`)

	if got, err := s.AgedEnrollments(prog, ou, cutoff); err != nil || !slices.Equal(got, []string{"EnOld000001"}) {
		t.Fatalf("aged: %v %v", got, err)
	}
	o, err := s.GetObject("EnStub00001")
	if err != nil || !o.Stub {
		t.Fatalf("stub not read: %+v %v", o, err)
	}
	if st, _ := s.ObjectStates([]string{"EnStub00001", "EnOld000001"}); !st["EnStub00001"].Stub || st["EnOld000001"].Stub {
		t.Fatalf("states: %+v", st)
	}
	if got, err := s.StubEnrollmentsEvents(); err != nil || !slices.Equal(got, []string{"EvStub00001"}) {
		t.Fatalf("events under stubs: %v %v", got, err)
	}
	if got, err := s.PrunableEntities("Sample", ou, cutoff); err != nil || !slices.Contains(got, "TeStb000001") || slices.Contains(got, "TeOld000001") {
		t.Fatalf("prunable: %v %v (TeOld000001 still has an enrollment in full)", got, err)
	}
	if got, err := s.EntityLeftovers([]string{"TeStb000001"}); err != nil || !slices.Contains(got, "EnStub00001") {
		t.Fatalf("leftovers: %v %v", got, err)
	}
	put("EnAct000002", KindEnrollment, "TeStb000001", "", "", `{"status":"ACTIVE"}`)
	if got, _ := s.PrunableEntities("Sample", ou, cutoff); slices.Contains(got, "TeStb000001") {
		t.Fatal("an entity with an active enrollment is kept")
	}

	// A pending outbox entry naming the aged enrollment keeps it.
	if err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := s.AppendOutbox(tx, 1, []byte(`{}`), "", []OutboxRef{{UID: "EnOld000001", Kind: KindEnrollment}}, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AgedEnrollments(prog, ou, cutoff); len(got) != 0 {
		t.Fatalf("pending enrollment aged: %v", got)
	}
}
