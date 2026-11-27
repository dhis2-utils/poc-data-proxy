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

// F4, offline: where the program does not allow future dates, an enrollment dated
// after central's today is refused (E1020), and so is an incident date after it
// (E1021), with central's texts. "Today" is the hub's clock in central's time zone.
func TestEnrollmentFutureDates(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	facility, tet := "OuAaaaaaaa1", "TetAaaaaaa1"
	no, yes := false, true
	progs := map[string]*access.ProgramMeta{
		"PrForbids01": {ID: "PrForbids01", TET: tet, SelectEnrollmentDatesInFuture: &no, SelectIncidentDatesInFuture: &no},
		"PrAllows001": {ID: "PrAllows001", TET: tet, SelectEnrollmentDatesInFuture: &yes, SelectIncidentDatesInFuture: &yes},
		"PrOldCat001": {ID: "PrOldCat001", TET: tet}, // a catalog from before the settings
	}
	cat := &access.Catalog{Programs: progs}
	cat.Index()
	te := &Obj{UID: "TeAaaaaaaa1", Kind: store.KindTrackedEntity, TET: tet, OwnerOrgUnit: facility, Slot: ix.SlotFor("TeAaaaaaaa1"),
		Origin: store.OriginLocal, SyncState: store.SyncPending, Data: M{"trackedEntity": "TeAaaaaaaa1"}}
	if err := s.Tx(context.Background(), func(tx *sql.Tx) error { return s.PutObject(tx, te.ToStore()) }); err != nil {
		t.Fatal(err)
	}
	eat, _ := time.LoadLocation("Africa/Nairobi")
	importer := func(now time.Time) *Importer {
		acc := &access.List{Programs: map[string]access.Perm{}, TETs: map[string]access.Perm{tet: {Read: true, Write: true}}}
		for id := range progs {
			acc.Programs[id] = access.Perm{Read: true, Write: true}
		}
		v := &Viewer{UserID: 1, Access: acc, Facility: facility, SearchScope: true, Catalog: cat}
		return &Importer{Ix: ix, V: v, Username: "nurse", UserUID: "U1", Now: now}
	}
	n := 0
	enroll := func(now time.Time, program, enrolled, incident string) *ImportOutcome {
		n++
		e := M{"enrollment": "EnFuture" + string(rune('A'+n)) + "01", "trackedEntity": te.UID, "program": program,
			"orgUnit": facility, "enrolledAt": enrolled, "status": "ACTIVE"}
		if incident != "" {
			e["occurredAt"] = incident
		}
		return importer(now).Import(M{"enrollments": []any{e}})
	}
	noon := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	reports := func(out *ImportOutcome) (codes, msgs []string) {
		for _, e := range out.Errors {
			codes, msgs = append(codes, e.ErrorCode), append(msgs, e.Message)
		}
		return
	}

	codes, msgs := reports(enroll(noon, "PrForbids01", "2026-11-05", "2026-11-04"))
	if !slices.Equal(codes, []string{"E1020"}) || msgs[0] != "Enrollment date: `2026-11-05T00:00:00.000` cannot be a future date." {
		t.Errorf("enrolled tomorrow: %v %q, want E1020 with central's text", codes, msgs)
	}
	codes, msgs = reports(enroll(noon, "PrForbids01", "2026-11-04", "2026-11-05T08:00:00.000"))
	if !slices.Equal(codes, []string{"E1021"}) || msgs[0] != "Incident date: `2026-11-05T08:00:00.000` cannot be a future date." {
		t.Errorf("incident tomorrow: %v %q, want E1021 with central's text", codes, msgs)
	}
	if codes, _ = reports(enroll(noon, "PrForbids01", "2026-11-05", "2026-11-05")); !slices.Equal(codes, []string{"E1020", "E1021"}) {
		t.Errorf("both tomorrow: %v, want E1020 and E1021, as central reports both", codes)
	}
	for _, c := range []struct {
		what, program, date string
		now                 time.Time
	}{
		{"today, late in the day", "PrForbids01", "2026-11-04T23:59:00.000", noon},
		{"a program that allows future dates", "PrAllows001", "2026-11-05", noon},
		{"a catalog without the settings", "PrOldCat001", "2026-11-05", noon},
		// 21:30 UTC is already 5 November in Nairobi (UTC+3): central's today.
		{"central's today in its own time zone", "PrForbids01", "2026-11-05", time.Date(2026, 11, 4, 21, 30, 0, 0, time.UTC).In(eat)},
	} {
		if out := enroll(c.now, c.program, c.date, c.date); len(out.Errors) != 0 || len(out.Accepted) != 1 {
			codes, _ := reports(out)
			t.Errorf("%s: codes %v, want taken", c.what, codes)
		}
	}
}

// With the hub's clock behind central's, the future-date check leaves the dates to
// central (docs/design/2026-11-04-no-rtc.md).
func TestEnrollmentFutureDatesClockBehind(t *testing.T) {
	no := false
	pm := &access.ProgramMeta{SelectEnrollmentDatesInFuture: &no, SelectIncidentDatesInFuture: &no}
	it := &Item{UID: "EnrBehind01", Data: M{"enrolledAt": "2026-11-07", "occurredAt": "2026-11-07"}}
	now := time.Date(2026, 10, 6, 0, 27, 0, 0, time.UTC) // the image's date, after a power cut
	if errs := (&Importer{Now: now}).checkEnrollmentDates(it, nil, pm); len(errs) != 2 {
		t.Fatalf("clock trusted: %d errors, want 2 (E1020, E1021)", len(errs))
	}
	if errs := (&Importer{Now: now, ClockBehind: true}).checkEnrollmentDates(it, nil, pm); len(errs) != 0 {
		t.Errorf("clock behind central's: %v, want none", errs)
	}
}
