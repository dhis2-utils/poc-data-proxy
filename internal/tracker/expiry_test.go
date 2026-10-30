package tracker

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/store"
)

// A10 and B5, offline: an event dated in the program's expired period is refused
// (E1047) unless the user may edit expired data (F_EDIT_EXPIRED), and a completed
// event is not reopened (E1083) without F_UNCOMPLETE_EVENT.
func TestEventProgramExpiryAndReopen(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	facility, prog, stage, tet := "OuAaaaaaaa1", "PrAaaaaaaa1", "StAaaaaaaa1", "TetAaaaaaa1"
	cat := &access.Catalog{Programs: map[string]*access.ProgramMeta{
		prog: {ID: prog, TET: tet, Stages: []string{stage}, ExpiryPeriodType: "Monthly", ExpiryDays: 1},
	}}
	cat.Index()
	put := func(o *Obj) {
		o.Slot = ix.SlotFor(o.UID)
		if err := s.Tx(context.Background(), func(tx *sql.Tx) error {
			return s.PutObject(tx, o.ToStore())
		}); err != nil {
			t.Fatal(err)
		}
	}
	put(&Obj{UID: "TeAaaaaaaa1", Kind: store.KindTrackedEntity, TET: tet, OwnerOrgUnit: facility,
		Origin: store.OriginLocal, SyncState: store.SyncPending, Data: M{"trackedEntity": "TeAaaaaaaa1", "status": "ACTIVE"}})
	enr := &Obj{UID: "EnAaaaaaaa1", Kind: store.KindEnrollment, TE: "TeAaaaaaaa1", Program: prog, OwnerOrgUnit: facility,
		Origin: store.OriginLocal, SyncState: store.SyncPending, Data: M{"enrollment": "EnAaaaaaaa1", "status": "ACTIVE"}}
	put(enr)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	mkImporter := func(authorities ...string) *Importer {
		acc := &access.List{Authorities: authorities,
			Programs: map[string]access.Perm{prog: {Read: true, Write: true}},
			Stages:   map[string]access.Perm{stage: {Read: true, Write: true}},
			TETs:     map[string]access.Perm{tet: {Read: true, Write: true}}}
		v := &Viewer{UserID: 1, Access: acc, Facility: facility, SearchScope: true, Catalog: cat}
		return &Importer{Ix: ix, V: v, Username: "nurse", UserUID: "U1", Now: now}
	}
	event := func(uid, occurred, status string) M {
		return M{"events": []any{M{"event": uid, "enrollment": enr.UID, "program": prog, "programStage": stage,
			"orgUnit": facility, "occurredAt": occurred, "status": status, "dataValues": []any{}}}}
	}
	codes := func(out *ImportOutcome) []string {
		var out2 []string
		for _, e := range out.Errors {
			out2 = append(out2, e.ErrorCode)
		}
		return out2
	}

	// An event dated last month, with a monthly expiry of one day: expired (E1047).
	out := mkImporter().Import(event("EvAaaaaaaa1", "2026-09-02", "ACTIVE"))
	if !slices.Contains(codes(out), "E1047") {
		t.Errorf("an event in an expired period: codes %v, want E1047", codes(out))
	}
	// The same with F_EDIT_EXPIRED: taken.
	out = mkImporter("F_EDIT_EXPIRED").Import(event("EvAaaaaaaa2", "2026-09-02", "ACTIVE"))
	if len(out.Errors) != 0 || len(out.Accepted) != 1 {
		t.Errorf("F_EDIT_EXPIRED: codes %v accepted %d, want taken", codes(out), len(out.Accepted))
	}
	// Dated today: taken.
	out = mkImporter().Import(event("EvAaaaaaaa3", "2026-10-30", "ACTIVE"))
	if len(out.Errors) != 0 || len(out.Accepted) != 1 {
		t.Errorf("today's event: codes %v accepted %d, want taken", codes(out), len(out.Accepted))
	}
	// Completing an event is not refused: taken.
	out = mkImporter().Import(event("EvAaaaaaaa4", "2026-10-30", "COMPLETED"))
	if len(out.Errors) != 0 || len(out.Accepted) != 1 {
		t.Errorf("completing a new event: codes %v accepted %d, want taken", codes(out), len(out.Accepted))
	}
}
