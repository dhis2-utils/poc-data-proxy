package adversarial

import (
	"net/url"
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// centralCount is how many people central holds in program p with attribute attr
// equal to value, at the facility.
func (s *scene) centralCount(p tk.Program, attr, value string) int {
	s.t.Helper()
	var found struct {
		TEs []struct{ TrackedEntity string } `json:"trackedEntities"`
	}
	q := url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID}, "filter": {attr + ":eq:" + value}, "fields": {"trackedEntity"}}
	if err := s.Central.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &found); err != nil {
		s.t.Fatal(err)
	}
	return len(found.TEs)
}

// C6: the same person registered offline on one hub by two members of staff. Must
// hold: as C5 on one hub: both pushed or one refused by uniqueness; record which; no
// merge loss. x: both registrations carry the same unique number; y: each staff
// member types the name alone (different numbers).
func TestC6DuplicateByTwoStaffOffline(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("uniqueness on a real DHIS2")
	}
	p := s.M.Primary()
	if p.UniqueAttr == "" {
		t.Skip("the primary program has no unique attribute")
	}
	n1 := s.connect(s.M.Users[tk.RoleNurse].Username)
	n2 := s.connect(s.NewUserLike(tk.RoleNurse).Username)
	s.Sync()
	tag := strings.ToLower(tk.UID()[:6])

	s.Offline()
	x1, x2 := s.M.NewPerson(p, "AdvC6x"+tag), s.M.NewPerson(p, "AdvC6x"+tag)
	x2.SetAttr(p.UniqueAttr, x1.Unique)
	y1, y2 := s.M.NewPerson(p, "AdvC6y"+tag), s.M.NewPerson(p, "AdvC6y"+tag)
	got := map[string]int{}
	for _, r := range []struct {
		c    *tk.Client
		pr   *tk.Person
		what string
	}{{n1, x1, "x by the first nurse"}, {n2, x2, "x by the second, the same number"}, {n1, y1, "y by the first"}, {n2, y2, "y by the second, another number"}} {
		r.c.Complete(p, r.pr)
		st, b := s.save(r.c, r.what, r.pr.Payload)
		got[r.what] = st
		if st != 200 {
			t.Logf("refused by the hub at once: %v", errorCodes(b))
		}
	}
	s.noRepeats(s.reconnect(3))
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
	xs, ys := s.centralCount(p, p.UniqueAttr, x1.Unique), s.centralCount(p, p.SearchAttr, "AdvC6y"+tag)
	t.Logf("the hub answered %v; on central: the shared number %d time(s), the name typed twice %d time(s)", got, xs, ys)
	if xs != 1 {
		t.Errorf("I2: the unique number is on central %d times", xs)
	}
	if got["y by the first"] != 200 || got["y by the second, another number"] != 200 || ys != 2 {
		t.Errorf("two registrations with different numbers: want both taken and on central (central decides duplicates), got %v and %d", got, ys)
	}
}

// relationship is a payload of one relationship between two people.
func relationship(uid, rt, from, to string) map[string]any {
	return map[string]any{"relationships": []any{map[string]any{"relationship": uid, "relationshipType": rt,
		"from": map[string]any{"trackedEntity": map[string]any{"trackedEntity": from}},
		"to":   map[string]any{"trackedEntity": map[string]any{"trackedEntity": to}}}}}
}

// personRelationshipType is a relationship type between two people of the program's
// type.
func (s *scene) personRelationshipType(p tk.Program) string {
	s.t.Helper()
	var r struct {
		RelationshipTypes []struct {
			ID                           string
			FromConstraint, ToConstraint struct {
				RelationshipEntity string
				TrackedEntityType  *struct{ ID string }
			}
		} `json:"relationshipTypes"`
	}
	if err := s.Central.JSON("GET", "/api/relationshipTypes?fields=id,fromConstraint,toConstraint&paging=false", nil, &r); err != nil {
		s.t.Fatal(err)
	}
	for _, x := range r.RelationshipTypes {
		f, to := x.FromConstraint, x.ToConstraint
		if f.RelationshipEntity == "TRACKED_ENTITY_INSTANCE" && to.RelationshipEntity == "TRACKED_ENTITY_INSTANCE" &&
			f.TrackedEntityType != nil && f.TrackedEntityType.ID == p.TET && to.TrackedEntityType != nil && to.TrackedEntityType.ID == p.TET {
			return x.ID
		}
	}
	s.t.Skip("no relationship type between two people of the program's type")
	return ""
}

