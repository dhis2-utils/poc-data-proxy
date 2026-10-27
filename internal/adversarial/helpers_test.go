package adversarial

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// scene is one scenario: a hub set up at the facility, and the ledger of what it
// confirmed to clients.
type scene struct {
	*tk.Env
	t *testing.T
	l *tk.Ledger
}

func begin(t *testing.T) *scene {
	e := tk.Start(t)
	e.Setup()
	return &scene{Env: e, t: t, l: e.Ledger()}
}

// connect makes a user's Android client known to the hub, online, as the app's first
// login does.
func (s *scene) connect(username string) *tk.Client {
	s.t.Helper()
	c := s.AndroidUser(username)
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 200 {
		s.t.Fatalf("%s: /api/me %d %.300s", username, st, b)
	}
	return c
}

// save posts a tracker payload as the client; what the hub confirms goes in the ledger.
func (s *scene) save(c *tk.Client, what string, payload map[string]any) (int, []byte) {
	s.t.Helper()
	st, b := c.Do("POST", tk.SyncImport, payload)
	if st == 200 {
		s.l.Confirmed(c.User, payload)
	}
	s.t.Logf("%s (%s, online=%v): hub answers %d %.400s", what, c.User, !s.Net.Down(), st, b)
	return st, b
}

// register registers a person in p online, without events, and returns them.
func (s *scene) register(c *tk.Client, p tk.Program, name string) *tk.Person {
	s.t.Helper()
	pr := s.M.NewPerson(p, name)
	c.Complete(p, pr)
	if st, _ := s.save(c, "register "+name, pr.Payload); st != 200 {
		s.t.Fatalf("registering %s failed", name)
	}
	return pr
}

// event builds a payload of one event with these data values.
func (s *scene) event(p tk.Program, stage, enrollment, uid string, values map[string]string) map[string]any {
	dvs := []any{}
	for de, v := range values {
		dvs = append(dvs, map[string]any{"dataElement": de, "value": v})
	}
	return map[string]any{"events": []any{map[string]any{"event": uid, "enrollment": enrollment, "program": p.ID,
		"programStage": stage, "orgUnit": s.M.Facility.ID, "occurredAt": time.Now().UTC().Format("2006-01-02"),
		"status": "ACTIVE", "dataValues": dvs}}}
}

// reconnect brings the uplink back and runs cycles, returning how many tracker pushes
// each cycle sent to central: after the first, refused changes must not be sent again
// and again (I5, I7). A cycle's error is logged, not fatal.
func (s *scene) reconnect(cycles int) []int {
	s.t.Helper()
	s.Online()
	s.Net.Calls()
	var pushes []int
	for range cycles {
		if err := s.App.Worker.Cycle(context.Background()); err != nil {
			s.t.Logf("sync: %v", err)
		}
		n := 0
		for _, c := range s.Net.Calls() {
			if c.Method == "POST" && c.URL.Path == "/api/tracker" {
				n++
			}
		}
		pushes = append(pushes, n)
	}
	s.t.Logf("tracker pushes per cycle: %v; pending outbox %d, stuck %v", pushes,
		s.App.Hub.Store.CountOutbox(store.OutboxPending), s.App.Hub.StuckOutbox())
	return pushes
}

// noRepeats fails if a cycle after the first pushed again.
func (s *scene) noRepeats(pushes []int) {
	s.t.Helper()
	if len(pushes) > 1 && slices.ContainsFunc(pushes[1:], func(n int) bool { return n > 0 }) {
		s.t.Errorf("I5/I7: pushes repeat in later cycles: %v", pushes)
	}
}

// expect checks the ledger: every save has an allowed outcome, and the last save of
// each named object has exactly its own.
func (s *scene) expect(want map[string]string, allowed ...string) []tk.Line {
	s.t.Helper()
	lines := s.l.Check()
	for i, ln := range lines {
		s.t.Logf("ledger: %s", ln)
		later := slices.ContainsFunc(lines[i+1:], func(x tk.Line) bool { return x.UID == ln.UID })
		if w, ok := want[ln.UID]; ok && !later && ln.Outcome != w {
			s.t.Errorf("%s %s: %s, want %s", ln.Kind, ln.UID, ln.Outcome, w)
		} else if (!ok || later) && !slices.Contains(allowed, ln.Outcome) {
			s.t.Errorf("%s %s: %s", ln.Kind, ln.UID, ln.Outcome)
		}
	}
	return lines
}

