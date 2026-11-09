package adversarial

import (
	"encoding/json"
	"maps"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Scenarios A17–A23 of docs/ADVERSARIAL-TESTING.md: more of central's metadata
// changing while the hub holds offline work.

// datedPerson is a registration in p enrolled on enrolled with its incident on
// incident, without events.
func (s *scene) datedPerson(c *tk.Client, p tk.Program, name, enrolled, incident string) *tk.Person {
	s.t.Helper()
	pr := s.M.NewPerson(p, name)
	c.Complete(p, pr)
	enr := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)["enrollments"].([]any)[0].(map[string]any)
	enr["enrolledAt"], enr["occurredAt"] = enrolled, incident
	return pr
}

// futureAllowed is the hub's copy of the program's two future-date settings.
func (s *scene) futureAllowed(p tk.Program) string {
	pm := s.App.Hub.Catalog().Programs[p.ID]
	show := func(b *bool) string {
		if b == nil {
			return "unset"
		}
		if *b {
			return "allowed"
		}
		return "refused"
	}
	return "enrollment " + show(pm.SelectEnrollmentDatesInFuture) + ", incident " + show(pm.SelectIncidentDatesInFuture)
}

// A21: the program's "allow future enrollment/incident dates" settings change during
// an outage. The hub checks them offline from its last copy (F4, checkEnrollmentDates).
// Turned off: the hub takes a registration dated tomorrow offline; central refuses it
// at push (E1020, E1021) and the hub keeps it. Turned on: the hub refuses offline what
// central now takes, until its catalog follows; the catalog is re-read with the
// access refresh (AccessRefreshInterval, an hour by default), after the push in a
// cycle. The test measures that wait with the hub set to the default hour.
func TestA21FutureDatesSettingChanges(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("the fake central's future dates are tested in internal/live (TestFutureEnrollmentDates)")
	}
	p := s.M.Primary()
	prog := "/api/programs/" + p.ID
	today := s.App.Hub.CentralNow()
	if today.Add(10*time.Minute).Day() != today.Day() {
		t.Skip("central's date changes within ten minutes")
	}
	day, tomorrow := today.Format(time.DateOnly), today.AddDate(0, 0, 1).Format(time.DateOnly)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	set := func(v bool) {
		s.SetField(prog, "selectEnrollmentDatesInFuture", v)
		s.SetField(prog, "selectIncidentDatesInFuture", v)
	}

	// Turned off during the outage.
	set(true)
	s.Sync()
	t.Logf("before the outage the hub holds: %s", s.futureAllowed(p))
	s.Offline()
	enrolled := s.datedPerson(nurse, p, "AdvA21a", tomorrow, day)
	incident := s.datedPerson(nurse, p, "AdvA21b", day, tomorrow)
	ok := s.datedPerson(nurse, p, "AdvA21c", day, day)
	for _, pr := range []*tk.Person{enrolled, incident, ok} {
		if st, b := s.save(nurse, "registration, future dates allowed as the hub last read", pr.Payload); st != 200 {
			t.Fatalf("the hub refused offline what its copy allows: %d %v", st, errorCodes(b))
		}
	}
	set(false)
	s.noRepeats(s.reconnect(3))
	lines := s.expect(map[string]string{enrolled.Enrollment: tk.RefusedKept, incident.Enrollment: tk.RefusedKept,
		ok.Enrollment: tk.OnCentral, enrolled.TE: tk.OnCentral, incident.TE: tk.OnCentral, ok.TE: tk.OnCentral}, tk.OnCentral)
	for _, ln := range lines {
		if ln.Outcome == tk.RefusedKept {
			t.Logf("central's reason for %s: %s", ln.UID, ln.Detail)
		}
	}
	t.Logf("after the sync the hub holds: %s", s.futureAllowed(p))
	s.Offline()
	if st, b := s.save(nurse, "enrolled tomorrow, after the sync", s.datedPerson(nurse, p, "AdvA21d", tomorrow, day).Payload); st == 200 {
		t.Error("after the sync the hub takes an enrollment dated tomorrow offline where central refuses it")
	} else {
		t.Logf("after the sync the hub refuses it itself: %d %v", st, errorCodes(b))
	}
	s.Online()

	// Turned on during the outage, with the hub's access refresh at its default hour:
	// the refresh just ran, so the next one is due in an hour.
	s.App.Hub.Cfg.AccessRefreshInterval = time.Hour
	s.Offline()
	st, b := s.save(nurse, "enrolled tomorrow, settings off as the hub last read", s.datedPerson(nurse, p, "AdvA21e", tomorrow, day).Payload)
	t.Logf("offline before the change: %d %v", st, errorCodes(b))
	set(true)
	refusedNow := 0
	if st, b := s.save(nurse, "enrolled tomorrow, central now allows it", s.datedPerson(nurse, p, "AdvA21f", tomorrow, day).Payload); st != 200 {
		refusedNow++
		t.Logf("the hub refuses offline what central now takes: %d %v %.300s", st, errorCodes(b), b)
	}
	s.reconnect(2)
	t.Logf("two cycles after the uplink returns, refresh not due: the hub holds %s", s.futureAllowed(p))
	s.connect(s.M.Users[tk.RoleNurse].Username) // an online request as the nurse
	s.Offline()
	if st, b := s.save(nurse, "enrolled tomorrow, after the cycles, refresh not due", s.datedPerson(nurse, p, "AdvA21g", tomorrow, day).Payload); st != 200 {
		refusedNow++
		t.Logf("still refused offline after two cycles online: %d %v", st, errorCodes(b))
	}
	s.Online()
	s.App.Hub.Cfg.AccessRefreshInterval = 0 // the hour has passed
	s.Sync()
	t.Logf("after the hourly refresh the hub holds: %s", s.futureAllowed(p))
	s.Offline()
	later := s.datedPerson(nurse, p, "AdvA21h", tomorrow, tomorrow)
	if st, b := s.save(nurse, "enrolled tomorrow, after the refresh", later.Payload); st != 200 {
		t.Errorf("after the refresh the hub refuses offline what central allows: %d %v", st, errorCodes(b))
	}
	s.reconnect(2)
	s.expect(map[string]string{later.Enrollment: tk.OnCentral}, tk.OnCentral, tk.RefusedKept)
	if refusedNow > 0 {
		t.Logf("the hub refused %d registration(s) offline that central would take, while its copy was stale", refusedNow)
	}
}