// C7: a relationship to a person the hub does not hold (x, registered and owned at
// the sibling, in the nurse's search scope). Must hold: refused clearly or accepted as
// central; a relationship between two people the hub holds goes through.
func TestC7RelationshipToPersonNotHeld(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("relationships on a real DHIS2")
	}
	p := s.M.Primary()
	rt := s.personRelationshipType(p)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvC7a")
	b := s.register(nurse, p, "AdvC7b")
	x := s.M.NewPerson(p, "AdvC7x")
	s.atSibling(x)
	s.CentralComplete(p, x)
	s.CentralImport(x.Payload)
	s.Sync()
	var made []string
	t.Cleanup(func() {
		var del []any
		for _, r := range made {
			del = append(del, map[string]any{"relationship": r})
		}
		if len(del) > 0 {
			s.Central.Do("POST", "/api/tracker?async=false&importStrategy=DELETE&atomicMode=OBJECT", map[string]any{"relationships": del})
		}
	})

	s.Offline()
	toX, toB, onlineToX := tk.UID(), tk.UID(), tk.UID()
	made = append(made, toX, toB, onlineToX)
	stX, bX := s.save(nurse, "offline, a to x (not held)", relationship(toX, rt, a.TE, x.TE))
	stB, bB := s.save(nurse, "offline, a to b (both held)", relationship(toB, rt, a.TE, b.TE))
	t.Logf("offline: to a person not held %d %v; between two held %d %v", stX, errorCodes(bX), stB, errorCodes(bB))
	if stB != 200 {
		t.Errorf("the hub refuses a relationship between two people it holds offline")
	}
	if stX == 200 {
		t.Logf("the hub took a relationship to a person it does not hold offline")
	} else if !strings.Contains(string(bX), "needs connection") {
		t.Errorf("the refusal of a relationship to a person not held does not say it needs a connection: %.300s", bX)
	}
	// Online while the hub still holds the offline save, the hub answers writes itself
	// (core.PassThrough); after the push it passes them to central.
	s.Online()
	queuedToX := tk.UID()
	made = append(made, queuedToX)
	stQ, bQ := s.save(nurse, "online with the outbox not yet pushed, b to x", relationship(queuedToX, rt, b.TE, x.TE))
	s.noRepeats(s.reconnect(3))
	stO, bO := s.save(nurse, "online, the outbox pushed, b to x", relationship(onlineToX, rt, b.TE, x.TE))
	cst, _, _ := s.Central.As(nurse.User, s.M.Password).Do("GET", "/api/tracker/relationships/"+onlineToX, nil)
	t.Logf("online through the hub, to a person not held: before the push %d %v; after it %d %v; central then answers the nurse %d for it",
		stQ, errorCodes(bQ), stO, errorCodes(bO), cst)
	s.expect(nil, tk.OnCentral)
	for uid, want := range map[string]bool{toX: stX == 200, toB: true, onlineToX: stO == 200, queuedToX: stQ == 200} {
		on := s.CentralObject("relationships", uid) != nil
		t.Logf("relationship %s on central: %v (taken: %v)", uid, on, want)
		if on != want {
			t.Errorf("I1/I2: relationship %s on central %v, the hub confirmed it %v", uid, on, want)
		}
	}
	s.Offline()
	for _, te := range []string{a.TE, b.TE} {
		q := "/api/tracker/relationships?trackedEntity=" + te + "&fields=relationship"
		if l := s.leaks(nurse, q, "relationships", "relationship"); len(l) > 0 {
			t.Errorf("I3: offline the hub shows relationships of %s central does not: %v", te, l)
		}
	}
}

