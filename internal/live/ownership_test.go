package live

import (
	"strings"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Program ownership while the hub holds the data: permanent transfers made on central
// or through the hub, and temporary ownership ("break the glass"). The program is
// protected, so once another facility owns a person, the facility's staff may not
// see their enrollment, on central or on the hub offline.

const seedTE, seedEnr = "SeedTe00001", "SeedEn00001"

func ownershipEnv(t *testing.T) (*tk.Env, *tk.Client) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Protected = true
	return e, e.AndroidUser("nurse_a")
}

func register(t *testing.T, c *tk.Client) {
	t.Helper()
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("%s: %d %s", c.User, st, b)
	}
}

// seesOffline reports whether the user finds the person through the program, opens
// them with the program, or sees their events, on the hub offline.
func seesOffline(t *testing.T, e *tk.Env, c *tk.Client) (search, open, events bool) {
	t.Helper()
	e.Offline()
	defer e.Online()
	_, b := c.Do("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnits="+fc.Facility+"&fields=trackedEntity", nil)
	search = strings.Contains(string(b), seedTE)
	st, _ := c.Do("GET", "/api/tracker/trackedEntities/"+seedTE+"?program="+fc.Program+"&fields=trackedEntity", nil)
	open = st == 200
	_, b = c.Do("GET", "/api/tracker/events?enrollments="+seedEnr+"&fields=event&paging=false", nil)
	events = strings.Contains(string(b), "SeedEv")
	return
}

func TestTransferOnCentral(t *testing.T) {
	e, nurse := ownershipEnv(t)
	e.Setup()
	register(t, nurse)
	e.Sync()
	if s, o, ev := seesOffline(t, e, nurse); !s || !o || !ev {
		t.Fatalf("before the transfer: search %v, open %v, events %v; want all", s, o, ev)
	}
	e.Fake.Transfer(seedTE, fc.OtherFacility)
	e.Sync()
	if s, o, ev := seesOffline(t, e, nurse); s || o || ev {
		t.Errorf("after a transfer out and a sync: search %v, open %v, events %v; want none", s, o, ev)
	}
	e.Fake.Transfer(seedTE, fc.Facility)
	e.Sync()
	if s, o, ev := seesOffline(t, e, nurse); !s || !o || !ev {
		t.Errorf("after a transfer back and a sync: search %v, open %v, events %v; want all", s, o, ev)
	}
}

// A person registered at the facility and transferred away on central before the hub
// first pulls: the facility's staff do not find them through the program, nor see
// their enrollment or events. Opened by UID with the program, the hub answers with no
// enrollment or owner in it (accepted, decided 2026-10-28, docs/FINDINGS.md): it holds
// the person without that enrollment, and cannot tell it from someone not enrolled.
func TestTransferredBeforeSetup(t *testing.T) {
	e, nurse := ownershipEnv(t)
	e.Fake.Transfer(seedTE, fc.OtherFacility)
	e.Setup()
	register(t, nurse)
	e.Sync()
	if s, _, ev := seesOffline(t, e, nurse); s || ev {
		t.Errorf("transferred away before the first pull: search %v, events %v; want neither", s, ev)
	}
	e.Offline()
	defer e.Online()
	if st, b := nurse.Do("GET", "/api/tracker/trackedEntities/"+seedTE+"?program="+fc.Program+"&fields=*", nil); st == 200 &&
		(strings.Contains(string(b), seedEnr) || strings.Contains(string(b), fc.Program)) {
		t.Errorf("opened with the program, the hub shows the enrollment or owner: %s", b)
	}
}

func TestTransferThroughHub(t *testing.T) {
	e, nurse := ownershipEnv(t)
	e.Setup()
	register(t, nurse)
	e.Sync()
	path := "/api/tracker/ownership/transfer?trackedEntity=" + seedTE + "&program=" + fc.Program + "&ou="
	e.Offline()
	if st, b := nurse.Do("PUT", path+fc.OtherFacility, nil); st == 200 {
		t.Errorf("a transfer offline was accepted: %s", b)
	}
	e.Online()
	if st, b := nurse.Do("PUT", path+fc.OtherFacility, nil); st != 200 {
		t.Fatalf("transfer online through the hub: %d %s", st, b)
	}
	if s, o, ev := seesOffline(t, e, nurse); s || o || ev {
		t.Errorf("right after transferring out through the hub: search %v, open %v, events %v; want none", s, o, ev)
	}
}

// TestTransferBackOnCentral: a person transferred out through the hub and back on
// central (C4, docs/ADVERSARIAL-TESTING.md). Central bumps the person's updatedAt at a
// transfer, but a fetch of the person through another scope (here an online read
// without the program; on synthetic 2.42 another user's type pull) takes the bump
// before the program's pull lists them, so that pull finds them current and leaves the
// hub's owner as it was. The ownership check finds them.
func TestTransferBackOnCentral(t *testing.T) {
	e, nurse := ownershipEnv(t)
	e.Setup()
	register(t, nurse)
	e.Sync()
	if st, b := nurse.Do("PUT", "/api/tracker/ownership/transfer?trackedEntity="+seedTE+"&program="+fc.Program+"&ou="+fc.OtherFacility, nil); st != 200 {
		t.Fatalf("transfer online through the hub: %d %s", st, b)
	}
	e.Fake.Transfer(seedTE, fc.Facility)
	if st, b := nurse.Do("GET", "/api/tracker/trackedEntities/"+seedTE+"?fields=*", nil); st != 200 {
		t.Fatalf("online read of the person: %d %s", st, b)
	}
	e.Sync()
	if s, o, ev := seesOffline(t, e, nurse); !s || !o || !ev {
		t.Errorf("after a transfer back on central and a sync: search %v, open %v, events %v; want all", s, o, ev)
	}
}

func TestBreakTheGlass(t *testing.T) {
	e, nurse := ownershipEnv(t)
	e.Fake.Transfer(seedTE, fc.OtherFacility) // registered here, owned elsewhere
	e.Setup()
	register(t, nurse)
	e.Sync()
	open := "/api/tracker/trackedEntities/" + seedTE + "?program=" + fc.Program + "&fields=*,enrollments[*,events[*]],programOwners"
	if st, _ := nurse.Do("GET", open, nil); st != 403 {
		t.Fatalf("online, before breaking the glass: %d, want 403", st)
	}
	if st, b := nurse.Do("POST", "/api/tracker/ownership/override?trackedEntity="+seedTE+"&program="+fc.Program+"&reason=emergency", nil); st != 200 {
		t.Fatalf("break the glass through the hub: %d %s", st, b)
	}
	if st, b := nurse.Do("GET", open, nil); st != 200 {
		t.Fatalf("online, with temporary ownership: %d %s", st, b)
	}
	if s, o, ev := seesOffline(t, e, nurse); s || o || ev {
		t.Errorf("offline after breaking the glass online: search %v, open %v, events %v; want none", s, o, ev)
	}
	e.Fake.ExpireTemporaryAccess()
	e.Sync()
	if s, o, ev := seesOffline(t, e, nurse); s || o || ev {
		t.Errorf("offline after the temporary ownership expired: search %v, open %v, events %v; want none", s, o, ev)
	}
}