// A22: a generated attribute's pattern changes (RANDOM to SEQUENTIAL, a new prefix)
// while the hub's pool and a tablet hold values reserved under the old one. Must hold:
// registrations with old values as central decides; no value on two people; the
// hub's pool follows the new pattern after the next sync.
func TestA22GeneratedPatternChanges(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("text patterns on a real DHIS2")
	}
	p := s.M.Primary()
	attr := p.GeneratedAttr
	if attr == "" {
		t.Skip("the primary program has no generated attribute")
	}
	path := "/api/trackedEntityAttributes/" + attr
	var meta struct{ Pattern string }
	if err := s.Central.JSON("GET", path+"?fields=pattern", nil, &meta); err != nil {
		t.Fatal(err)
	}
	now := func() string { return s.App.Hub.CentralTime(time.Now()) }
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	s.Sync()
	t.Logf("pattern %s; the hub's pool holds %d", meta.Pattern, s.App.Hub.Store.CountReservedValues(attr, now()))

	// take is a value from the hub's pool, as a tablet gets it offline.
	take := func(what string) string {
		t.Helper()
		st, b := nurse.Do("GET", path+"/generate", nil)
		var v struct{ Value string }
		json.Unmarshal(b, &v)
		t.Logf("%s: the hub answers %d %.200s", what, st, b)
		if st != 200 || v.Value == "" {
			t.Fatalf("no value from the hub's pool offline")
		}
		return v.Value
	}
	used := map[string]string{} // value -> enrollment
	register := func(name, v string) *tk.Person {
		t.Helper()
		pr := s.M.NewPerson(p, name)
		nurse.Complete(p, pr)
		pr.SetAttr(attr, v)
		if st, b := s.save(nurse, "register "+name+" with "+v, pr.Payload); st != 200 {
			t.Fatalf("the hub refused a registration offline: %d %v", st, errorCodes(b))
		}
		used[v] = pr.Enrollment
		return pr
	}

	s.Offline()
	before := register("AdvA22a", take("offline, before the change"))
	held := take("offline, held by a tablet")
	newPattern := `"PX" + SEQUENTIAL(#######)`
	s.SetField(path, "pattern", newPattern)
	var gen struct{ Value string }
	s.Central.JSON("GET", path+"/generate", nil, &gen)
	t.Logf("central's pattern now %s; central generates %q", newPattern, gen.Value)
	after := register("AdvA22b", held)

	s.noRepeats(s.reconnect(3))
	lines := s.expect(nil, tk.OnCentral, tk.RefusedKept)
	for _, ln := range lines {
		if ln.UID == before.Enrollment || ln.UID == after.Enrollment || ln.UID == before.TE || ln.UID == after.TE {
			t.Logf("%s %s: %s %s", ln.Kind, ln.UID, ln.Outcome, ln.Detail)
		}
	}
	var hubPattern string
	for _, a := range s.App.Hub.Catalog().GeneratedAttributes() {
		if a.ID == attr {
			hubPattern = a.Pattern
		}
	}
	left := s.App.Hub.Store.CountReservedValues(attr, now())
	t.Logf("after the sync: the hub's catalog pattern %s; its pool holds %d", hubPattern, left)
	if hubPattern != newPattern {
		t.Errorf("after the sync the hub's catalog has pattern %s, central %s", hubPattern, newPattern)
	}

	s.Offline()
	v := take("offline, after the sync")
	third := register("AdvA22c", v)
	oldShape := !strings.HasPrefix(v, "PX")
	s.noRepeats(s.reconnect(2))
	for _, ln := range s.expect(nil, tk.OnCentral, tk.RefusedKept) {
		if ln.UID == third.Enrollment || ln.UID == third.TE {
			t.Logf("%s %s: %s %s", ln.Kind, ln.UID, ln.Outcome, ln.Detail)
		}
	}
	for val := range used {
		if n := s.centralCount(p, attr, val); n > 1 {
			t.Errorf("I2: %s on %d people on central", val, n)
		}
	}
	if oldShape {
		t.Errorf("after the sync the hub hands out %s, a value of the old pattern (%d left in the pool)", v, left)
	}
}