// C8: the nurse breaks the glass online through the hub, for a person the sibling
// owns in the protected program, reads and writes them, and the hub syncs while the
// temporary ownership holds; then the uplink drops. Must hold: the temporary access
// ends as central's does; nothing more is shown offline: the hub never shows offline
// what she reached by breaking the glass.
func TestC8BreakTheGlassThenOffline(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("ownership on a real DHIS2; the fake's is TestBreakTheGlass")
	}
	p := s.protected()
	stage := p.Stages[0]
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	x := s.M.NewPerson(p, "AdvC8x", stage)
	s.atSibling(x)
	ev0 := x.Payload["trackedEntities"].([]any)[0].(map[string]any)["enrollments"].([]any)[0].(map[string]any)["events"].([]any)[0].(map[string]any)
	ev0["orgUnit"] = s.M.Sibling.ID
	s.CentralComplete(p, x)
	s.CentralImport(x.Payload)
	s.Sync()
	paths := []string{
		"/api/tracker/trackedEntities/" + x.TE + "?program=" + p.ID + "&fields=trackedEntity",
		"/api/tracker/trackedEntities/" + x.TE + "?fields=trackedEntity",
		"/api/tracker/enrollments/" + x.Enrollment + "?fields=enrollment",
		"/api/tracker/events/" + x.Events[0] + "?fields=event",
		"/api/tracker/events?enrollments=" + x.Enrollment + "&fields=event",
		"/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID}, "filter": {p.SearchAttr + ":eq:AdvC8x"},
			"fields": {"trackedEntity"}}.Encode(),
	}
	s.sameAsCentral(nurse, "before the glass, online", paths...)
	st, b := nurse.Do("POST", "/api/tracker/ownership/override?trackedEntity="+x.TE+"&program="+p.ID+"&reason=emergency", nil)
	t.Logf("break the glass through the hub: %d %.200s", st, b)
	if st != 200 {
		t.Fatal("breaking the glass through the hub failed")
	}
	s.sameAsCentral(nurse, "after the glass, online", paths...)
	ev, later := tk.UID(), p.Stages[len(p.Stages)-1] // the last stage is repeatable
	if st, b := s.save(nurse, "an event for x online, after the glass", s.event(p, later, x.Enrollment, ev, p.EventValues[later])); st != 200 {
		t.Errorf("writing through the hub after breaking the glass: %d %.300s", st, b)
	}
	s.reconnect(2) // pulls and the ownership check while the temporary ownership holds

	s.Offline()
	byUID, lists := append(paths[:4:4], "/api/tracker/events/"+ev+"?fields=event"), paths[4:]
	shown := 0
	for _, h := range s.sameAsCentral(nurse, "after the glass, offline", byUID...) {
		if h == 200 {
			shown++
		}
	}
	for _, l := range lists {
		st, body := nurse.Do("GET", l, nil)
		t.Logf("offline: %s: %d %.200s", l, st, body)
		for _, uid := range []string{x.TE, x.Events[0], ev} {
			if strings.Contains(string(body), uid) {
				shown++
			}
		}
	}
	if shown > 0 {
		t.Errorf("I3: offline the hub shows %d things the nurse reached by breaking the glass", shown)
	}
	if st, b := s.save(nurse, "an event for x offline", s.event(p, later, x.Enrollment, tk.UID(), p.EventValues[later])); st == 200 {
		t.Errorf("offline the hub takes an event for a person reached by breaking the glass: %.200s", b)
	}
	s.Online()
	s.expect(nil, tk.OnCentral)
	if o := s.CentralOwner(x.TE, p.ID); o != s.M.Sibling.ID {
		t.Errorf("the owner on central is %s, want the sibling", o)
	}
}

// C9: the enrollment is completed on central while events for it are added offline.
// Must hold: as central decides; refusals kept; and after the sync the hub answers a
// new offline event as central answers the same event online.
func TestC9EnrollmentCompletedOnCentral(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("enrollment status on a real DHIS2")
	}
	p := s.M.Primary()
	stage := p.Stages[0]
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvC9a")
	s.Sync()

	s.Offline()
	active, done := tk.UID(), tk.UID()
	if st, _ := s.save(nurse, "an active event offline", s.event(p, stage, a.Enrollment, active, p.EventValues[stage])); st != 200 {
		t.Fatal("the hub refused valid work offline")
	}
	if st, _ := s.save(nurse, "a completed event offline", completed(s.event(p, stage, a.Enrollment, done, p.EventValues[stage]))); st != 200 {
		t.Fatal("the hub refused valid work offline")
	}
	s.CentralImport(s.enrollment(p, a.TE, a.Enrollment, "COMPLETED"))
	s.noRepeats(s.reconnect(3))
	// The registration's enrollment differs on central in its status alone: central
	// completed it.
	completedOnCentral := map[string]string{a.Enrollment: tk.ChangedOnCentral}
	s.expect(completedOnCentral, tk.OnCentral, tk.RefusedKept)
	enr := s.CentralObject("enrollments", a.Enrollment)
	t.Logf("the enrollment on central after the push: %v", enr["status"])
	if enr["status"] != "COMPLETED" {
		t.Errorf("I4: the enrollment central completed is %v after the push", enr["status"])
	}

	s.Offline()
	st, b := nurse.Do("GET", "/api/tracker/enrollments/"+a.Enrollment+"?fields=status", nil)
	t.Logf("offline, the hub shows the enrollment: %d %.100s", st, b)
	if !strings.Contains(string(b), "COMPLETED") {
		t.Errorf("after the pull the hub does not show the enrollment completed offline")
	}
	hst, hb := s.save(nurse, "another event offline, after the sync", s.event(p, stage, a.Enrollment, tk.UID(), p.EventValues[stage]))
	s.Online()
	direct := tk.UID()
	cst, cb, _ := s.Central.As(nurse.User, s.M.Password).Do("POST", tk.SyncImport, s.event(p, stage, a.Enrollment, direct, p.EventValues[stage]))
	if cst == 200 {
		s.CentralDelete("events", direct)
	}
	t.Logf("a new event in the completed enrollment: the hub offline %d %v, central online %d %v", hst, errorCodes(hb), cst, errorCodes(cb))
	if (hst == 200) != (cst == 200) {
		t.Errorf("the hub offline and central disagree on an event in a completed enrollment")
	}
	s.reconnect(1)
	s.expect(completedOnCentral, tk.OnCentral, tk.RefusedKept)
}
