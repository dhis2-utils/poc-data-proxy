package live

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Write verdicts.
const (
	wMatch      = "MATCH"       // hub and central agree: both accept or both refuse
	wDenied     = "DENIED"      // hub accepted offline what central refuses this user for access reasons
	wLate       = "LATE"        // hub accepted offline, central rejected on push for another reason
	wOverReject = "OVER-REJECT" // hub refused offline what central accepts: a coverage gap
	wLost       = "LOST"        // hub accepted offline, central does not have it, and no issue says why
	wAltered    = "ALTERED"     // central has it, but not as written: another author or other values
)

// accessCodes are central's error codes for "this user may not write this".
var accessCodes = []string{"E1000", "E1083", "E1091", "E1095", "E1096", "E1099", "E1100", "E1102", "E1103", "E1104"}

type write struct {
	name    string
	program tk.Program
	payload func(fresh bool) (uid, kind string, body map[string]any)
}

// TestWriteOracle has every registered user try the same writes offline through the
// hub, then lets central judge them after reconnection (docs/TESTING.md). Live only.
func TestWriteOracle(t *testing.T) {
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST)")
	}
	m := e.M
	e.Setup()
	type person struct {
		name string
		c    *tk.Client
	}
	var users []person
	for role := range m.Users {
		users = append(users, person{m.Users[role].Username, e.Android(role)})
	}
	for _, mu := range m.Matrix {
		users = append(users, person{mu.Username, e.AndroidUser(mu.Username)})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].name < users[j].name })
	for _, u := range users {
		u.c.Do("GET", "/api/me?fields=id", nil)
	}
	e.Sync()

	var writes []write
	for _, p := range m.Programs {
		p := p
		own, sib := enrollmentAt(t, e, p, m.Facility.ID), enrollmentAt(t, e, p, m.Sibling.ID)
		writes = append(writes, write{"register a person", p, func(bool) (string, string, map[string]any) {
			pr := m.NewPerson(p, "Wr"+tk.UID()[:6], p.Stages[0])
			// Mandatory generated values are left out here: reserving them is a separate
			// call. Programs that need them are covered by the scenario suite.
			return pr.TE, "trackedEntities", pr.Payload
		}})
		for _, st := range p.Stages {
			st := st
			if own != "" {
				writes = append(writes, write{"event in stage " + st + " of a facility enrollment", p, func(bool) (string, string, map[string]any) {
					ev, body := m.NewEvent(p, st, own)
					return ev, "events", body
				}})
			}
		}
		if sib != "" {
			writes = append(writes, write{"event in an enrollment the sibling facility owns", p, func(bool) (string, string, map[string]any) {
				ev, body := m.NewEvent(p, p.Stages[0], sib)
				return ev, "events", body
			}})
		}
		// Attribute option combinations: accepted offline only by users who may write
		// each of their options (E1099 otherwise), as central decides.
		if own != "" {
			for _, aoc := range p.AOCs {
				aoc := aoc
				writes = append(writes, write{"event under option combination " + aoc, p, func(bool) (string, string, map[string]any) {
					ev, body := m.NewEvent(p, p.Stages[0], own)
					body["events"].([]any)[0].(map[string]any)["attributeOptionCombo"] = aoc
					return ev, "events", body
				}})
			}
		}
		for _, aoc := range p.EnrollmentAOCs {
			aoc := aoc
			writes = append(writes, write{"register a person enrolled under option combination " + aoc, p, func(bool) (string, string, map[string]any) {
				pr := m.NewPerson(p, "Wr"+tk.UID()[:6])
				for _, en := range pr.Payload["trackedEntities"].([]any)[0].(map[string]any)["enrollments"].([]any) {
					en.(map[string]any)["attributeOptionCombo"] = aoc
				}
				return pr.TE, "trackedEntities", pr.Payload
			}})
		}
	}
	for _, p := range m.EventPrograms {
		p := p
		aocs := p.AOCs
		if len(aocs) == 0 {
			aocs = []string{""}
		}
		for _, aoc := range aocs {
			aoc := aoc
			name := "event in an event program"
			if aoc != "" {
				name += " under option combination " + aoc
			}
			writes = append(writes, write{name, p, func(bool) (string, string, map[string]any) {
				uid, body := programEvent(m, p, aoc)
				return uid, "events", body
			}})
		}
	}

	type attempt struct {
		user, write, uid, kind string
		hub                    int
		body                   map[string]any
		w                      write
	}
	e.Offline()
	var attempts []attempt
	for _, u := range users {
		for _, w := range writes {
			if len(w.program.MandatoryGenerated) > 0 && strings.HasPrefix(w.name, "register") {
				continue
			}
			uid, kind, body := w.payload(false)
			st, _ := u.c.Do("POST", tk.SyncImport, body)
			attempts = append(attempts, attempt{u.name, w.name + " (" + w.program.Name + ")", uid, kind, st, body, w})
		}
	}
	e.Online()
	e.Sync()

	counts := map[string]int{}
	var bad, report []string
	for _, a := range attempts {
		verdict, detail := wMatch, ""
		if a.hub == 200 {
			if c := e.CentralObject(a.kind, a.uid); c == nil {
				codes := issueCodes(e, a.uid)
				verdict, detail = wLate, strings.Join(codes, ",")
				if detail == noIssue {
					verdict = wLost // accepted, gone, and nobody told: silent loss
				}
				for _, c := range codes {
					for _, ac := range accessCodes {
						if c == ac {
							verdict = wDenied
						}
					}
				}
			} else if d := savedAsWritten(e, a.user, a.body); d != "" {
				verdict, detail = wAltered, d
			}
		} else {
			// Would central have taken it from this user? Replay with fresh UIDs.
			uid, kind, body := a.w.payload(true)
			st, _, err := e.Central.As(a.user, m.Password).Do("POST", tk.SyncImport, body)
			if err != nil {
				t.Fatal(err)
			}
			if st == 200 && e.CentralObject(kind, uid) != nil {
				verdict, detail = wOverReject, fmt.Sprintf("hub answered %d", a.hub)
			}
		}
		counts[verdict]++
		line := fmt.Sprintf("%s %s: %s %s", verdict, a.user, a.write, detail)
		report = append(report, line)
		if verdict == wDenied || verdict == wLost || verdict == wAltered {
			bad = append(bad, line)
		}
	}
	t.Logf("%d writes: %v", len(attempts), counts)
	if p := os.Getenv("HUB_REPORT"); p != "" {
		os.WriteFile(strings.TrimSuffix(p, ".json")+"-writes.txt", []byte(strings.Join(report, "\n")+"\n"), 0o644)
	}
	gaps := map[string]int{}
	for _, l := range report {
		if strings.HasPrefix(l, wOverReject) || strings.HasPrefix(l, wLate) {
			gaps[strings.SplitN(l, ": ", 2)[1]]++
		}
	}
	for w, n := range gaps {
		t.Logf("not a failure, %d users: %s", n, w)
	}
	for _, l := range bad {
		t.Error(l)
	}
	if n := e.App.Hub.Store.CountOutbox(store.OutboxPending); n > e.App.Hub.StuckOutbox() {
		t.Errorf("%d outbox entries still pending after sync", n)
	}
}