// A20: a program's access level changes to CLOSED, from open and from protected, while
// the hub holds offline work. Must hold as A6: work for the facility's own people
// reaches central; nothing of another facility's person is read or written through
// the hub offline; no break-the-glass offline. b is the facility's person whom central
// transfers to the sibling during the outage, with an event for them waiting.
func TestA20ProgramBecomesClosed(t *testing.T) {
	for _, from := range []string{"OPEN", "PROTECTED"} {
		t.Run("from "+from, func(t *testing.T) { programBecomesClosed(t, from) })
	}
}

func programBecomesClosed(t *testing.T, from string) {
	s := begin(t)
	if !s.Live() {
		t.Skip("access levels on a real DHIS2")
	}
	p := s.M.Primary()
	if from == "PROTECTED" {
		p = s.protected()
	}
	if p.AccessLevel != from {
		t.Skipf("program %s is %s, not %s", p.ID, p.AccessLevel, from)
	}
	stage := p.Stages[len(p.Stages)-1]
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA20a")
	b := s.register(nurse, p, "AdvA20b")
	x := s.M.NewPerson(p, "AdvA20x")
	s.atSibling(x)
	s.CentralComplete(p, x)
	s.CentralImport(x.Payload)
	s.Sync()
	others := []string{
		"/api/tracker/trackedEntities/" + x.TE + "?program=" + p.ID + "&fields=trackedEntity",
		"/api/tracker/enrollments/" + x.Enrollment + "?fields=enrollment",
		"/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnits": {s.M.District.ID}, "ouMode": {"DESCENDANTS"},
			"filter": {p.SearchAttr + ":eq:AdvA20x"}, "fields": {"trackedEntity"}}.Encode(),
	}
	s.sameAsCentral(nurse, from+", online", others...)

	s.Offline()
	evA, evB, evX := tk.UID(), tk.UID(), tk.UID()
	for _, w := range []struct{ what, enr, ev string }{{"a's event", a.Enrollment, evA}, {"b's event", b.Enrollment, evB}} {
		if st, _ := s.save(nurse, w.what, s.event(p, stage, w.enr, w.ev, p.EventValues[stage])); st != 200 {
			t.Fatal("the hub refused valid work offline")
		}
	}
	st, body := s.save(nurse, "an event for the sibling's person", s.event(p, stage, x.Enrollment, evX, p.EventValues[stage]))
	t.Logf("offline, an event for a person the hub does not hold: %d %v", st, errorCodes(body))
	glass := "/api/tracker/ownership/override?trackedEntity=" + x.TE + "&program=" + p.ID + "&reason=emergency"
	if st, body := nurse.Do("POST", glass, nil); st == 200 {
		t.Errorf("offline the hub lets the nurse break the glass: %.200s", body)
	} else {
		t.Logf("offline, break the glass: %d %.200s", st, body)
	}

	s.SetField("/api/programs/"+p.ID, "accessLevel", "CLOSED")
	param := "ou"
	if strings.HasPrefix(s.M.DHIS2Version, "2.43") {
		param = "orgUnit"
	}
	tst, tb, _ := s.Central.Do("PUT", "/api/tracker/ownership/transfer?"+url.Values{"trackedEntity": {b.TE}, "program": {p.ID},
		param: {s.M.Sibling.ID}}.Encode(), nil)
	t.Logf("central transfers b to the sibling in the closed program: %d %.200s", tst, tb)
	s.noRepeats(s.reconnect(3))
	want := map[string]string{evA: tk.OnCentral}
	if tst/100 == 2 {
		want[evB] = tk.RefusedKept
	}
	for _, ln := range s.expect(want, tk.OnCentral, tk.RefusedKept) {
		if ln.UID == evB || ln.UID == evX {
			t.Logf("%s %s: %s %s", ln.Kind, ln.UID, ln.Outcome, ln.Detail)
		}
	}
	cst, cb, _ := s.Central.As(nurse.User, s.M.Password).Do("POST", glass, nil)
	hst, hb := nurse.Do("POST", glass, nil)
	t.Logf("closed, online, break the glass: central %d %.200s; through the hub %d %.200s", cst, cb, hst, hb)
	if hst == 200 && cst != 200 {
		t.Errorf("I3: the hub lets the nurse break the glass where central does not")
	}
	s.Sync()
	if tst/100 == 2 {
		others = append(others, "/api/tracker/trackedEntities/"+b.TE+"?program="+p.ID+"&fields=trackedEntity")
	}
	s.sameAsCentral(nurse, "closed, online", others...)
	s.noLeaks(nurse)
	s.Offline()
	s.sameAsCentral(nurse, "closed, offline", append(others,
		"/api/tracker/trackedEntities/"+a.TE+"?program="+p.ID+"&fields=trackedEntity")...)
	if st, body := nurse.Do("POST", glass, nil); st == 200 {
		t.Errorf("closed, offline, the hub lets the nurse break the glass: %.200s", body)
	}
	if st, _ := s.save(nurse, "an event for the facility's person, closed", s.event(p, stage, a.Enrollment, tk.UID(), p.EventValues[stage])); st != 200 {
		t.Error("offline the hub refuses an event for a person the facility owns in a closed program")
	}
	if tst/100 == 2 {
		if st, _ := s.save(nurse, "an event for b, transferred", s.event(p, stage, b.Enrollment, tk.UID(), p.EventValues[stage])); st == 200 {
			t.Error("offline, after the sync, the hub takes an event for a person transferred away in a closed program")
		}
	}
	s.reconnect(1)
	s.expect(want, tk.OnCentral, tk.RefusedKept)
}