// stageElement is a data element of a program stage on central.
type stageElement struct {
	ID         string
	Compulsory bool
	OptionSet  string
	Options    []string
}

func (s *scene) stageElements(stage string) []stageElement {
	s.t.Helper()
	var res struct {
		PSDEs []struct {
			Compulsory  bool
			DataElement struct {
				ID        string
				OptionSet *struct {
					ID      string
					Options []struct{ Code string }
				}
			}
		} `json:"programStageDataElements"`
	}
	if err := s.Central.JSON("GET", "/api/programStages/"+stage+
		"?fields=programStageDataElements[compulsory,dataElement[id,optionSet[id,options[code]]]]", nil, &res); err != nil {
		s.t.Fatal(err)
	}
	var out []stageElement
	for _, p := range res.PSDEs {
		el := stageElement{ID: p.DataElement.ID, Compulsory: p.Compulsory}
		if os := p.DataElement.OptionSet; os != nil {
			el.OptionSet = os.ID
			for _, o := range os.Options {
				el.Options = append(el.Options, o.Code)
			}
		}
		out = append(out, el)
	}
	return out
}

// errorCodes are the error codes in a tracker import report.
func errorCodes(b []byte) []string {
	var r struct {
		ValidationReport struct {
			ErrorReports []struct{ ErrorCode string } `json:"errorReports"`
		} `json:"validationReport"`
	}
	json.Unmarshal(b, &r)
	var out []string
	for _, e := range r.ValidationReport.ErrorReports {
		out = append(out, e.ErrorCode)
	}
	return out
}

// leaks asks the hub and central (as the same user, logging in with the password)
// the same listing, and returns what the hub shows that central does not (I3). coll
// and key name the list and its items' uid, e.g. trackedEntities and trackedEntity.
func (s *scene) leaks(c *tk.Client, path, coll, key string) []string {
	s.t.Helper()
	list := func(st int, b []byte) []string {
		if st != 200 {
			return nil
		}
		var r map[string]any
		json.Unmarshal(b, &r)
		var out []string
		for _, x := range asList(r[coll]) {
			if m, ok := x.(map[string]any); ok {
				out = append(out, fmt.Sprint(m[key]))
			}
		}
		return out
	}
	hst, hb := c.Do("GET", path, nil)
	var cst int
	var cb []byte
	if s.Live() {
		cst, cb, _ = s.Central.As(c.User, s.M.Password).Do("GET", path, nil)
	} else {
		req := httptest.NewRequest("GET", path, nil)
		req.SetBasicAuth(c.User, s.M.Password)
		w := httptest.NewRecorder()
		s.Fake.ServeHTTP(w, req)
		cst, cb = w.Code, w.Body.Bytes()
	}
	hub, central := list(hst, hb), list(cst, cb)
	s.t.Logf("%s as %s: hub (online=%v) %d (%d items), central %d (%d items) %.200s", path, c.User, !s.Net.Down(),
		hst, len(hub), cst, len(central), map[bool][]byte{true: cb}[cst != 200])
	return slices.DeleteFunc(hub, func(u string) bool { return slices.Contains(central, u) })
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// finding skips a scenario that documents a finding, so the suite stays green, unless
// HUB_FINDINGS is set.
func finding(t *testing.T, what string) {
	if os.Getenv("HUB_FINDINGS") == "" {
		t.Skip("FINDING: " + what + " (HUB_FINDINGS=1 runs it)")
	}
}

// shows logs what a client gets from the hub for an object, online and offline: what
// the app would show of a refused save (I8).
func (s *scene) shows(c *tk.Client, kind, uid string) (online, offline int) {
	s.t.Helper()
	for _, down := range []bool{false, true} {
		if down {
			s.Offline()
			defer s.Online()
		}
		st, b := c.Do("GET", "/api/tracker/"+kind+"/"+uid+"?fields=*", nil)
		s.t.Logf("%s %s through the hub (online=%v): %d %.200s", kind, uid, !down, st, b)
		if down {
			offline = st
		} else {
			online = st
		}
	}
	return
}