func programEvent(m *tk.Manifest, p tk.Program, aoc string) (string, map[string]any) {
	uid := tk.UID()
	ev := map[string]any{"event": uid, "program": p.ID, "programStage": p.Stages[0], "orgUnit": m.Facility.ID,
		"occurredAt": time.Now().UTC().Format(time.DateOnly), "status": "ACTIVE"}
	if aoc != "" {
		ev["attributeOptionCombo"] = aoc
	}
	if vals := p.EventValues[p.Stages[0]]; len(vals) > 0 {
		var dv []any
		for de, v := range vals {
			dv = append(dv, map[string]any{"dataElement": de, "value": v})
		}
		ev["dataValues"] = dv
	}
	return uid, map[string]any{"events": []any{ev}}
}

// enrollmentAt returns an enrollment in program p of a tracked entity owned by ou.
func enrollmentAt(t *testing.T, e *tk.Env, p tk.Program, ou string) string {
	var res struct {
		TEs []struct {
			Enrollments []struct {
				Enrollment string `json:"enrollment"`
				Program    string `json:"program"`
				Status     string `json:"status"`
			} `json:"enrollments"`
		} `json:"trackedEntities"`
	}
	q := url.Values{"program": {p.ID}, "orgUnits": {ou}, "pageSize": {"20"}, "fields": {"enrollments[enrollment,program,status]"}}
	if err := e.Central.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &res); err != nil {
		t.Fatal(err)
	}
	for _, te := range res.TEs {
		for _, en := range te.Enrollments {
			if en.Program == p.ID && en.Status == "ACTIVE" {
				return en.Enrollment
			}
		}
	}
	return ""
}