// A17: a data element's and an attribute's value type change while offline data has
// the old type. Central 2.42.7 lets an administrator change a value type with data
// stored, even to one the stored values do not fit (checked 9 Nov: TEXT to NUMBER with
// 24 events holding "Hb low", 200 and no warning). Must hold as A1: refused with
// central's reason and kept; values that fit go through; the hub's search filters
// follow the new type after the next sync.
func TestA17ValueTypeChanges(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("value types on a real DHIS2")
	}
	p := s.M.Primary()
	var stage, de string
	for _, st := range p.Stages {
		for _, el := range s.stageElements(st) {
			var d struct{ ValueType string }
			s.Central.JSON("GET", "/api/dataElements/"+el.ID+"?fields=valueType", nil, &d)
			if _, filled := p.EventValues[st][el.ID]; stage == "" && !el.Compulsory && !filled && el.OptionSet == "" &&
				(d.ValueType == "TEXT" || d.ValueType == "LONG_TEXT") && st != p.RestrictedStage {
				stage, de = st, el.ID
			}
		}
	}
	attr := ""
	var res struct {
		PTEAs []struct {
			Mandatory bool
			Attr      struct {
				ID, ValueType     string
				Unique, Generated bool
				OptionSet         *struct{ ID string }
			} `json:"trackedEntityAttribute"`
		} `json:"programTrackedEntityAttributes"`
	}
	s.Central.JSON("GET", "/api/programs/"+p.ID+"?fields=programTrackedEntityAttributes[mandatory,"+
		"trackedEntityAttribute[id,valueType,unique,generated,optionSet[id]]]", nil, &res)
	for _, x := range res.PTEAs {
		if a := x.Attr; attr == "" && !x.Mandatory && !a.Unique && !a.Generated && a.OptionSet == nil &&
			(a.ValueType == "TEXT" || a.ValueType == "PHONE_NUMBER") && a.ID != p.SearchAttr {
			attr = a.ID
		}
	}
	if stage == "" || attr == "" {
		t.Skipf("no optional text data element (%q) or attribute (%q) to change", de, attr)
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA17a")
	b := s.register(nurse, p, "AdvA17b")

	s.Offline()
	evText, evNum := tk.UID(), tk.UID()
	for _, x := range []struct{ ev, enr, v string }{{evText, a.Enrollment, "seen twice"}, {evNum, b.Enrollment, "42"}} {
		v := maps.Clone(p.EventValues[stage])
		v[de] = x.v
		if st, _ := s.save(nurse, "event, "+de+" = "+x.v, s.event(p, stage, x.enr, x.ev, v)); st != 200 {
			t.Fatal("the hub refused a valid event offline")
		}
	}
	pText, pNum := s.M.NewPerson(p, "AdvA17c"), s.M.NewPerson(p, "AdvA17d")
	pText.SetAttr(attr, "+256 700 123")
	pNum.SetAttr(attr, "700123")
	for _, pr := range []*tk.Person{pText, pNum} {
		nurse.Complete(p, pr)
		if st, _ := s.save(nurse, "registration with "+attr, pr.Payload); st != 200 {
			t.Fatal("the hub refused a valid registration offline")
		}
	}

	s.SetField("/api/dataElements/"+de, "valueType", "NUMBER")
	s.SetField("/api/trackedEntityAttributes/"+attr, "valueType", "NUMBER")
	s.noRepeats(s.reconnect(3))
	lines := s.expect(map[string]string{evText: tk.RefusedKept, evNum: tk.OnCentral, pText.TE: tk.RefusedKept, pNum.TE: tk.OnCentral},
		tk.OnCentral, tk.RefusedKept)
	for _, ln := range lines {
		if ln.Outcome == tk.RefusedKept {
			t.Logf("central's reason for %s %s: %s", ln.Kind, ln.UID, ln.Detail)
		}
	}
	cat := s.App.Hub.Catalog()
	t.Logf("after the sync the hub's catalog: %s %s, %s %s", de, cat.ValueTypes[de], attr, cat.ValueTypes[attr])
	if cat.ValueTypes[de] != "NUMBER" || cat.ValueTypes[attr] != "NUMBER" {
		t.Error("after the sync the hub's catalog does not hold the new value types")
	}
	// A search filter on the attribute, online on central and offline on the hub.
	for _, f := range []string{attr + ":gt:700000", attr + ":gt:abc"} {
		q := "/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID}, "filter": {f},
			"fields": {"trackedEntity"}}.Encode()
		cst, cb, _ := s.Central.As(nurse.User, s.M.Password).Do("GET", q, nil)
		s.Offline()
		hst, hb := nurse.Do("GET", q, nil)
		s.Online()
		t.Logf("filter %s: central %d %.200s; the hub offline %d %.200s", f, cst, cb, hst, hb)
		if (cst == 200) != (hst == 200) {
			t.Errorf("filter %s: central answers %d, the hub offline %d", f, cst, hst)
		} else if cst == 200 && strings.Contains(string(cb), pNum.TE) != strings.Contains(string(hb), pNum.TE) {
			t.Errorf("filter %s: central and the hub offline disagree on %s", f, pNum.TE)
		}
	}
}

