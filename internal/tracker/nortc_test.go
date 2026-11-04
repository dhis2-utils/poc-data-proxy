package tracker

import (
	"context"
	"database/sql"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/store"
)

// These tests record what the hub does when its own clock is wrong, as on a Pi 4
// without an RTC started after a power cut with no network (docs/NO-RTC.md). They
// pass on today's code: they describe its behaviour, not a wish. A fix changes them.

// The future-date check (F4) compares the enrollment date with the hub's today. A hub
// behind central refuses today's registrations; a hub ahead lets central's tomorrow
// through, and central refuses it at push.
func TestNoRTCEnrollmentDateCheck(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	facility, tet := "OuAaaaaaaa1", "TetAaaaaaa1"
	no := false
	progs := map[string]*access.ProgramMeta{
		"PrForbids01": {ID: "PrForbids01", TET: tet, SelectEnrollmentDatesInFuture: &no, SelectIncidentDatesInFuture: &no},
	}
	cat := &access.Catalog{Programs: progs}
	cat.Index()
	te := &Obj{UID: "TeAaaaaaaa1", Kind: store.KindTrackedEntity, TET: tet, OwnerOrgUnit: facility, Slot: ix.SlotFor("TeAaaaaaaa1"),
		Origin: store.OriginLocal, SyncState: store.SyncPending, Data: M{"trackedEntity": "TeAaaaaaaa1"}}
	if err := s.Tx(context.Background(), func(tx *sql.Tx) error { return s.PutObject(tx, te.ToStore()) }); err != nil {
		t.Fatal(err)
	}
	n := 0
	enroll := func(hubNow time.Time, date string) []string {
		n++
		acc := &access.List{Programs: map[string]access.Perm{"PrForbids01": {Read: true, Write: true}},
			TETs: map[string]access.Perm{tet: {Read: true, Write: true}}}
		v := &Viewer{UserID: 1, Access: acc, Facility: facility, SearchScope: true, Catalog: cat}
		im := &Importer{Ix: ix, V: v, Username: "nurse", UserUID: "U1", Now: hubNow}
		out := im.Import(M{"enrollments": []any{M{"enrollment": "EnNoRtc" + string(rune('A'+n)) + "001", "trackedEntity": te.UID,
			"program": "PrForbids01", "orgUnit": facility, "enrolledAt": date, "occurredAt": date, "status": "ACTIVE"}}})
		var codes []string
		for _, e := range out.Errors {
			codes = append(codes, e.ErrorCode)
		}
		return codes
	}
	central := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) // the true time, central's
	today, tomorrow := "2026-11-04", "2026-11-05"
	for _, c := range []struct {
		what   string
		hubNow time.Time
		date   string
		want   []string
	}{
		{"right clock, today", central, today, nil},
		{"hours behind, after midnight: today", time.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC).Add(-3 * time.Hour), today, []string{"E1020", "E1021"}},
		{"hours behind, in the day: today", central.Add(-3 * time.Hour), today, nil},
		{"days behind: today", central.AddDate(0, 0, -3), today, []string{"E1020", "E1021"}},
		{"days behind: a week ago", central.AddDate(0, 0, -3), "2026-10-28", nil},
		{"the image's epoch (6 October): today", time.Date(2026, 10, 6, 0, 27, 36, 0, time.UTC), today, []string{"E1020", "E1021"}},
		// Central refuses this one at push (E1020): the staff member learns it later.
		{"a day ahead: central's tomorrow", central.AddDate(0, 0, 1), tomorrow, nil},
	} {
		if got := enroll(c.hubNow, c.date); !slices.Equal(got, c.want) {
			t.Errorf("%s: codes %v, want %v", c.what, got, c.want)
		}
	}
}

// Program expiry (E1047) is applied offline by the hub's clock: behind, the hub takes
// edits central refuses at push; ahead, it refuses edits central would take.
func TestNoRTCEventExpiry(t *testing.T) {
	pm := &access.ProgramMeta{ExpiryPeriodType: "Monthly", ExpiryDays: 5} // October locks after 5 November
	occurred := "2026-10-15T00:00:00.000"
	for _, c := range []struct {
		what        string
		central, at time.Time
		want        bool
	}{
		{"right clock, 8 November", time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC), true},
		{"10 days behind on 8 November", time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC), false},
		{"right clock, 4 November", time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), false},
		{"3 days ahead on 4 November", time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), true},
	} {
		if got := eventExpired(pm, occurred, "", c.at); got != c.want {
			t.Errorf("%s: expired %v, want %v (central: %v)", c.what, got, c.want, eventExpired(pm, occurred, "", c.central))
		}
	}
}

// An object written on the hub counts as updated after Android's watermark by its hub
// write time, at most a day before the watermark (updatedFloor), or always when the
// watermark is more than 38 hours ahead of the hub's clock read as UTC
// (hubClockBehind). Between the two, a hub behind by more than a day and less than 38
// hours (less central's UTC offset) leaves the newest offline writes out of other
// devices' incremental syncs. Here the test's clock is the hub's; central is ahead.
func TestNoRTCIncrementalSyncWindow(t *testing.T) {
	counted := func(behind time.Duration, zone *time.Location) bool {
		hubNow := time.Now()
		// The device synced an hour before the write. Its watermark is central's wall
		// clock then, and central's clock runs ahead of the hub's by behind.
		watermark := hubNow.Add(behind - time.Hour).In(zone).Format(DHIS2Layout)
		stamp := DHIS2Time(hubNow.In(zone)) // the hub's write time (Hub.CentralTime)
		q := url.Values{"updatedAfter": {watermark}}
		floor, err := updatedFloor(q)
		if err != nil {
			t.Fatal(err)
		}
		if hubClockBehind(q) {
			floor = "" // as updatedMatch
		}
		return hubUpdated(stamp, floor)
	}
	kampala, _ := time.LoadLocation("Africa/Kampala") // UTC+3
	for _, c := range []struct {
		what   string
		behind time.Duration
		zone   *time.Location
		want   bool
	}{
		{"10 hours behind, UTC", 10 * time.Hour, time.UTC, true},
		{"23 hours behind, UTC", 23 * time.Hour, time.UTC, true},
		{"26 hours behind, UTC", 26 * time.Hour, time.UTC, false},
		{"36 hours behind, UTC", 36 * time.Hour, time.UTC, false},
		{"40 hours behind, UTC", 40 * time.Hour, time.UTC, true},
		{"34 hours behind, UTC+3", 34 * time.Hour, kampala, false},
		{"37 hours behind, UTC+3", 37 * time.Hour, kampala, true},
		{"four weeks behind, UTC+3", 4 * 7 * 24 * time.Hour, kampala, true},
	} {
		if got := counted(c.behind, c.zone); got != c.want {
			t.Errorf("%s: a write an hour after the watermark counted %v, want %v", c.what, got, c.want)
		}
	}
	// Once the clock is right again, a write made while it was days behind carries
	// the old time and is left out until it comes back from central.
	now := time.Now().UTC()
	q := url.Values{"updatedAfter": {DHIS2Time(now.Add(-time.Hour))}}
	floor, _ := updatedFloor(q)
	if hubClockBehind(q) || hubUpdated(DHIS2Time(now.AddDate(0, 0, -3)), floor) {
		t.Error("a write stamped three days behind counts after the clock is set right")
	}
}