// issueCodes returns the error codes the hub recorded for an object it could not push.
func issueCodes(e *tk.Env, uid string) []string {
	issues, _ := e.App.Hub.Store.Issues(false, 100000)
	var out []string
	for _, is := range issues {
		if is.ObjectUID != uid {
			continue
		}
		var body struct {
			Message      string `json:"message"`
			ErrorReports []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"errorReports"`
		}
		json.Unmarshal(is.Error, &body)
		for _, r := range body.ErrorReports {
			out = append(out, r.ErrorCode)
		}
		if len(body.ErrorReports) == 0 {
			out = append(out, body.Message)
		}
	}
	if len(out) == 0 {
		out = append(out, noIssue)
	}
	return out
}

const noIssue = "no issue recorded"

// savedAsWritten compares what central holds with what the user wrote offline: each
// tracked entity's and event's author, and the attribute and data values of the
// payload, which central must hold with the same values. Central may hold more (values
// a program rule assigned, for example). It returns "" when they agree, or what differs.
func savedAsWritten(e *tk.Env, user string, body map[string]any) string {
	var diffs []string
	check := func(kind, uid, program string, written map[string]any, listKey, idKey string) {
		// Asked with the program: without one, central leaves out the program's own
		// attributes.
		path := "/api/tracker/" + kind + "/" + uid + "?fields=*"
		if program != "" {
			path += "&program=" + program
		}
		var c map[string]any
		if err := e.Central.JSON("GET", path, nil, &c); err != nil {
			diffs = append(diffs, kind+" "+uid+" not readable on central: "+err.Error())
			return
		}
		by, _ := c["createdBy"].(map[string]any)
		if by["username"] != user {
			diffs = append(diffs, fmt.Sprintf("%s %s createdBy %v", kind, uid, by["username"]))
		}
		held := map[string]any{}
		for _, x := range objList(c[listKey]) {
			held[fmt.Sprint(x[idKey])] = x["value"]
		}
		for _, x := range objList(written[listKey]) {
			id := fmt.Sprint(x[idKey])
			if v, ok := held[id]; !ok || fmt.Sprint(v) != fmt.Sprint(x["value"]) {
				diffs = append(diffs, fmt.Sprintf("%s %s %s: wrote %v, central %v", kind, uid, id, x["value"], v))
			}
		}
	}
	event := func(ev map[string]any) {
		check("events", fmt.Sprint(ev["event"]), "", ev, "dataValues", "dataElement")
	}
	for _, te := range objList(body["trackedEntities"]) {
		program := ""
		for _, en := range objList(te["enrollments"]) {
			program = fmt.Sprint(en["program"])
		}
		check("trackedEntities", fmt.Sprint(te["trackedEntity"]), program, te, "attributes", "attribute")
		for _, en := range objList(te["enrollments"]) {
			for _, ev := range objList(en["events"]) {
				event(ev)
			}
		}
	}
	for _, ev := range objList(body["events"]) {
		event(ev)
	}
	return strings.Join(diffs, "; ")
}

func objList(v any) []map[string]any {
	var out []map[string]any
	l, _ := v.([]any)
	for _, x := range l {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