// A18: a program attribute is made mandatory while registrations without it, and
// edits of people registered before, wait offline. Must hold as A1: what central
// refuses is kept with its reason; the rest goes through.
func TestA18AttributeMadeMandatory(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("program attributes on a real DHIS2")
	}
	p := s.M.Primary()
	path := "/api/programs/" + p.ID
	var res struct {
		PTEAs []struct {
			Mandatory bool
			Attr      struct {
				ID, ValueType     string
				Unique, Generated bool
				OptionSet         *struct{ ID string }
			} `json:"trackedEntityAttribute"`
		} `json:"programTrackedEntityAttributes"`
	}
	s.Central.JSON("GET", path+"?fields=programTrackedEntityAttributes[mandatory,"+
		"trackedEntityAttribute[id,valueType,unique,generated,optionSet[id]]]", nil, &res)
	attr, value := "", ""
	for _, x := range res.PTEAs {
		if a := x.Attr; attr == "" && !x.Mandatory && !a.Unique && !a.Generated && a.OptionSet == nil && a.ID != p.SearchAttr {
			switch a.ValueType {
			case "PHONE_NUMBER":
				attr, value = a.ID, "+256700000018"
			case "TEXT":
				attr, value = a.ID, "A18"
			}
		}
	}
	if attr == "" {
		t.Skip("no optional text attribute to make mandatory")
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	old := s.register(nurse, p, "AdvA18old")
	stage := p.Stages[0]

	s.Offline()
	without, with := s.M.NewPerson(p, "AdvA18a"), s.M.NewPerson(p, "AdvA18b")
	with.SetAttr(attr, value)
	for _, pr := range []*tk.Person{without, with} {
		nurse.Complete(p, pr)
		if st, _ := s.save(nurse, "registration", pr.Payload); st != 200 {
			t.Fatal("the hub refused a valid registration offline")
		}
	}
	edit := personEdit(p, old, s.M.Facility.ID, map[string]string{p.SearchAttr: "AdvA18old2"})
	if st, _ := s.save(nurse, "an edit of a person registered before, without "+attr, edit); st != 200 {
		t.Fatal("the hub refused a valid edit offline")
	}
	ev := tk.UID()
	if st, _ := s.save(nurse, "an event for the person registered before", s.event(p, stage, old.Enrollment, ev, p.EventValues[stage])); st != 200 {
		t.Fatal("the hub refused a valid event offline")
	}

	s.changeOwner(path, func(o map[string]any) {
		for _, x := range asList(o["programTrackedEntityAttributes"]) {
			m := x.(map[string]any)
			if ta, _ := m["trackedEntityAttribute"].(map[string]any); ta != nil && ta["id"] == attr {
				m["mandatory"] = true
			}
		}
	})
	s.noRepeats(s.reconnect(3))
	lines := s.expect(map[string]string{without.Enrollment: tk.RefusedKept, with.Enrollment: tk.OnCentral}, tk.OnCentral, tk.RefusedKept)
	for _, ln := range lines {
		if ln.UID == without.Enrollment || ln.UID == without.TE || ln.UID == old.TE || ln.UID == ev {
			t.Logf("%s %s: %s %s", ln.Kind, ln.UID, ln.Outcome, ln.Detail)
		}
	}
}

