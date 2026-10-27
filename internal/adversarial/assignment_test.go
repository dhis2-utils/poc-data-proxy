package adversarial

import (
	"net/url"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// A5: the program is removed from the facility while a registration and an event in
// it wait offline. Must hold: offline work refused or accepted as central decides,
// refusals kept; the program leaves the hub at the next pull; nothing leaks after.
func TestA5ProgramRemovedFromFacility(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("the fake central cannot unassign its program")
	}
	p := s.M.Primary()
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA5a")
	stage := p.Stages[0]

	s.Offline()
	b := s.M.NewPerson(p, "AdvA5b", stage)
	nurse.Complete(p, b)
	ev := tk.UID()
	for what, payload := range map[string]map[string]any{"a new registration with an event": b.Payload,
		"an event for a person registered online": s.event(p, stage, a.Enrollment, ev, p.EventValues[stage])} {
		if st, _ := s.save(nurse, what, payload); st != 200 {
			t.Fatal("the hub refused valid work offline")
		}
	}

	s.RemoveProgramFromOrgUnit(p.ID, s.M.Facility.ID)
	s.noRepeats(s.reconnect(3))
	s.expect(nil, tk.OnCentral, tk.RefusedKept)

	if s.App.Hub.Catalog().Programs[p.ID] != nil {
		t.Errorf("program %s still in the hub's catalog after the pull", p.ID)
	}
	s.Offline()
	q := url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID}, "fields": {"trackedEntity"}, "pageSize": {"200"}}
	if l := s.leaks(nurse, "/api/tracker/trackedEntities?"+q.Encode(), "trackedEntities", "trackedEntity"); len(l) > 0 {
		t.Errorf("I3: the hub shows %d people central does not: %v", len(l), l)
	}
	q = url.Values{"program": {p.ID}, "orgUnit": {s.M.Facility.ID}, "fields": {"event"}, "pageSize": {"200"}}
	if l := s.leaks(nurse, "/api/tracker/events?"+q.Encode(), "events", "event"); len(l) > 0 {
		t.Errorf("I3: the hub shows %d events central does not: %v", len(l), l)
	}
}
