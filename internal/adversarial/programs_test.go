package adversarial

import (
	"cmp"
	"net/url"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// today is the date clients put on what they save.
func today() string { return time.Now().UTC().Format("2006-01-02") }

// sameAsCentral reads each path as the client through the hub and as the same user on
// central, and fails where the hub answers 200 and central does not (I3). It returns
// the hub's statuses.
func (s *scene) sameAsCentral(c *tk.Client, when string, paths ...string) []int {
	s.t.Helper()
	var out []int
	for _, path := range paths {
		hst, _ := c.Do("GET", path, nil)
		cst, cb, _ := s.Central.As(c.User, cmp.Or(c.Password, s.M.Password)).Do("GET", path, nil)
		s.t.Logf("%s, as %s: %s: hub %d, central %d %.150s", when, c.User, path, hst, cst, map[bool][]byte{true: cb}[cst != 200])
		if hst == 200 && cst != 200 {
			s.t.Errorf("I3: %s: the hub (online=%v) answers %s 200 where central answers %d", path, !s.Net.Down(), c.User, cst)
		}
		out = append(out, hst)
	}
	return out
}

// A6: the primary program's access level changes from open to protected while the hub
// holds offline work. Must hold: ownership rules apply from the next pull; offline
// reads of other facilities' people stop (the hub holds only the facility's: they were
// never shown offline); work for the facility's own people reaches central.
//
// x is registered and owned at the sibling, in the nurse's search scope (the
// district): in an open program the nurse may read them on central; in a protected one
// only after breaking the glass.
func TestA6ProgramBecomesProtected(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("the fake central's access levels are tested in internal/live")
	}
	p := s.M.Primary()
	if p.AccessLevel != "OPEN" {
		t.Skipf("the primary program is %s, not open", p.AccessLevel)
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA6a")
	x := s.M.NewPerson(p, "AdvA6x")
	s.atSibling(x)
	s.CentralComplete(p, x)
	s.CentralImport(x.Payload)
	s.Sync()
	stage := p.Stages[0]
	others := []string{
		"/api/tracker/trackedEntities/" + x.TE + "?program=" + p.ID + "&fields=trackedEntity",
		"/api/tracker/enrollments/" + x.Enrollment + "?fields=enrollment",
		"/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnits": {s.M.District.ID}, "ouMode": {"DESCENDANTS"},
			"filter": {p.SearchAttr + ":eq:AdvA6x"}, "fields": {"trackedEntity"}}.Encode(),
	}
	s.sameAsCentral(nurse, "open, online", others...)
	s.Offline()
	s.sameAsCentral(nurse, "open, offline", others...)
	evA, evX := tk.UID(), tk.UID()
	if st, _ := s.save(nurse, "an event for the facility's person", s.event(p, stage, a.Enrollment, evA, p.EventValues[stage])); st != 200 {
		t.Fatal("the hub refused valid work offline")
	}
	st, b := s.save(nurse, "an event for the sibling's person", s.event(p, stage, x.Enrollment, evX, p.EventValues[stage]))
	t.Logf("offline, an event for a person the hub does not hold: %d %v", st, errorCodes(b))

	s.SetField("/api/programs/"+p.ID, "accessLevel", "PROTECTED")
	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{evA: tk.OnCentral}, tk.OnCentral)
	s.sameAsCentral(nurse, "protected, online", others...)
	s.noLeaks(nurse)
	s.Offline()
	s.sameAsCentral(nurse, "protected, offline", append(others,
		"/api/tracker/trackedEntities/"+a.TE+"?program="+p.ID+"&fields=trackedEntity")...)
	if st, _ := s.save(nurse, "an event for the facility's person, protected", s.event(p, stage, a.Enrollment, tk.UID(), p.EventValues[stage])); st != 200 {
		t.Error("offline, the hub refuses an event for a person the facility owns in a protected program")
	}
	s.reconnect(1)
	s.expect(nil, tk.OnCentral)
}

// enrollment is a payload of one enrollment of a person the hub holds.
func (s *scene) enrollment(p tk.Program, te, uid, status string) map[string]any {
	return map[string]any{"enrollments": []any{map[string]any{"enrollment": uid, "trackedEntity": te, "program": p.ID,
		"orgUnit": s.M.Facility.ID, "enrolledAt": today(), "occurredAt": today(), "status": status}}}
}

// A8: the program is set to enrol each person only once (onlyEnrollOnce) while an
// enrollment made offline waits: a's first enrollment completed and a second made,
// as staff do for a new episode; b registered and enrolled for the first time. Must
// hold: as central decides (a's second refused, E1016, and kept; b's accepted); the
// hub then refuses a second enrollment offline itself.
func TestA8OnlyEnrollOnceTurnedOn(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("the fake central's onlyEnrollOnce is tested in internal/live (TestEnrollmentRulesOffline)")
	}
	p := s.M.Primary()
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA8a")
	s.Sync()

	s.Offline()
	second := tk.UID()
	if st, _ := s.save(nurse, "a's enrollment completed", s.enrollment(p, a.TE, a.Enrollment, "COMPLETED")); st != 200 {
		t.Fatal("the hub refused valid work offline")
	}
	if st, _ := s.save(nurse, "a enrolled again", s.enrollment(p, a.TE, second, "ACTIVE")); st != 200 {
		t.Fatal("the hub refused a second enrollment after a completed one, with onlyEnrollOnce off")
	}
	b := s.M.NewPerson(p, "AdvA8b")
	nurse.Complete(p, b)
	if st, _ := s.save(nurse, "b registered", b.Payload); st != 200 {
		t.Fatal("the hub refused valid work offline")
	}

	s.SetField("/api/programs/"+p.ID, "onlyEnrollOnce", true)
	s.noRepeats(s.reconnect(3))
	lines := s.expect(map[string]string{second: tk.RefusedKept, b.Enrollment: tk.OnCentral, a.Enrollment: tk.OnCentral},
		tk.OnCentral)
	for _, ln := range lines {
		if ln.UID == second {
			t.Logf("central's reason: %s", ln.Detail)
		}
	}
	if !s.App.Hub.Catalog().Programs[p.ID].OnlyEnrollOnce {
		t.Error("after the sync the hub's catalog does not hold onlyEnrollOnce")
	}
	s.Offline()
	s.save(nurse, "b's enrollment completed", s.enrollment(p, b.TE, b.Enrollment, "COMPLETED"))
	st, body := s.save(nurse, "b enrolled again, after the sync", s.enrollment(p, b.TE, tk.UID(), "ACTIVE"))
	if st == 200 {
		t.Error("after the sync the hub takes a second enrollment offline where the program enrols once")
	} else {
		t.Logf("refused by the hub itself: %v", errorCodes(body))
	}
	s.reconnect(1)
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
}
