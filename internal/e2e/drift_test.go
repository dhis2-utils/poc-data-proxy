package e2e

import (
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// The hub's copy follows central's (review R-28): an online read with a narrow fields
// list keeps the stored person and program of an enrollment, and a pull's whole event
// replaces the data values, so a value cleared on central goes from the hub too.
func TestCopyFollowsCentral(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	h := e.app.Hub
	u, err := h.Store.UserByName("nurse_a")
	if err != nil {
		t.Fatal(err)
	}
	if o := h.Index.Get("SeedEn00001"); o == nil || o.TE != "SeedTe00001" {
		t.Fatalf("seeded enrollment not pulled: %+v", o)
	}

	// GET /api/tracker/enrollments/<E>?fields=enrollment,orgUnit,status, with a change.
	narrow := tracker.M{"enrollments": []any{tracker.M{"enrollment": "SeedEn00001", "orgUnit": fc.Facility, "status": "COMPLETED"}}}
	if _, err := h.UpsertCentral(u.ID, narrow, false); err != nil {
		t.Fatal(err)
	}
	if o := h.Index.Get("SeedEn00001"); o == nil || o.TE != "SeedTe00001" || o.Program != fc.Program {
		t.Errorf("after a narrow read: %+v, want person and program kept", o)
	}
	narrowEv := tracker.M{"events": []any{tracker.M{"event": "SeedEv00001", "status": "COMPLETED"}}}
	if _, err := h.UpsertCentral(u.ID, narrowEv, false); err != nil {
		t.Fatal(err)
	}
	if o := h.Index.Get("SeedEv00001"); o == nil || o.TE != "SeedTe00001" || o.Enrollment != "SeedEn00001" ||
		o.Program != fc.Program || o.Stage != fc.Stage1 {
		t.Errorf("event after a narrow read: %+v, want parents kept", o)
	}

	ev := func(updated string, dvs ...any) tracker.M {
		return tracker.M{"events": []any{tracker.M{"event": "SeedEv00001", "enrollment": "SeedEn00001", "trackedEntity": "SeedTe00001",
			"program": fc.Program, "programStage": fc.Stage1, "orgUnit": fc.Facility, "status": "ACTIVE",
			"occurredAt": "2026-10-07T00:00:00.000", "createdAt": "2026-10-07T00:00:00.000", "updatedAt": updated, "dataValues": dvs}}}
	}
	values := func() []any {
		o := h.Index.Get("SeedEv00001")
		dv, _ := h.Index.Full(o)["dataValues"].([]any)
		return dv
	}
	if _, err := h.UpsertCentral(u.ID, ev("2026-11-01T00:00:00.000", tracker.M{"dataElement": "DeWeight001", "value": "70"}), true); err != nil {
		t.Fatal(err)
	}
	if dv := values(); len(dv) != 1 {
		t.Fatalf("data values %v, want one", dv)
	}
	// An online read naming other fields keeps the values.
	if _, err := h.UpsertCentral(u.ID, tracker.M{"events": []any{tracker.M{"event": "SeedEv00001", "status": "ACTIVE",
		"dataValues": []any{}}}}, false); err != nil {
		t.Fatal(err)
	}
	if dv := values(); len(dv) != 1 {
		t.Errorf("after an online read: data values %v, want kept", dv)
	}
	// The pull's whole event without the value: cleared on central.
	if _, err := h.UpsertCentral(u.ID, ev("2026-11-02T00:00:00.000"), true); err != nil {
		t.Fatal(err)
	}
	if dv := values(); len(dv) != 0 {
		t.Errorf("after a pull: data values %v, want none", dv)
	}
}

// A completed enrollment saved online carries no update time until the next pull
// (the client's is stripped); retention must not take it for an old one and keep only
// a stub of it.
func TestNoTimeIsNotOld(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	h := e.app.Hub
	u, err := h.Store.UserByName("nurse_a")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Store.SetState("retention", []byte(`{"defaultMonths":12}`)); err != nil {
		t.Fatal(err)
	}
	enr := tracker.M{"enrollments": []any{tracker.M{"enrollment": "NoTimeEn001", "trackedEntity": "SeedTe00001", "program": fc.Program,
		"orgUnit": fc.Facility, "status": "COMPLETED", "enrolledAt": "2026-10-07", "occurredAt": "2026-10-07"}}}
	if _, err := h.UpsertSaved(u.ID, enr, map[string]bool{"NoTimeEn001": true}); err != nil {
		t.Fatal(err)
	}
	if o := h.Index.Get("NoTimeEn001"); o == nil || o.Stub {
		t.Errorf("enrollment saved online: %+v, want stored whole", o)
	}
}