// A19: a stage's validation strategy changes from ON_COMPLETE to ON_UPDATE while
// active events without a compulsory data element wait offline. Must hold as A1: with
// ON_UPDATE central refuses the active event itself (A1 covered ON_COMPLETE, where it
// takes it); refused and kept with central's reason; an event with the value goes
// through.
func TestA19ValidationOnUpdate(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("the fake central has no stage metadata to change")
	}
	p := s.M.Primary()
	var stage, de string
	for _, st := range p.Stages {
		var r struct{ ValidationStrategy string }
		s.Central.JSON("GET", "/api/programStages/"+st+"?fields=validationStrategy", nil, &r)
		for _, el := range s.stageElements(st) {
			if _, filled := p.EventValues[st][el.ID]; stage == "" && r.ValidationStrategy == "ON_COMPLETE" && !el.Compulsory &&
				!filled && el.OptionSet == "" && st != p.RestrictedStage {
				stage, de = st, el.ID
			}
		}
	}
	if stage == "" {
		t.Skip("no ON_COMPLETE stage with an optional data element")
	}
	s.SetCompulsory(stage, de, true)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA19a")
	b := s.register(nurse, p, "AdvA19b")
	s.Sync()

	s.Offline()
	evA, evB := tk.UID(), tk.UID()
	with := maps.Clone(p.EventValues[stage])
	with[de] = "7"
	if st, _ := s.save(nurse, "an active event without "+de, s.event(p, stage, a.Enrollment, evA, p.EventValues[stage])); st != 200 {
		t.Fatal("the hub refused an active event offline")
	}
	if st, _ := s.save(nurse, "an active event with "+de, s.event(p, stage, b.Enrollment, evB, with)); st != 200 {
		t.Fatal("the hub refused an active event offline")
	}
	s.SetField("/api/programStages/"+stage, "validationStrategy", "ON_UPDATE_AND_INSERT")
	s.noRepeats(s.reconnect(3))
	lines := s.expect(map[string]string{evA: tk.RefusedKept, evB: tk.OnCentral}, tk.OnCentral)
	for _, ln := range lines {
		if ln.UID == evA {
			t.Logf("central's reason: %s", ln.Detail)
			if !strings.Contains(ln.Detail, de) {
				t.Errorf("the refusal does not name %s: %s", de, ln.Detail)
			}
		}
	}
}

