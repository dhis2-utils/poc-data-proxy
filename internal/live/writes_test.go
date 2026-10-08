package live

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Write verdicts.
const (
	wMatch      = "MATCH"       // hub and central agree: both accept or both refuse
	wDenied     = "DENIED"      // hub accepted offline what central refuses this user for access reasons
	wLate       = "LATE"        // hub accepted offline, central rejected on push for another reason
	wOverReject = "OVER-REJECT" // hub refused offline what central accepts: a coverage gap
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
			if e.CentralObject(a.kind, a.uid) == nil {
				codes := issueCodes(e, a.uid)
				verdict, detail = wLate, strings.Join(codes, ",")
				for _, c := range codes {
					for _, ac := range accessCodes {
						if c == ac {
							verdict = wDenied
						}
					}
				}
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
		if verdict == wDenied {
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
		out = append(out, "no issue recorded")
	}
	return out
}
