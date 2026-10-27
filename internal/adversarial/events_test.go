package adversarial

import (
	"net/url"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// eventProgram is the manifest's first event program, or skips.
func (s *scene) eventProgram() tk.Program {
	s.t.Helper()
	if !s.Live() {
		s.t.Skip("adversarial scenarios run against a real DHIS2 (HUB_MANIFEST)")
	}
	if len(s.M.EventPrograms) == 0 {
		s.t.Skip("this setup has no event program at the facility")
	}
	return s.M.EventPrograms[0]
}

// programEvent is a payload of one event of an event program (no enrollment), with
// the stage's values and these.
func (s *scene) programEvent(p tk.Program, uid string, values map[string]string) map[string]any {
	stage := p.Stages[0]
	dvs := []any{}
	for de, v := range p.EventValues[stage] {
		dvs = append(dvs, map[string]any{"dataElement": de, "value": v})
	}
	for de, v := range values {
		dvs = append(dvs, map[string]any{"dataElement": de, "value": v})
	}
	ev := map[string]any{"event": uid, "program": p.ID, "programStage": stage, "orgUnit": s.M.Facility.ID,
		"occurredAt": time.Now().UTC().Format(time.DateOnly), "status": "ACTIVE", "dataValues": dvs}
	if len(p.AOCs) > 0 {
		ev["attributeOptionCombo"] = p.AOCs[0]
	}
	return map[string]any{"events": []any{ev}}
}

// numberElement is a data element of the event program's stage that takes a number
// and is not filled by the manifest's values.
func (s *scene) numberElement(p tk.Program) string {
	s.t.Helper()
	var res struct {
		PSDEs []struct {
			DataElement struct{ ID, ValueType string } `json:"dataElement"`
		} `json:"programStageDataElements"`
	}
	if err := s.Central.JSON("GET", "/api/programStages/"+p.Stages[0]+"?fields=programStageDataElements[dataElement[id,valueType]]", nil, &res); err != nil {
		s.t.Fatal(err)
	}
	for _, x := range res.PSDEs {
		if _, filled := p.EventValues[p.Stages[0]][x.DataElement.ID]; !filled && (x.DataElement.ValueType == "NUMBER" || x.DataElement.ValueType == "INTEGER") {
			return x.DataElement.ID
		}
	}
	s.t.Skipf("stage %s has no number to edit", p.Stages[0])
	return ""
}

// K1: the event program is closed at the facility (unassigned from it on central)
// while events in it wait offline: new ones, and an edit of one saved online. Must
// hold: as central decides, refusals kept; the program leaves the hub at the next pull;
// nothing leaks after.
func TestK1EventProgramClosedAtFacility(t *testing.T) {
	s := begin(t)
	p := s.eventProgram()
	de := s.numberElement(p)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	online := tk.UID()
	if st, _ := s.save(nurse, "an event saved online", s.programEvent(p, online, map[string]string{de: "1"})); st != 200 {
		t.Fatal("the hub refused a valid event online")
	}
	s.Sync()

	s.Offline()
	a, b := tk.UID(), tk.UID()
	for what, payload := range map[string]map[string]any{
		"a new event":                       s.programEvent(p, a, map[string]string{de: "2"}),
		"another new event":                 s.programEvent(p, b, map[string]string{de: "3"}),
		"an edit of the event saved online": s.programEvent(p, online, map[string]string{de: "4"}),
	} {
		if st, _ := s.save(nurse, what, payload); st != 200 {
			t.Fatal("the hub refused valid work offline")
		}
	}

	s.RemoveProgramFromOrgUnit(p.ID, s.M.Facility.ID)
	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{a: tk.RefusedKept, b: tk.RefusedKept, online: tk.RefusedKept}, tk.OnCentral, tk.RefusedKept)
	s.shows(nurse, "events", a)

	if s.App.Hub.Catalog().Programs[p.ID] != nil {
		t.Errorf("program %s still in the hub's catalog after the pull", p.ID)
	}
	s.Offline()
	q := url.Values{"program": {p.ID}, "orgUnit": {s.M.Facility.ID}, "fields": {"event"}, "pageSize": {"200"}}
	if l := s.leaks(nurse, "/api/tracker/events?"+q.Encode(), "events", "event"); len(l) > 0 {
		t.Errorf("I3: the hub shows %d events central does not: %v", len(l), l)
	}
}

// K3: an event of an event program is deleted on central while it is edited offline.
// Must hold: the edit refused and kept; the event leaves the hub's lists.
func TestK3EventDeletedWhileEditedOffline(t *testing.T) {
	s := begin(t)
	p := s.eventProgram()
	de := s.numberElement(p)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	ev, other := tk.UID(), tk.UID()
	for _, uid := range []string{ev, other} {
		if st, _ := s.save(nurse, "an event saved online", s.programEvent(p, uid, map[string]string{de: "1"})); st != 200 {
			t.Fatal("the hub refused a valid event online")
		}
	}
	s.Sync()

	s.Offline()
	for _, uid := range []string{ev, other} {
		if st, _ := s.save(nurse, "an edit offline", s.programEvent(p, uid, map[string]string{de: "2"})); st != 200 {
			t.Fatal("the hub refused a valid edit offline")
		}
	}
	s.CentralDelete("events", ev)
	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{ev: tk.RefusedKept, other: tk.OnCentral}, tk.OnCentral, tk.RefusedKept)
	if o := s.CentralObject("events", ev); o != nil {
		t.Errorf("I4: the event deleted on central is back: %v", o["deleted"])
	}
	s.shows(nurse, "events", ev)

	// I8: what a client lists offline. The hub keeps the refused edit, and serves it, as
	// for any refusal (A2): the event central deleted is listed as live offline, and in
	// Android's download (includeDeleted) not as deleted, until the refusal is resolved.
	s.Offline()
	q := url.Values{"program": {p.ID}, "orgUnit": {s.M.Facility.ID}, "fields": {"event,deleted"}, "paging": {"false"}}
	t.Logf("offline, the event central deleted is in the working list: %v", eventsIn(nurse.JSON("GET", "/api/tracker/events?"+q.Encode(), nil, 200))[ev])
	q.Set("includeDeleted", "true")
	for _, x := range asList(nurse.JSON("GET", "/api/tracker/events?"+q.Encode(), nil, 200)["events"]) {
		if m, _ := x.(map[string]any); m["event"] == ev {
			t.Logf("offline, Android's download lists it with deleted=%v", m["deleted"])
		}
	}
}

func eventsIn(body map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, x := range asList(body["events"]) {
		if m, ok := x.(map[string]any); ok {
			out[m["event"].(string)] = true
		}
	}
	return out
}
