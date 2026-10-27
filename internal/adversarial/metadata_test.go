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
