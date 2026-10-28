package adversarial

import (
	"net/url"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// C10: a one-time referral, as Capture makes it ("related stages", schedule in an org
// unit): an event at the referring facility, a scheduled event in the referred stage at
// the receiving facility, and a relationship from the first to the second. The owner
// stays where it was. Must hold: the receiving facility sees what central lets it see
// and no more, online and offline; the referral's event reaches central once; the owner
// does not change.
//
// To the facility: sibling staff (the outsider, on central) refer people the sibling
// owns, in an open and a protected program; the nurse at the hub's facility opens and
// completes the referred event. From the facility: the nurse refers a person the
// facility owns to the sibling through the hub, online and offline; the sibling
// completes it on central and the hub pulls it.
func TestC10OneTimeReferral(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("referrals on a real DHIS2")
	}
	m := s.M
	open, prot := m.Primary(), s.protected()
	openFrom, openTo := open.Stages[0], open.Stages[len(open.Stages)-1]
	protStage := prot.Stages[len(prot.Stages)-1] // repeatable: the referral and its origin
	var rels []string                            // the referrals' relationships, removed at the end with their types
	openType := s.referralType(openFrom, openTo, &rels)
	protType := s.referralType(protStage, protStage, &rels)
	nurse := s.connect(m.Users[tk.RoleNurse].Username)
	outsider := s.Central.As(m.Users[tk.RoleOutsider].Username, m.Password)
	today := time.Now().UTC().Format("2006-01-02")

	// To the facility. People the sibling owns, registered there, referred by sibling
	// staff on central.
	type case_ struct {
		p                  tk.Program
		pr                 *tk.Person
		origin, referred   string
		fromStage, toStage string
	}
	var in []case_
	for _, c := range []struct {
		p        tk.Program
		rt       string
		from, to string
	}{{open, openType, openFrom, openTo}, {prot, protType, protStage, protStage}} {
		pr := m.NewPerson(c.p, "AdvC10in")
		s.atSibling(pr)
		s.CentralComplete(c.p, pr)
		s.CentralImport(pr.Payload)
		o, r, payload := s.referral(c.p, c.rt, pr.Enrollment, c.from, c.to, m.Sibling.ID, m.Facility.ID, &rels)
		st, b, err := outsider.Do("POST", tk.SyncImport, payload)
		t.Logf("%s: the sibling refers %s to the facility on central: %d %v %.300s", c.p.ID, pr.TE, st, err, b)
		if st != 200 {
			t.Fatalf("central refused the sibling's referral")
		}
		in = append(in, case_{c.p, pr, o, r, c.from, c.to})
	}
	s.Sync()

	// What the nurse sees of the referral, through the hub and on central: online the
	// hub passes the request on; offline it must not show more than central.
	probes := func(c case_) []string {
		return []string{
			"/api/tracker/events/" + c.referred + "?fields=event,status,orgUnit",
			"/api/tracker/trackedEntities/" + c.pr.TE + "?program=" + c.p.ID + "&fields=trackedEntity",
			"/api/tracker/enrollments/" + c.pr.Enrollment + "?fields=enrollment",
		}
	}
	// compare returns what the hub lists that central does not.
	compare := func(c *tk.Client, paths []string) (extra []string) {
		t.Helper()
		for _, path := range paths {
			hst, _ := c.Do("GET", path, nil)
			cst, cb, _ := s.Central.As(c.User, m.Password).Do("GET", path, nil)
			t.Logf("as %s (online=%v): %s: hub %d, central %d %.120s", c.User, !s.Net.Down(), path, hst, cst,
				map[bool][]byte{true: cb}[cst != 200])
			if hst == 200 && cst != 200 {
				t.Errorf("I3: %s through the hub (online=%v) gives %s what central does not (%d)", path, !s.Net.Down(), c.User, cst)
			}
		}
		for _, p := range []tk.Program{open, prot} {
			lists := []string{
				"/api/tracker/events?orgUnit=" + m.Facility.ID + "&program=" + p.ID + "&fields=event&paging=false",
				"/api/tracker/trackedEntities?orgUnits=" + m.Facility.ID + "&program=" + p.ID + "&fields=trackedEntity&paging=false",
			}
			for i, l := range lists {
				coll, key := "events", "event"
				if i == 1 {
					coll, key = "trackedEntities", "trackedEntity"
				}
				if x := s.leaks(c, l, coll, key); len(x) > 0 {
					t.Logf("I3: %s lists %v for %s; central does not", l, x, c.User)
					extra = append(extra, x...)
				}
			}
		}
		return extra
	}
	for _, down := range []bool{false, true} {
		if down {
			s.Offline()
		}
		for _, c := range in {
			if x := compare(nurse, probes(c)); len(x) > 0 {
				t.Errorf("I3: the hub (online=%v) lists %v that central does not list to the nurse", !s.Net.Down(), x)
			}
		}
	}

	// The nurse completes the referred event in the open program: offline first, as a
	// client that cached it would, then online.
	complete := func(c case_) map[string]any {
		return map[string]any{"events": []any{map[string]any{"event": c.referred, "enrollment": c.pr.Enrollment,
			"program": c.p.ID, "programStage": c.toStage, "orgUnit": m.Facility.ID, "status": "COMPLETED",
			"scheduledAt": today, "occurredAt": today, "dataValues": values(c.p.EventValues[c.toStage])}}}
	}
	st, b := s.save(nurse, "the nurse completes the referred event offline", complete(in[0]))
	if st == 200 {
		t.Logf("the hub took the completion of a referred event offline")
	} else {
		t.Logf("offline the hub does not take the completion (%d %v): the receiving facility completes a referral online only", st, errorCodes(b))
	}
	s.Online()
	if st, b := s.save(nurse, "the nurse completes the referred event online", complete(in[0])); st != 200 {
		t.Errorf("completing the referred event through the hub online: %d %.300s", st, b)
	}
	for _, c := range in {
		if o := s.CentralOwner(c.pr.TE, c.p.ID); o != m.Sibling.ID {
			t.Errorf("%s: the owner on central is %s, want the sibling %s", c.p.ID, o, m.Sibling.ID)
		}
	}

	// From the facility: a person the facility owns, referred to the sibling through
	// the hub online, and another offline.
	a := s.register(nurse, open, "AdvC10outA")
	z := s.register(nurse, open, "AdvC10outZ")
	s.Sync()
	_, refA, payA := s.referral(open, openType, a.Enrollment, openFrom, openTo, m.Facility.ID, m.Sibling.ID, &rels)
	if st, b := s.save(nurse, "a referral to the sibling online", payA); st != 200 {
		t.Errorf("the hub refused a referral to the sibling online: %d %.300s", st, b)
	}
	s.Offline()
	_, refZ, payZ := s.referral(open, openType, z.Enrollment, openFrom, openTo, m.Facility.ID, m.Sibling.ID, &rels)
	stZ, bZ := s.save(nurse, "a referral to the sibling offline", payZ)
	t.Logf("offline referral to the sibling: %d %v", stZ, errorCodes(bZ))
	s.noRepeats(s.reconnect(3))

	// The sibling completes the referral on central; the hub pulls it with the person.
	refOut := case_{open, a, "", refA, openFrom, openTo}
	done := complete(refOut)
	done["events"].([]any)[0].(map[string]any)["orgUnit"] = m.Sibling.ID
	if st, b, err := outsider.Do("POST", tk.SyncImport, done); st != 200 {
		t.Errorf("the sibling completing the referral on central: %d %v %.300s", st, err, b)
	}
	s.Sync()
	s.expect(map[string]string{refA: tk.ChangedOnCentral}, tk.OnCentral, tk.ChangedOnCentral)
	for _, r := range []struct {
		pr  *tk.Person
		ref string
		saw int
	}{{a, refA, 200}, {z, refZ, stZ}} {
		n := s.referredEvents(r.pr.Enrollment, openTo, m.Sibling.ID)
		want := 0
		if r.saw == 200 {
			want = 1
		}
		t.Logf("%s: %d referred event(s) at the sibling on central, the hub answered %d", r.pr.TE, n, r.saw)
		if n != want {
			t.Errorf("the referral's event is on central %d times, want %d", n, want)
		}
		if o := s.CentralOwner(r.pr.TE, open.ID); o != m.Facility.ID {
			t.Errorf("%s: the owner on central is %s after a referral, want the facility", r.pr.TE, o)
		}
	}
	var rel struct {
		Relationships []struct{ Relationship string } `json:"relationships"`
	}
	s.Central.JSON("GET", "/api/tracker/relationships?event="+refA+"&fields=relationship", nil, &rel)
	t.Logf("relationships to the referral on central: %d", len(rel.Relationships))
	if len(rel.Relationships) != 1 {
		t.Errorf("the referral's relationship is on central %d times, want once", len(rel.Relationships))
	}
	s.Offline()
	st, b = nurse.Do("GET", "/api/tracker/events/"+refA+"?fields=status,orgUnit", nil)
	t.Logf("offline, the completed referral as the nurse sees it: %d %.200s", st, b)
	extra := append(compare(nurse, probes(refOut)), compare(nurse, probes(in[0]))...)
	var others []string
	for _, x := range extra {
		if x != in[0].referred {
			others = append(others, x)
		}
	}
	if len(others) > 0 {
		t.Errorf("I3: offline the hub lists %v that central does not list to the nurse", others)
	}
	if len(extra) > len(others) {
		// Central lists a program's events by the owner's org unit, so the referral,
		// whose person the sibling owns, is not in the facility's list; the nurse
		// opens it by its uid. The hub keeps what the nurse saved online and lists it
		// by the event's org unit.
		finding(t, "C10: a referred-in event the nurse completed online is listed offline in the facility's events; central does not list it there (it opens it by uid)")
		t.Errorf("I3: offline the hub lists the referred-in event %s in the facility's events; central does not", in[0].referred)
	}
}