// personType makes a relationship type between two people of p's type, readable and
// writable by all; removed when the test ends unless already deleted.
func (s *scene) personType(p tk.Program, rels *[]string) string {
	s.t.Helper()
	id := tk.UID()
	constraint := map[string]any{"relationshipEntity": "TRACKED_ENTITY_INSTANCE", "trackedEntityType": map[string]string{"id": p.TET}}
	if st, b, err := s.Central.Do("POST", "/api/relationshipTypes", map[string]any{"id": id,
		"name": "Adversarial A23 " + id, "fromToName": "Knows", "toFromName": "Known by", "bidirectional": false,
		"sharing": map[string]any{"public": "rwrw----"}, "fromConstraint": constraint, "toConstraint": constraint}); err != nil || st/100 != 2 {
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
		if st, b, err := s.Central.Do("DELETE", "/api/relationshipTypes/"+id, nil); err != nil || (st/100 != 2 && st != 404) {
			s.t.Errorf("relationship type %s not removed: %d %v %.300s", id, st, err, b)
		}
	})
	return id
}

// refusal tells whether the hub holds an object as refused, with an open issue, and
// central's reason (the ledger follows people, enrollments and events only).
func (s *scene) refusal(uid string) (bool, string) {
	s.t.Helper()
	issues, err := s.App.Hub.Store.UnresolvedIssues(100000)
	if err != nil {
		s.t.Fatal(err)
	}
	obj := s.App.Hub.Index.Get(uid)
	for _, is := range issues {
		if is.ObjectUID == uid {
			return obj != nil && obj.SyncState == store.SyncRejected, string(is.Error)
		}
	}
	return false, ""
}

