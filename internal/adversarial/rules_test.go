package adversarial

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// A14 (C in the plan: Capture runs program rules on the device): program rules change
// on central while an event made under the old rules waits offline. A new rule assigns
// a value to one data element, another hides a second one; the waiting event carries
// other values in both, as Capture with its cached rules would save it. What the API
// shows: what central does with the event on push, what it answers the same payload
// directly, and which rules the hub gives a client offline. What Capture itself shows
// and does with cached rules offline needs a real client (and on Android, its rule
// engine's version).
func TestA14ProgramRulesChange(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("program rules on a real DHIS2")
	}
	p := s.M.Primary()
	stage := p.Stages[0]
	var assignDE, hideDE string
	for _, el := range s.stageElements(stage) {
		if _, filled := p.EventValues[stage][el.ID]; filled || el.Compulsory || el.OptionSet != "" {
			continue
		}
		if assignDE == "" {
			assignDE = el.ID
		} else if hideDE == "" {
			hideDE = el.ID
		}
	}
	if hideDE == "" {
		t.Skip("the stage has fewer than two optional data elements")
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA14a")
	rulesPath := "/api/programRules?" + url.Values{"filter": {"program.id:eq:" + p.ID},
		"fields": {"id,condition,programRuleActions[programRuleActionType,dataElement[id],data]"}, "paging": {"false"}}.Encode()
	rules := func(when string) int {
		t.Helper()
		st, b := nurse.Do("GET", rulesPath, nil)
		var r struct {
			ProgramRules []any `json:"programRules"`
		}
		json.Unmarshal(b, &r)
		t.Logf("%s (online=%v): the hub gives the nurse %d program rules (%d)", when, !s.Net.Down(), len(r.ProgramRules), st)
		return len(r.ProgramRules)
	}
	before := rules("before the change")
	s.Sync()

	s.Offline()
	values := map[string]string{assignDE: "7", hideDE: "11"}
	for de, v := range p.EventValues[stage] {
		values[de] = v
	}
	ev := tk.UID()
	if st, _ := s.save(nurse, "an event under the old rules", s.event(p, stage, a.Enrollment, ev, values)); st != 200 {
		t.Fatal("the hub refused valid work offline")
	}

	r1, a1, r2, a2 := tk.UID(), tk.UID(), tk.UID(), tk.UID()
	md := map[string]any{
		"programRules": []any{
			map[string]any{"id": r1, "name": "Adversarial A14 assign " + r1, "program": map[string]any{"id": p.ID},
				"condition": "true", "programRuleActions": []any{map[string]any{"id": a1}}},
			map[string]any{"id": r2, "name": "Adversarial A14 hide " + r2, "program": map[string]any{"id": p.ID},
				"condition": "true", "programRuleActions": []any{map[string]any{"id": a2}}},
		},
		"programRuleActions": []any{
			map[string]any{"id": a1, "programRule": map[string]any{"id": r1}, "programRuleActionType": "ASSIGN",
				"dataElement": map[string]any{"id": assignDE}, "data": "99"},
			map[string]any{"id": a2, "programRule": map[string]any{"id": r2}, "programRuleActionType": "HIDEFIELD",
				"dataElement": map[string]any{"id": hideDE}},
		},
	}
	if st, b, err := s.Central.Do("POST", "/api/metadata", md); err != nil || st/100 != 2 || strings.Contains(string(b), `"status":"ERROR"`) {
		t.Fatalf("program rules: %d %v %.500s", st, err, b)
	}
	t.Cleanup(func() {
		for _, r := range []string{r1, r2} { // a rule's actions go with it
			if st, b, err := s.Central.Do("DELETE", "/api/programRules/"+r, nil); err != nil || st/100 != 2 {
				t.Errorf("NOT UNDONE on central: program rule %s: %d %v %.300s", r, st, err, b)
			}
		}
	})
	t.Logf("on central: rule %s assigns %s = 99, rule %s hides %s", r1, assignDE, r2, hideDE)

	rules("offline, after the change on central, before the sync")
	s.noRepeats(s.reconnect(3))
	for _, ln := range s.expect(nil, tk.OnCentral, tk.ChangedOnCentral, tk.RefusedKept) {
		if ln.UID == ev {
			t.Logf("the waiting event on central: %s %s", ln.Outcome, ln.Detail)
		}
	}
	got := valuesOf(must(s.Central.Do("GET", "/api/tracker/events/"+ev+"?fields=dataValues", nil)))
	t.Logf("central holds %s=%q (sent 7, the rule assigns 99), %s=%q (sent 11, the rule hides it)", assignDE, got[assignDE], hideDE, got[hideDE])
	if got[assignDE] == "" && got[hideDE] == "" && s.CentralObject("events", ev) == nil {
		t.Errorf("I1: the event is not on central")
	}

	// The same payload straight to central as the nurse: its import report.
	direct := tk.UID()
	st, b, _ := s.Central.As(nurse.User, s.M.Password).Do("POST", tk.SyncImport, s.event(p, stage, a.Enrollment, direct, values))
	var rep struct {
		Status           string
		ValidationReport struct {
			WarningReports []struct{ ErrorCode, Message string } `json:"warningReports"`
			ErrorReports   []struct{ ErrorCode, Message string } `json:"errorReports"`
		} `json:"validationReport"`
	}
	json.Unmarshal(b, &rep)
	t.Logf("the same event straight to central: %d %s, warnings %v, errors %v", st, rep.Status, rep.ValidationReport.WarningReports,
		rep.ValidationReport.ErrorReports)
	if st == 200 {
		s.CentralDelete("events", direct)
	}

	s.Offline()
	if n := rules("offline after the sync, the nurse not online since"); n != before {
		t.Logf("the hub's offline copy changed without the nurse reading online")
	}
	s.Online()
	rules("online")
	s.Offline()
	if n := rules("offline after reading online"); n != before+2 {
		t.Errorf("offline after the nurse read the rules online, the hub gives %d rules, want %d", n, before+2)
	}
}