// referral is the payload Capture sends for a one-time referral: the origin event at
// fromOU, the referred event scheduled in toStage at toOU, and the relationship from
// the first to the second (its uid added to rels).
func (s *scene) referral(p tk.Program, rt, enr, fromStage, toStage, fromOU, toOU string, rels *[]string) (origin, referred string, payload map[string]any) {
	origin, referred = tk.UID(), tk.UID()
	rel := tk.UID()
	*rels = append(*rels, rel)
	ev := s.event(p, fromStage, enr, origin, p.EventValues[fromStage])["events"].([]any)[0].(map[string]any)
	ev["orgUnit"] = fromOU
	payload = map[string]any{
		"events": []any{ev, map[string]any{"event": referred, "enrollment": enr, "program": p.ID,
			"programStage": toStage, "orgUnit": toOU, "status": "SCHEDULE",
			"scheduledAt": time.Now().UTC().Format("2006-01-02")}},
		"relationships": []any{map[string]any{"relationship": rel, "relationshipType": rt,
			"from": map[string]any{"event": map[string]any{"event": origin}},
			"to":   map[string]any{"event": map[string]any{"event": referred}}}},
	}
	return
}

// atSibling moves a new person's payload to the sibling facility: registered and
// enrolled there, so the sibling owns them.
func (s *scene) atSibling(pr *tk.Person) {
	te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
	te["orgUnit"] = s.M.Sibling.ID
	for _, en := range te["enrollments"].([]any) {
		en.(map[string]any)["orgUnit"] = s.M.Sibling.ID
	}
}

