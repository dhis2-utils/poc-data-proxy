package adversarial

import (
	"maps"
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// A1: a data element becomes compulsory in a stage while events without it wait
// offline. Must hold: the event is refused on push with central's reason and kept,
// its later edits wait behind it, and an unrelated event goes through. Central checks
// compulsory data elements when an event is completed (the stage's validation
// strategy ON_COMPLETE, the default), so: a an active event completed later, c an
// event completed at once and edited after, b an event with the value.
func TestA1CompulsoryDataElement(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("the fake central has no stage metadata to change")
	}
	p := s.M.Primary()
	var stage, de string
	for _, st := range p.Stages {
		for _, el := range s.stageElements(st) {
			if _, filled := p.EventValues[st][el.ID]; !el.Compulsory && !filled && el.OptionSet == "" && stage == "" {
				stage, de = st, el.ID
			}
		}
	}
	if stage == "" {
		t.Skip("no optional data element to make compulsory")
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA1a")
	b := s.register(nurse, p, "AdvA1b")
	c := s.register(nurse, p, "AdvA1c")

	s.Offline()
	evA, evB, evC := tk.UID(), tk.UID(), tk.UID()
	must := func(st int, _ []byte) {
		t.Helper()
		if st != 200 {
			t.Fatal("the hub refused a valid event offline")
		}
	}
	must(s.save(nurse, "a: active event without "+de, s.event(p, stage, a.Enrollment, evA, p.EventValues[stage])))
	must(s.save(nurse, "a: completed", completed(s.event(p, stage, a.Enrollment, evA, p.EventValues[stage]))))
	must(s.save(nurse, "c: completed event without "+de, completed(s.event(p, stage, c.Enrollment, evC, p.EventValues[stage]))))
	edited := maps.Clone(p.EventValues[stage])
	for _, el := range s.stageElements(stage) {
		if _, ok := edited[el.ID]; !ok && el.ID != de && el.OptionSet == "" {
			edited[el.ID] = "8" // a later edit of another value
			break
		}
	}
	must(s.save(nurse, "c: edited", completed(s.event(p, stage, c.Enrollment, evC, edited))))
	with := maps.Clone(p.EventValues[stage])
	with[de] = "7"
	must(s.save(nurse, "b: completed event with "+de, completed(s.event(p, stage, b.Enrollment, evB, with))))

	s.SetCompulsory(stage, de, true)
	s.noRepeats(s.reconnect(3))
	lines := s.expect(map[string]string{evA: tk.RefusedKept, evC: tk.RefusedKept, evB: tk.OnCentral}, tk.OnCentral, tk.RefusedKept)
	for _, ln := range lines {
		if ln.Outcome == tk.RefusedKept && ln.Detail != tk.SavedAgain && !strings.Contains(ln.Detail, de) {
			t.Errorf("refusal of %s does not name %s: %s", ln.UID, de, ln.Detail)
		}
	}
}

// completed marks the payload's events completed.
func completed(payload map[string]any) map[string]any {
	for _, ev := range payload["events"].([]any) {
		ev.(map[string]any)["status"] = "COMPLETED"
	}
	return payload
}

// A2: an option is removed from an option set while events using it wait offline.
// Must hold as A1: refused with central's reason and kept; an event with an option
// that stays goes through.
func TestA2OptionRemoved(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("the fake central has no option sets to change")
	}
	p := s.M.Primary()
	var stage string
	var el stageElement
	for _, st := range p.Stages {
		for _, x := range s.stageElements(st) {
			if len(x.Options) >= 2 && stage == "" {
				stage, el = st, x
			}
		}
	}
	if stage == "" {
		t.Skip("no stage of the primary program has a data element with two options")
	}
	gone, stays := el.Options[len(el.Options)-1], el.Options[0]
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA2a")
	b := s.register(nurse, p, "AdvA2b")

	s.Offline()
	evGone, evStays := tk.UID(), tk.UID()
	for _, x := range []struct{ ev, enr, code string }{{evGone, a.Enrollment, gone}, {evStays, b.Enrollment, stays}} {
		v := maps.Clone(p.EventValues[stage])
		v[el.ID] = x.code
		if st, _ := s.save(nurse, "event with option "+x.code, s.event(p, stage, x.enr, x.ev, v)); st != 200 {
			t.Fatal("the hub refused a valid event offline")
		}
	}

	s.RemoveOption(el.OptionSet, gone)
	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{evGone: tk.RefusedKept, evStays: tk.OnCentral}, tk.OnCentral)
	s.shows(nurse, "events", evGone)
}

// A4: a stage is made non-repeatable while two events in it wait offline for one
// person. Must hold: the first reaches central, the second is refused and kept.
func TestA4StageNoLongerRepeatable(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("the fake central has no stage metadata to change")
	}
	p := s.M.Primary()
	stage := ""
	for _, st := range p.Stages {
		var r struct{ Repeatable bool }
		if s.Central.JSON("GET", "/api/programStages/"+st+"?fields=repeatable", nil, &r); r.Repeatable && stage == "" {
			stage = st
		}
	}
	if stage == "" {
		t.Skip("the primary program has no repeatable stage")
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA4a")

	s.Offline()
	first, second := tk.UID(), tk.UID()
	for _, ev := range []string{first, second} {
		if st, _ := s.save(nurse, "event in "+stage, s.event(p, stage, a.Enrollment, ev, p.EventValues[stage])); st != 200 {
			t.Fatal("the hub refused a valid event offline")
		}
	}

	s.SetRepeatable(stage, false)
	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{first: tk.OnCentral, second: tk.RefusedKept}, tk.OnCentral)

	// The hub has the new rule now: a third event offline is refused at once.
	s.Offline()
	if st, _ := s.save(nurse, "a third event, after the sync", s.event(p, stage, a.Enrollment, tk.UID(), p.EventValues[stage])); st == 200 {
		t.Errorf("the hub accepts an event in a non-repeatable stage offline after learning the rule")
	}
}