// A23: a relationship type changes while relationships of it wait offline: its
// constraints (to a household), bidirectional, its sharing (data write removed), or
// it is deleted. Must hold: refused or accepted as central decides, refusals kept;
// nothing applied twice.
func TestA23RelationshipTypeChanges(t *testing.T) {
	for _, change := range []string{"constraint", "bidirectional", "sharing", "deleted"} {
		t.Run(change, func(t *testing.T) { relationshipTypeChanges(t, change) })
	}
}

func relationshipTypeChanges(t *testing.T, change string) {
	s := begin(t)
	if !s.Live() {
		t.Skip("relationships on a real DHIS2")
	}
	p := s.M.Primary()
	var rels []string
	rt := s.personType(p, &rels)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	a := s.register(nurse, p, "AdvA23a")
	b := s.register(nurse, p, "AdvA23b")
	s.Sync()

	s.Offline()
	rel := tk.UID()
	rels = append(rels, rel)
	if st, body := s.save(nurse, "a relationship a to b", relationship(rel, rt, a.TE, b.TE)); st != 200 {
		t.Fatalf("the hub refused a relationship offline: %d %v", st, errorCodes(body))
	}
	path := "/api/relationshipTypes/" + rt
	switch change {
	case "constraint":
		var hh struct {
			TrackedEntityTypes []struct{ ID string } `json:"trackedEntityTypes"`
		}
		s.Central.JSON("GET", "/api/trackedEntityTypes?fields=id&paging=false", nil, &hh)
		other := ""
		for _, x := range hh.TrackedEntityTypes {
			if x.ID != p.TET && other == "" {
				other = x.ID
			}
		}
		if other == "" {
			t.Skip("no other tracked entity type")
		}
		s.changeOwner(path, func(o map[string]any) {
			o["toConstraint"] = map[string]any{"relationshipEntity": "TRACKED_ENTITY_INSTANCE", "trackedEntityType": map[string]any{"id": other}}
		})
	case "bidirectional":
		s.SetField(path, "bidirectional", true)
	case "sharing":
		st, body, _ := s.Central.Do("POST", "/api/sharing?type=relationshipType&id="+rt,
			map[string]any{"object": map[string]any{"publicAccess": "rw------"}})
		t.Logf("data write removed from the type's sharing: %d %.200s", st, body)
	case "deleted":
		st, body, _ := s.Central.Do("DELETE", path, nil)
		t.Logf("the type deleted on central: %d %.200s", st, body)
		if st/100 != 2 {
			t.Fatalf("central refused to delete the type")
		}
	}
	s.noRepeats(s.reconnect(3))
	s.expect(nil, tk.OnCentral)
	on := s.CentralObject("relationships", rel) != nil
	held, reason := s.refusal(rel)
	t.Logf("relationship after the change (%s): on central %v; refused and kept on the hub %v %s", change, on, held, reason)
	if on == held {
		t.Errorf("I1: relationship on central %v, refused and kept %v: want exactly one", on, held)
	}
	outcome := map[bool]string{true: tk.RefusedKept}[held]
	if outcome == tk.RefusedKept {
		s.shows(nurse, "relationships", rel)
	}
}