func values(v map[string]string) []any {
	out := []any{}
	for de, val := range v {
		out = append(out, map[string]any{"dataElement": de, "value": val})
	}
	return out
}

// referralType makes an event-to-event relationship type between two stages, as
// Capture's related stages need, readable and writable by all. Removed at the end,
// with the relationships in rels: central keeps deleted relationships, which hold the
// type, until its maintenance removes them.
func (s *scene) referralType(from, to string, rels *[]string) string {
	s.t.Helper()
	id := tk.UID()
	constraint := func(stage string) map[string]any {
		return map[string]any{"relationshipEntity": "PROGRAM_STAGE_INSTANCE", "programStage": map[string]string{"id": stage}}
	}
	if st, b, err := s.Central.Do("POST", "/api/relationshipTypes", map[string]any{"id": id,
		"name": "Adversarial referral " + id, "fromToName": "Refers to", "toFromName": "Referred from",
		"bidirectional": false, "sharing": map[string]any{"public": "rwrw----"},
		"fromConstraint": constraint(from), "toConstraint": constraint(to)}); err != nil || st/100 != 2 {
		s.t.Fatalf("relationship type: %d %v %.300s", st, err, b)
	}
	s.t.Cleanup(func() {
		var del []any
		for _, r := range *rels {
			del = append(del, map[string]any{"relationship": r})
		}
		if len(del) > 0 {
			s.Central.Do("POST", "/api/tracker?async=false&importStrategy=DELETE&atomicMode=OBJECT", map[string]any{"relationships": del})
		}
		s.Central.Do("POST", "/api/maintenance/softDeletedRelationshipRemoval", nil)
		if st, b, err := s.Central.Do("DELETE", "/api/relationshipTypes/"+id, nil); err != nil || st/100 != 2 {
			s.t.Errorf("relationship type %s not removed: %d %v %.300s", id, st, err, b)
		}
	})
	return id
}

