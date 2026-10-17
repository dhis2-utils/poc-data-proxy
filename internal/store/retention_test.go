package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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

	// The events under stubs are found from the stubs, not by walking every event: the
	// query runs each sync cycle.
	rows, err := s.DB().Query("EXPLAIN QUERY PLAN "+sqlStubEvents, KindEvent, SyncSynced, OutboxPending)
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
	if !strings.Contains(strings.Join(plan, " | "), "objects_stubs") {
		t.Errorf("events under stubs not read from the stubs: %s", strings.Join(plan, " | "))
	}

	// Stubbing re-checks what may have turned pending since the aged list was read: an
	// unsynced event under the enrollment keeps it.
	put("EnOld000002", KindEnrollment, "TeOld000002", "", "", `{"status":"COMPLETED"}`)
	slot++
	if err := s.PutObject(s.DB(), &Object{UID: "EvPend00001", Kind: KindEvent, TE: "TeOld000002", Enrollment: "EnOld000002",
		Program: prog, OwnerOrgUnit: ou, Origin: OriginLocal, SyncState: "pending", Slot: slot, Slim: []byte(`{}`), Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	n, err := s.StubEnrollments([]string{"EnOld000002"}, func(p []byte) ([]byte, []byte, error) {
		return []byte(`{"hubStub":true}`), []byte(`{"hubStub":true}`), nil
	})
	if err != nil || n != 0 {
		t.Errorf("an enrollment with a pending event stubbed: %d %v", n, err)
	}

	// A pending relationship naming the aged enrollment keeps it.
	slot++
	if err := s.PutObject(s.DB(), &Object{UID: "RelPend0001", Kind: KindRelationship, RelFrom: "EnOld000001", RelTo: "TeAct000001",
		OwnerOrgUnit: ou, Origin: OriginLocal, SyncState: "pending", Slot: slot, Slim: []byte(`{}`), Payload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AgedEnrollments(prog, ou, cutoff); len(got) != 0 {
		t.Fatalf("enrollment named by a pending relationship aged: %v", got)
	}
	s.DB().Exec(`DELETE FROM objects WHERE uid = 'RelPend0001'`)

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

// The pruning queries read indexes, never a whole table: they run each sync cycle on
// hubs with millions of rows.
func TestRetentionQueryPlans(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	scan := regexp.MustCompile(`SCAN [a-z]+( |$)`)
	for _, q := range []struct {
		name, sql string
		args      []any
	}{
		{"aged enrollments", sqlAgedEnrollments, []any{KindEnrollment, "p", "o", SyncSynced, "2025-01-01"}},
		{"events under stubs", sqlStubEvents, []any{KindEvent, SyncSynced, OutboxPending}},
		{"prunable entities", sqlPrunableEntities, []any{KindTrackedEntity, "t", "o", SyncSynced, "2025-01-01", OutboxPending, KindEnrollment, SyncSynced, SyncSynced, SyncSynced}},
		{"entity leftovers", sqlEntityLeftovers("?"), []any{"a", "a", "a"}},
		{"orphan relationships", sqlOrphanRelationships("?"), []any{"a", "a"}},
		{"object states", sqlObjectStates("?,?"), []any{"a", "b"}},
	} {
		rows, err := s.DB().Query("EXPLAIN QUERY PLAN "+q.sql, q.args...)
		if err != nil {
			t.Fatalf("%s: %v", q.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			rows.Scan(&id, &parent, &notused, &detail)
			plan = append(plan, detail)
		}
		rows.Close()
		if p := strings.Join(plan, " | "); scan.MatchString(p) {
			t.Errorf("%s scans a table: %s", q.name, p)
		}
	}
}

// Review minors: after pruning deletes events, only relationships whose end is really
// gone go with them; an event that turned pending meanwhile stayed, and so does its
// relationship.
func TestOrphanRelationships(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	k, _ := hubcrypto.NewKey()
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	slot := int64(0)
	put := func(o *Object) {
		t.Helper()
		slot++
		o.Slot, o.OwnerOrgUnit, o.Origin, o.Slim, o.Payload = slot, "OuAbcdefghi", OriginCentral, []byte(`{}`), []byte(`{}`)
		if o.SyncState == "" {
			o.SyncState = SyncSynced
		}
		if err := s.PutObject(s.DB(), o); err != nil {
			t.Fatal(err)
		}
	}
	put(&Object{UID: "EvKept00001", Kind: KindEvent, SyncState: SyncPending}) // turned pending: not deleted
	put(&Object{UID: "RelGone0001", Kind: KindRelationship, RelFrom: "EvGone00001", RelTo: "TeAbcdefgh1"})
	put(&Object{UID: "RelKept0001", Kind: KindRelationship, RelFrom: "EvKept00001", RelTo: "TeAbcdefgh1"})
	got, err := s.OrphanRelationships([]string{"EvGone00001", "EvKept00001"})
	if err != nil || !slices.Equal(got, []string{"RelGone0001"}) {
		t.Fatalf("orphans: %v %v", got, err)
	}
}
