package adversarial

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// personEdit is a payload editing a person's attributes as Capture sends it: every
// attribute the person has, with these values changed.
func personEdit(p tk.Program, pr *tk.Person, facility string, changed map[string]string) map[string]any {
	te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
	attrs := []any{}
	for _, a := range te["attributes"].([]any) {
		am := a.(map[string]any)
		v := am["value"]
		if c, ok := changed[am["attribute"].(string)]; ok {
			v = c
		}
		attrs = append(attrs, map[string]any{"attribute": am["attribute"], "value": v})
	}
	return map[string]any{"trackedEntities": []any{map[string]any{"trackedEntity": pr.TE, "trackedEntityType": p.TET,
		"orgUnit": facility, "attributes": attrs}}}
}

// protected is the manifest's protected program: ownership decides who may write.
func (s *scene) protected() tk.Program {
	s.t.Helper()
	for _, p := range s.M.Programs {
		if p.AccessLevel == "PROTECTED" && !p.Event {
			return p
		}
	}
	s.t.Skip("no protected program in this setup")
	return tk.Program{}
}

// hubValues is what a user reads of a person's attributes, or an event's values,
// through the hub offline.
func (s *scene) hubValues(c *tk.Client, path string) (int, map[string]string) {
	s.t.Helper()
	s.Offline()
	defer s.Online()
	st, b := c.Do("GET", path, nil)
	return st, valuesOf(b)
}

func valuesOf(b []byte) map[string]string {
	var o struct {
		Attributes []struct{ Attribute, Value string }
		DataValues []struct{ DataElement, Value string }
	}
	json.Unmarshal(b, &o)
	out := map[string]string{}
	for _, a := range o.Attributes {
		out[a.Attribute] = a.Value
	}
	for _, d := range o.DataValues {
		out[d.DataElement] = d.Value
	}
	return out
}

// changeLog is central's change log of a person or an event: who set which value.
func (s *scene) changeLog(kind, uid string) []string {
	s.t.Helper()
	var r struct {
		ChangeLogs []struct {
			CreatedBy struct{ Username string }
			Type      string
			Change    map[string]struct {
				Attribute, DataElement      string
				PreviousValue, CurrentValue string
			}
		} `json:"changeLogs"`
	}
	if err := s.Central.JSON("GET", "/api/tracker/"+kind+"/"+uid+"/changeLogs?pageSize=100", nil, &r); err != nil {
		s.t.Fatal(err)
	}
	var out []string
	for _, c := range r.ChangeLogs {
		for _, v := range c.Change {
			out = append(out, fmt.Sprintf("%s %s %s%s %q->%q", c.CreatedBy.Username, c.Type, v.Attribute, v.DataElement,
				v.PreviousValue, v.CurrentValue))
		}
	}
	return out
}