// referredEvents counts the events on central in an enrollment's stage at an org unit.
func (s *scene) referredEvents(enrollment, stage, ou string) int {
	s.t.Helper()
	var evs struct {
		Events []struct{ ProgramStage, OrgUnit string } `json:"events"`
	}
	if err := s.Central.JSON("GET", "/api/tracker/events?"+url.Values{"enrollments": {enrollment},
		"fields": {"programStage,orgUnit"}, "paging": {"false"}}.Encode(), nil, &evs); err != nil {
		s.t.Fatal(err)
	}
	n := 0
	for _, e := range evs.Events {
		if e.ProgramStage == stage && strings.EqualFold(e.OrgUnit, ou) {
			n++
		}
	}
	return n
}

// C10 in the protected program: sibling staff refer a person the sibling owns to the
// facility. The nurse cannot open the referral until she breaks the glass (temporary
// ownership, on central through the hub online); then she opens the referred event and
// completes it, as Capture would. Offline the hub must not show what she reached
// through temporary ownership: it must not outlive the connection (core.UpsertCentral),
// and the hub cannot tell when central ends it.
func TestC10ProtectedAfterBreakingTheGlass(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("referrals on a real DHIS2")
	}
	m := s.M
	p := s.protected()
	stage := p.Stages[len(p.Stages)-1]
	var rels []string
	rt := s.referralType(stage, stage, &rels)
	nurse := s.connect(m.Users[tk.RoleNurse].Username)
	asNurse := s.Central.As(nurse.User, m.Password)

	pr := m.NewPerson(p, "AdvC10glass")
	s.atSibling(pr)
	s.CentralComplete(p, pr)
	s.CentralImport(pr.Payload)
	_, ref, payload := s.referral(p, rt, pr.Enrollment, stage, stage, m.Sibling.ID, m.Facility.ID, &rels)
	if st, b, err := s.Central.As(m.Users[tk.RoleOutsider].Username, m.Password).Do("POST", tk.SyncImport, payload); st != 200 {
		t.Fatalf("the sibling's referral on central: %d %v %.300s", st, err, b)
	}
	s.Sync()

	byUID := []string{
		"/api/tracker/events/" + ref + "?fields=*",
		"/api/tracker/trackedEntities/" + pr.TE + "?program=" + p.ID + "&fields=*",
		"/api/tracker/enrollments/" + pr.Enrollment + "?fields=*",
	}
	listing := "/api/tracker/events?orgUnit=" + m.Facility.ID + "&program=" + p.ID + "&fields=event&paging=false"
	look := func(when string) (shown []string) {
		t.Helper()
		for _, path := range byUID {
			hst, _ := nurse.Do("GET", path, nil)
			cst, _, _ := asNurse.Do("GET", path, nil)
			t.Logf("%s: %s: hub %d, central %d", when, path, hst, cst)
			if hst == 200 {
				shown = append(shown, path)
			}
		}
		if x := s.leaks(nurse, listing, "events", "event"); len(x) > 0 {
			t.Logf("%s: the hub lists %v at the facility; central does not", when, x)
			shown = append(shown, listing)
		}
		return shown
	}
	look("online, before the glass")
	st, b := nurse.Do("POST", "/api/tracker/ownership/override?trackedEntity="+pr.TE+"&program="+p.ID+
		"&reason=referred+for+follow-up", nil)
	t.Logf("break the glass through the hub: %d %.200s", st, b)
	if st != 200 {
		t.Fatal("breaking the glass through the hub failed")
	}
	look("online, after the glass") // Capture opens the person and the event: the hub sees the reads
	if st, b := s.save(nurse, "the nurse completes the referral online, after the glass", map[string]any{
		"events": []any{map[string]any{"event": ref, "enrollment": pr.Enrollment, "program": p.ID,
			"programStage": stage, "orgUnit": m.Facility.ID, "status": "COMPLETED",
			"occurredAt": time.Now().UTC().Format("2006-01-02"), "scheduledAt": time.Now().UTC().Format("2006-01-02"),
			"dataValues": values(p.EventValues[stage])}}}); st != 200 {
		t.Errorf("completing the referral through the hub online: %d %.300s", st, b)
	}
	if o := s.CentralOwner(pr.TE, p.ID); o != m.Sibling.ID {
		t.Errorf("the owner on central is %s, want the sibling", o)
	}
	s.Offline()
	if shown := look("offline"); len(shown) > 0 {
		finding(t, "C10: a referred-in event of a protected program, completed online after breaking the glass, is served and listed offline; central answers the nurse 404 for it")
		t.Errorf("I3: offline the hub shows what the nurse reached by temporary ownership: %v", shown)
	}
}
