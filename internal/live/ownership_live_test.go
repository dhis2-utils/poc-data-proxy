package live

import (
	"encoding/json"
	"math/rand/v2"
	"net/url"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestOwnershipLive transfers a facility-owned person to the sibling facility on a
// real central while the hub holds them, and checks that after the next sync the
// nurse sees offline on the hub no more than central shows them online. In a
// protected program it also breaks the glass online through the hub and checks that
// nothing of it is visible offline. Ownership is restored at the end. Live only.
func TestOwnershipLive(t *testing.T) {
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST)")
	}
	m := e.M
	p := m.Primary()
	for _, x := range m.Programs {
		if x.AccessLevel == "PROTECTED" {
			p = x
			break
		}
	}
	te := firstOwned(t, e, p.ID, m.Facility.ID)
	if te == "" {
		t.Skipf("no person owned by the facility in %s", p.Name)
	}
	e.Setup()
	nurse := m.Users[tk.RoleNurse]
	c := e.AndroidUser(nurse.Username)
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("login: %d %s", st, b)
	}
	e.Sync()
	asNurse := e.Central.As(nurse.Username, m.Password)
	open := "/api/tracker/trackedEntities/" + te + "?program=" + p.ID + "&fields=trackedEntity,enrollments[enrollment]"

	// sees reports whether the answer shows the person's enrollment in the program.
	sees := func(st int, b []byte) bool {
		var r struct {
			Enrollments []struct{ Enrollment string } `json:"enrollments"`
		}
		return st == 200 && json.Unmarshal(b, &r) == nil && len(r.Enrollments) > 0
	}
	compare := func(step string) {
		t.Helper()
		st, b, err := asNurse.Do("GET", open, nil)
		if err != nil {
			t.Fatal(err)
		}
		central := sees(st, b)
		e.Offline()
		hst, hb := c.Do("GET", open, nil)
		e.Online()
		t.Logf("%s: central %d (enrollment %v), hub offline %d (enrollment %v)", step, st, central, hst, sees(hst, hb))
		switch hub := sees(hst, hb); {
		case hub && !central:
			t.Errorf("%s: LEAK: the hub shows %s's enrollment in %s offline; central refuses the nurse (%d)", step, te, p.Name, st)
		case !hub && central:
			t.Logf("%s: the hub refuses offline (%d) what central shows the nurse; not a failure", step, hst)
		}
	}
	transfer := func(ou string) {
		t.Helper()
		q := url.Values{"trackedEntity": {te}, "program": {p.ID}, "ou": {ou}}
		if st, b, err := e.Central.Do("PUT", "/api/tracker/ownership/transfer?"+q.Encode(), nil); err != nil || st != 200 {
			t.Fatalf("transfer to %s: %d %v %s", ou, st, err, b)
		}
	}
	defer transfer(m.Facility.ID)

	// Before the transfer only the hub is asked: a read on central would fill its
	// per-user ownership cache, and central would then keep showing the person to the
	// nurse for a while after the transfer (DHIS2 2.42; docs/FINDINGS.md).
	e.Offline()
	if st, b := c.Do("GET", open, nil); !sees(st, b) {
		e.Online()
		t.Fatalf("before the transfer the hub does not show the person offline: %d %s", st, b)
	}
	e.Online()
	transfer(m.Sibling.ID)
	e.Sync()
	compare("after a transfer to the sibling facility and a sync")

	if p.AccessLevel != "PROTECTED" {
		return
	}
	// Break the glass: the person is now owned by the sibling facility.
	q := url.Values{"trackedEntity": {te}, "program": {p.ID}, "reason": {"hub test"}}
	if st, b := c.Do("POST", "/api/tracker/ownership/override?"+q.Encode(), nil); st != 200 {
		t.Fatalf("break the glass through the hub: %d %s", st, b)
	}
	// Opened online as Capture does, with everything, so the hub could learn it all.
	full := "/api/tracker/trackedEntities/" + te + "?program=" + p.ID + "&fields=*,enrollments[*,events[*]],programOwners,relationships[*]"
	if st, b := c.Do("GET", full, nil); !sees(st, b) {
		t.Fatalf("online with temporary ownership, the nurse cannot open the person: %d %.300s", st, b)
	}
	e.Offline()
	st, b := c.Do("GET", open, nil)
	e.Online()
	t.Logf("after breaking the glass online: hub offline %d (enrollment %v)", st, sees(st, b))
	if sees(st, b) {
		t.Errorf("LEAK: after breaking the glass online, the hub shows the enrollment offline: %s", b)
	}
}

func firstOwned(t *testing.T, e *tk.Env, program, ou string) string {
	var r struct {
		TEs []struct{ TrackedEntity string } `json:"trackedEntities"`
	}
	// A random one: central's ownership cache may still hold earlier reads of others.
	q := url.Values{"program": {program}, "orgUnits": {ou}, "orgUnitMode": {"SELECTED"}, "pageSize": {"100"}, "fields": {"trackedEntity"}}
	if err := e.Central.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.TEs) == 0 {
		return ""
	}
	return r.TEs[rand.IntN(len(r.TEs))].TrackedEntity
}