// C1: the same person, and the same event, edited on central and on the hub offline.
// Must hold: recorded which edit wins on central and what the hub shows after the
// pull; neither edit lost without a trace (central's change log).
//
// The hub edits the first name; central edits the first and the last name. The hub
// sends every attribute, as Capture does, so its stale last name goes too. The event:
// both change the same value.
func TestC1EditedOnCentralAndOffline(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("central's change log is on a real DHIS2")
	}
	p := s.M.Primary()
	stage, de := "", ""
	for st, vals := range p.EventValues {
		for d, v := range vals {
			if _, err := fmt.Sscan(v, new(float64)); err == nil && stage == "" {
				stage, de = st, d
			}
		}
	}
	if stage == "" || p.SearchAttr == "" {
		t.Skip("no numeric event value or search attribute in the primary program")
	}
	last := ""
	for a := range p.RequiredAttrs {
		if a != p.SearchAttr {
			last = a
		}
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvC1a")
	ev := tk.UID()
	if st, _ := s.save(nurse, "an event", s.event(p, stage, a.Enrollment, ev, p.EventValues[stage])); st != 200 {
		t.Fatal("the hub refused an event online")
	}
	s.Sync()

	s.Offline()
	hubEv := map[string]string{de: "65"}
	if st, _ := s.save(nurse, "first name changed offline", personEdit(p, a, s.M.Facility.ID, map[string]string{p.SearchAttr: "AdvC1hub"})); st != 200 {
		t.Fatal("the hub refused an edit offline")
	}
	if st, _ := s.save(nurse, "event value changed offline", s.event(p, stage, a.Enrollment, ev, hubEv)); st != 200 {
		t.Fatal("the hub refused an edit offline")
	}
	central := map[string]string{p.SearchAttr: "AdvC1central"}
	if last != "" {
		central[last] = "Central"
	}
	s.CentralImport(personEdit(p, a, s.M.Facility.ID, central))
	s.CentralImport(s.event(p, stage, a.Enrollment, ev, map[string]string{de: "70"}))

	s.noRepeats(s.reconnect(3))
	s.expect(nil, tk.OnCentral, tk.ChangedOnCentral)
	for _, x := range []struct{ kind, uid, path string }{
		{"trackedEntities", a.TE, "/api/tracker/trackedEntities/" + a.TE + "?program=" + p.ID + "&fields=attributes"},
		{"events", ev, "/api/tracker/events/" + ev + "?fields=dataValues"},
	} {
		onCentral := valuesOf(must(s.Central.Do("GET", x.path, nil)))
		st, onHub := s.hubValues(nurse, x.path)
		t.Logf("%s %s: central %v; the hub offline after the pull: %d %v", x.kind, x.uid, onCentral, st, onHub)
		for k, v := range onCentral {
			if onHub[k] != v {
				t.Errorf("%s %s: the hub shows %s=%q, central %q", x.kind, x.uid, k, onHub[k], v)
			}
		}
		log := s.changeLog(x.kind, x.uid)
		t.Logf("%s %s: central's change log: %v", x.kind, x.uid, log)
		for k, v := range central {
			if x.kind == "events" {
				k, v = de, "70"
			}
			if onCentral[k] != v && !slices.ContainsFunc(log, func(l string) bool { return strings.Contains(l, k) && strings.Contains(l, fmt.Sprintf("%q", v)) }) {
				t.Errorf("I4: central's edit %s=%q of %s was overwritten without a trace", k, v, x.uid)
			}
		}
	}
}

func must(st int, b []byte, err error) []byte {
	if err != nil || st != 200 {
		return nil
	}
	return b
}

// C2: a person deleted on central while edited offline. Must hold: the edits refused
// as already deleted (E1114) and closed on the hub, which keeps a tombstone; the
// person disappears from the hub. Another person's event goes through.
func TestC2DeletedOnCentralWhileEditedOffline(t *testing.T) {
	deletedWhileEdited(t, false)
}

// The same with an event added offline to the deleted person's enrollment: central
// (2.42.7) accepts it into the deleted enrollment, where nobody sees it, and changes
// the person's updatedAt. When that lands more than a second after the person's edit
// was refused (once in three runs), the next pull takes central's newer, deleted
// person over the refused edit, which leaves the refused changes without a trace.
func TestC2EventForDeletedPerson(t *testing.T) {
	finding(t, "C2: an offline event for a person central deleted is accepted into the deleted enrollment: confirmed, seen by nobody")
	deletedWhileEdited(t, true)
}

func deletedWhileEdited(t *testing.T, withEvent bool) {
	s := begin(t)
	if !s.Live() {
		t.Skip("central's deletions are checked on a real DHIS2")
	}
	p := s.M.Primary()
	stage := p.Stages[0]
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvC2a")
	b := s.register(nurse, p, "AdvC2b")
	s.Sync()

	s.Offline()
	ev, evB := tk.UID(), tk.UID()
	work := []struct {
		what    string
		payload map[string]any
	}{
		{"name changed offline", personEdit(p, a, s.M.Facility.ID, map[string]string{p.SearchAttr: "AdvC2edited"})},
		{"another person's event", s.event(p, stage, b.Enrollment, evB, p.EventValues[stage])},
	}
	if withEvent {
		work = append(work, struct {
			what    string
			payload map[string]any
		}{"an event for the person", s.event(p, stage, a.Enrollment, ev, p.EventValues[stage])})
	}
	for _, x := range work {
		if st, _ := s.save(nurse, x.what, x.payload); st != 200 {
			t.Fatal("the hub refused valid work offline")
		}
	}
	s.CentralDelete("trackedEntities", a.TE)

	s.noRepeats(s.reconnect(3))
	for _, ln := range s.l.Check() {
		t.Logf("ledger: %s", ln)
		switch {
		case ln.UID == evB || ln.UID == b.TE || ln.UID == b.Enrollment:
			if ln.Outcome != tk.OnCentral {
				t.Errorf("%s %s: %s", ln.Kind, ln.UID, ln.Outcome)
			}
		case !ln.Online && ln.Outcome == tk.Closed:
		// Saved online, then deleted on central with the person: gone as central
		// decided, or closed with the person.
		case ln.Online && (ln.Outcome == tk.Lost || ln.Outcome == tk.Closed):
		default:
			t.Errorf("%s %s (online=%v): %s %s", ln.Kind, ln.UID, ln.Online, ln.Outcome, ln.Detail)
		}
	}
	if withEvent {
		if o := s.CentralObject("events", ev); o != nil {
			t.Logf("on central: event %s in enrollment %s, deleted=%v; the enrollment: %v", ev, o["enrollment"], o["deleted"],
				s.CentralObject("enrollments", a.Enrollment) != nil)
		}
	}
	s.shows(nurse, "trackedEntities", a.TE)
	s.Offline()
	q := url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID}, "filter": {p.SearchAttr + ":like:AdvC2"},
		"fields": {"trackedEntity"}}
	st, body := nurse.Do("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil)
	t.Logf("offline search for AdvC2: %d %.300s", st, body)
	if strings.Contains(string(body), a.TE) {
		t.Errorf("the person deleted on central is still found on the hub offline")
	}
	for _, path := range []string{"/api/tracker/trackedEntities/" + a.TE + "?program=" + p.ID,
		"/api/tracker/enrollments/" + a.Enrollment, "/api/tracker/events/" + ev} {
		if st, b := nurse.Do("GET", path, nil); st == 200 {
			t.Errorf("deleted on central, opens on the hub offline: %s %.200s", path, b)
		}
	}
	st, body = nurse.Do("GET", "/api/tracker/events?enrollments="+a.Enrollment+"&fields=event", nil)
	t.Logf("offline, the deleted enrollment's events: %d %.300s", st, body)
}

// C3: a person transferred to the sibling facility on central while the hub is
// offline, then edited offline. The program is protected: only the owner's staff may
// write. Must hold: refused on push, kept, shown; the person leaves the hub.
func TestC3TransferredOnCentralThenEditedOffline(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("ownership on a real DHIS2; the fake's is in internal/live")
	}
	p := s.protected()
	stage := p.Stages[0]
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvC3a")
	b := s.register(nurse, p, "AdvC3b")
	s.Sync()

	s.Offline()
	ev, evB := tk.UID(), tk.UID()
	for _, x := range []struct {
		what    string
		payload map[string]any
	}{
		{"name changed offline", personEdit(p, a, s.M.Facility.ID, map[string]string{p.SearchAttr: "AdvC3edited"})},
		{"an event offline", s.event(p, stage, a.Enrollment, ev, p.EventValues[stage])},
		{"another person's event", s.event(p, stage, b.Enrollment, evB, p.EventValues[stage])},
	} {
		if st, _ := s.save(nurse, x.what, x.payload); st != 200 {
			t.Fatal("the hub refused valid work offline")
		}
	}
	s.TransferOwnership(a.TE, p.ID, s.M.Sibling.ID)

	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{ev: tk.RefusedKept, evB: tk.OnCentral}, tk.OnCentral, tk.RefusedKept)
	s.shows(nurse, "events", ev)
	t.Logf("owner on central %s; on the hub %s", s.CentralOwner(a.TE, p.ID), s.hubOwner(a.Enrollment))
	s.Offline()
	if st, _ := nurse.Do("GET", "/api/tracker/trackedEntities/"+a.TE+"?program="+p.ID+"&fields=trackedEntity", nil); st == 200 {
		t.Errorf("I3: the person transferred away still opens in %s on the hub offline", p.ID)
	}
}

// hubOwner is the org unit the hub holds as an enrollment's owner.
func (s *scene) hubOwner(enrollment string) string {
	if o := s.App.Hub.Index.Get(enrollment); o != nil {
		return o.OwnerOrgUnit
	}
	return "(not held)"
}

// C4: a person transferred through the hub while central transfers them elsewhere.
// Must hold: one owner at the end, the one central holds, and the hub shows the
// person offline only when central shows them to the nurse.
//
// x: through the hub to the sibling, then on central to a third facility.
// z: on central to the sibling, then through the hub (not yet synced) to a third.
func TestC4TransferredThroughHubAndOnCentral(t *testing.T) {
	s, p, transfer, third := transferScene(t)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	x := s.register(nurse, p, "AdvC4x")
	z := s.register(nurse, p, "AdvC4z")
	s.Sync()

	if transfer(x, s.M.Sibling.ID) != 200 {
		t.Fatal("a transfer through the hub online failed")
	}
	s.TransferOwnership(x.TE, p.ID, third)
	s.TransferOwnership(z.TE, p.ID, s.M.Sibling.ID)
	if st := transfer(z, third); st == 200 {
		t.Errorf("central took the nurse's transfer of %s, owned by the sibling", z.TE)
	}
	s.reconnect(2)
	s.sameOwner(p, x, z)
	s.expect(nil, tk.OnCentral)
}

// y: through the hub to the sibling, then on central back to the facility. Central
// gives the person a new updatedAt at the transfer, but another user's type pull
// fetched them first, so the program's pull found them current and the hub held them
// as the sibling's, hidden offline, even after a full pull (C4, FINDINGS.md). The
// ownership check now finds them.
func TestC4TransferredBackOnCentral(t *testing.T) {
	s, p, transfer, _ := transferScene(t)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	y := s.register(nurse, p, "AdvC4y")
	s.Sync()

	if transfer(y, s.M.Sibling.ID) != 200 {
		t.Fatal("a transfer through the hub online failed")
	}
	s.TransferOwnership(y.TE, p.ID, s.M.Facility.ID)
	s.reconnect(2)
	s.sameOwner(p, y)
}

// transferScene sets a hub up for the C4 scenarios: the protected program, a client
// call transferring a person through the hub as the nurse, and a third facility of
// the program.
func transferScene(t *testing.T) (*scene, tk.Program, func(*tk.Person, string) int, string) {
	s := begin(t)
	if !s.Live() {
		t.Skip("ownership on a real DHIS2; the fake's is in internal/live")
	}
	p := s.protected()
	var prog struct {
		OrganisationUnits []struct{ ID string } `json:"organisationUnits"`
	}
	if err := s.Central.JSON("GET", "/api/programs/"+p.ID+"?fields=organisationUnits[id]", nil, &prog); err != nil {
		t.Fatal(err)
	}
	third := ""
	for _, ou := range prog.OrganisationUnits {
		if ou.ID != s.M.Facility.ID && ou.ID != s.M.Sibling.ID && third == "" {
			third = ou.ID
		}
	}
	if third == "" {
		t.Skip("the protected program is at no third facility")
	}
	nurse := s.AndroidUser(s.M.Users[tk.RoleNurse].Username)
	transfer := func(pr *tk.Person, ou string) int {
		st, b := nurse.Do("PUT", "/api/tracker/ownership/transfer?"+url.Values{"trackedEntity": {pr.TE}, "program": {p.ID},
			s.App.Hub.API().TransferOrgUnit(): {ou}}.Encode(), nil)
		t.Logf("transfer %s to %s through the hub: %d %.300s", pr.TE, ou, st, b)
		return st
	}
	return s, p, transfer, third
}

// sameOwner checks that the nurse opens each person in p on the hub offline exactly
// when central's owner is the facility, and logs both owners. It reports whether the
// hub agreed with central on all.
func (s *scene) sameOwner(p tk.Program, people ...*tk.Person) bool {
	s.t.Helper()
	nurse := s.AndroidUser(s.M.Users[tk.RoleNurse].Username)
	same := true
	for _, pr := range people {
		owner := s.CentralOwner(pr.TE, p.ID)
		path := "/api/tracker/trackedEntities/" + pr.TE + "?program=" + p.ID + "&fields=trackedEntity"
		cst, _, _ := s.Central.As(nurse.User, s.M.Password).Do("GET", path, nil)
		s.Offline()
		hst, _ := nurse.Do("GET", path, nil)
		s.Online()
		s.t.Logf("%s: owner on central %s, on the hub %q; the nurse opens them on central %d, on the hub offline %d",
			pr.TE, owner, s.hubOwner(pr.Enrollment), cst, hst)
		if hst == 200 && cst != 200 {
			s.t.Errorf("I3: %s opens on the hub offline, not on central (owner %s)", pr.TE, owner)
		}
		if (owner == s.M.Facility.ID) != (hst == 200) {
			s.t.Errorf("%s: owned by %s on central, the hub offline answers %d", pr.TE, owner, hst)
			same = false
		}
	}
	return same
}
