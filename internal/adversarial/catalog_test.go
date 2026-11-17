package adversarial

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// changeOwner reads a metadata object's own fields, lets edit change them and puts
// them back on central; the first call keeps the object as it was, and puts it back
// when the test ends.
func (s *scene) changeOwner(path string, edit func(map[string]any)) {
	s.t.Helper()
	read := func() map[string]any {
		var o map[string]any
		if err := s.Central.JSON("GET", path+"?fields=:owner", nil, &o); err != nil {
			s.t.Fatal(err)
		}
		return o
	}
	if s.saved == nil {
		s.saved = map[string]bool{}
	}
	if !s.saved[path] {
		s.saved[path] = true
		was := read()
		s.t.Cleanup(func() {
			if st, b, err := s.Central.Do("PUT", path, was); err != nil || st/100 != 2 {
				s.t.Errorf("NOT UNDONE on central: %s: %d %v %.300s", path, st, err, b)
			}
		})
	}
	o := read()
	edit(o)
	if st, b, err := s.Central.Do("PUT", path, o); err != nil || st/100 != 2 {
		s.t.Fatalf("PUT %s: %d %v %.300s", path, st, err, b)
	}
}

// typeAttribute is a tracked entity type's attribute entry for attr.
func typeAttribute(o map[string]any, attr string) map[string]any {
	for _, x := range asList(o["trackedEntityTypeAttributes"]) {
		if m := x.(map[string]any); fmt.Sprint(m["trackedEntityAttribute"].(map[string]any)["id"]) == attr {
			return m
		}
	}
	return nil
}

// attrs is what a read shows of a person's attributes.
func attrsOf(st int, b []byte) map[string]string {
	if st != 200 {
		return nil
	}
	return valuesOf(b)
}

// A13: an attribute becomes an attribute of the tracked entity type, is made
// searchable, then is removed from the type, while staff register and search offline.
// The attribute (date of birth) belongs to the primary program; q is a program of the
// same type without it. Must hold: searches and registrations follow at the next sync;
// reads show what central shows.
func TestA13TypeAttributeChanges(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("tracked entity type metadata on a real DHIS2")
	}
	p := s.M.Primary()
	attr, value, q := s.attributeForType(p)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	s.Sync()
	tetPath := "/api/trackedEntityTypes/" + p.TET

	// compare reads a person through the hub and on central as the nurse, with q and
	// without a program, and searches q by the attribute; it reports I3 and logs gaps.
	compare := func(when string, te string) {
		t.Helper()
		for _, path := range []string{
			"/api/tracker/trackedEntities/" + te + "?program=" + q.ID + "&fields=attributes",
			"/api/tracker/trackedEntities/" + te + "?fields=attributes",
		} {
			hst, hb := nurse.Do("GET", path, nil)
			cst, cb, _ := s.Central.As(nurse.User, s.M.Password).Do("GET", path, nil)
			h, c := attrsOf(hst, hb), attrsOf(cst, cb)
			t.Logf("%s (online=%v): %s: %s on the hub %d %q, on central %d %q", when, !s.Net.Down(), path, attr, hst, h[attr], cst, c[attr])
			if _, ok := h[attr]; ok && c[attr] == "" {
				t.Errorf("I3: %s: the hub shows %s, central does not", when, attr)
			}
		}
		search := "/api/tracker/trackedEntities?" + url.Values{"program": {q.ID}, "orgUnits": {s.M.Facility.ID},
			"filter": {attr + ":eq:" + value}, "fields": {"trackedEntity"}}.Encode()
		hst, hb := nurse.Do("GET", search, nil)
		cst, cb, _ := s.Central.As(nurse.User, s.M.Password).Do("GET", search, nil)
		t.Logf("%s (online=%v): search %s by %s: hub %d %.150s; central %d %.150s", when, !s.Net.Down(), q.ID, attr, hst, hb, cst, cb)
		if hst == 200 && strings.Contains(string(hb), te) && !(cst == 200 && strings.Contains(string(cb), te)) {
			t.Errorf("I3: %s: the hub finds the person by %s, central does not", when, attr)
		}
	}

	// Registered offline under the old rules, then the attribute joins the type.
	s.Offline()
	a := s.M.NewPerson(q, "AdvA13a")
	a.SetAttr(attr, value)
	nurse.Complete(q, a)
	st, b := s.save(nurse, "registering in "+q.ID+" with "+attr+", not yet a type attribute", a.Payload)
	t.Logf("offline, before the change: %d %v", st, errorCodes(b))
	s.changeOwner(tetPath, func(o map[string]any) {
		o["trackedEntityTypeAttributes"] = append(asList(o["trackedEntityTypeAttributes"]),
			map[string]any{"trackedEntityAttribute": map[string]any{"id": attr}, "searchable": false, "displayInList": false})
	})
	s.noRepeats(s.reconnect(3))
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
	if !slices.ContainsFunc(s.App.Hub.Catalog().TETAttributes[p.TET], func(x access.AttrMeta) bool { return x.ID == attr }) {
		t.Errorf("after the sync the hub's catalog does not hold %s as an attribute of %s", attr, p.TET)
	}
	b2 := s.M.NewPerson(q, "AdvA13b")
	b2.SetAttr(attr, value)
	nurse.Complete(q, b2)
	if st, _ := s.save(nurse, "registering online with the new type attribute", b2.Payload); st != 200 {
		t.Errorf("online, a registration with the new type attribute: %d", st)
	}
	s.Sync()
	s.Offline()
	compare("a type attribute, not searchable", b2.TE)
	c := s.M.NewPerson(q, "AdvA13c")
	c.SetAttr(attr, value)
	nurse.Complete(q, c)
	if st, _ := s.save(nurse, "registering offline with the new type attribute", c.Payload); st != 200 {
		t.Error("offline after the sync, the hub refuses a registration with the new type attribute")
	}
	s.Online()

	s.changeOwner(tetPath, func(o map[string]any) { typeAttribute(o, attr)["searchable"] = true })
	s.reconnect(1)
	s.Offline()
	compare("searchable", b2.TE)
	s.Online()

	s.reconnect(1)
	s.expect(nil, tk.OnCentral)

	s.changeOwner(tetPath, func(o map[string]any) {
		o["trackedEntityTypeAttributes"] = slices.DeleteFunc(asList(o["trackedEntityTypeAttributes"]), func(x any) bool {
			return fmt.Sprint(x.(map[string]any)["trackedEntityAttribute"].(map[string]any)["id"]) == attr
		})
	})
	s.reconnect(1)
	if slices.ContainsFunc(s.App.Hub.Catalog().TETAttributes[p.TET], func(x access.AttrMeta) bool { return x.ID == attr }) {
		t.Errorf("after the sync the hub's catalog still holds %s as an attribute of %s", attr, p.TET)
	}
	s.Offline()
	compare("removed from the type", b2.TE)
	d := s.M.NewPerson(q, "AdvA13d")
	d.SetAttr(attr, value)
	nurse.Complete(q, d)
	st, b = s.save(nurse, "registering offline with the attribute removed from the type", d.Payload)
	t.Logf("offline after the removal: %d %v", st, errorCodes(b))
	s.reconnect(2)
	// The attribute belongs to neither q nor the type now: central no longer shows it on
	// any read, so each person differs from what was saved in that value alone.
	hidden := fmt.Sprintf("%s=%q, central \"\"", attr, value)
	for _, ln := range s.l.Check() {
		t.Logf("ledger: %s", ln)
		if ln.Outcome != tk.OnCentral && !(ln.Outcome == tk.ChangedOnCentral && ln.Detail == hidden) {
			t.Errorf("%s %s: %s %s", ln.Kind, ln.UID, ln.Outcome, ln.Detail)
		}
	}
}

// attributeForType picks an attribute of program p that is not an attribute of its
// type, with a value for it, and another program of the same type without it.
func (s *scene) attributeForType(p tk.Program) (attr, value string, q tk.Program) {
	s.t.Helper()
	type pteas struct {
		PTEAs []struct {
			Attr struct {
				ID, ValueType     string
				Unique, Generated bool
				OptionSet         *struct{ ID string }
			} `json:"trackedEntityAttribute"`
		} `json:"programTrackedEntityAttributes"`
	}
	read := func(prog string) pteas {
		var r pteas
		if err := s.Central.JSON("GET", "/api/programs/"+prog+"?fields=programTrackedEntityAttributes[trackedEntityAttribute[id,valueType,unique,generated,optionSet[id]]]", nil, &r); err != nil {
			s.t.Fatal(err)
		}
		return r
	}
	var tet struct {
		TETAs []struct {
			Attr struct{ ID string } `json:"trackedEntityAttribute"`
		} `json:"trackedEntityTypeAttributes"`
	}
	if err := s.Central.JSON("GET", "/api/trackedEntityTypes/"+p.TET+"?fields=trackedEntityTypeAttributes[trackedEntityAttribute[id]]", nil, &tet); err != nil {
		s.t.Fatal(err)
	}
	isType := func(id string) bool {
		return slices.ContainsFunc(tet.TETAs, func(x struct {
			Attr struct{ ID string } `json:"trackedEntityAttribute"`
		}) bool {
			return x.Attr.ID == id
		})
	}
	for _, x := range read(p.ID).PTEAs {
		a := x.Attr
		if isType(a.ID) || a.Unique || a.Generated || a.OptionSet != nil || (a.ValueType != "DATE" && a.ValueType != "TEXT") {
			continue
		}
		for _, other := range s.M.Programs {
			if other.ID == p.ID || other.TET != p.TET || other.Event {
				continue
			}
			if !slices.ContainsFunc(read(other.ID).PTEAs, func(y struct {
				Attr struct {
					ID, ValueType     string
					Unique, Generated bool
					OptionSet         *struct{ ID string }
				} `json:"trackedEntityAttribute"`
			}) bool {
				return y.Attr.ID == a.ID
			}) {
				value = "A13-" + tk.UID()
				if a.ValueType == "DATE" {
					value = fmt.Sprintf("19%02d-0%d-1%d", 50+time.Now().Nanosecond()%50, 1+time.Now().Nanosecond()%9, time.Now().Nanosecond()%9)
				}
				return a.ID, value, other
			}
		}
	}
	s.t.Skip("no attribute of the primary program to add to its type")
	return
}

// A15: central's metadata changes many times while the hub is offline: a stage made
// non-repeatable, repeatable and non-repeatable again; a program set to enrol once,
// then not, then once; an attribute made not unique and back; a program taken from the
// facility. Must hold: after one sync the hub's catalog matches central; no stale
// cache: the hub's offline checks follow the final state.
func TestA15ManyChangesInOneOutage(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("metadata on a real DHIS2")
	}
	p := s.M.Primary()
	stage := p.Stages[0]
	var other tk.Program
	for _, x := range s.M.Programs {
		if x.ID != p.ID && x.AccessLevel == "OPEN" && other.ID == "" {
			other = x
		}
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	stagePath := "/api/programStages/" + stage + "?fields=repeatable"
	nurse.Do("GET", stagePath, nil) // a client's copy of the stage, online
	a := s.register(nurse, p, "AdvA15a")
	s.Sync()

	s.Offline()
	start := time.Now()
	for _, v := range []bool{false, true, false} {
		s.SetRepeatable(stage, v)
	}
	for _, v := range []bool{true, false, true} {
		s.SetField("/api/programs/"+p.ID, "onlyEnrollOnce", v)
	}
	if p.UniqueAttr != "" {
		s.SetUnique(p.UniqueAttr, false)
		s.SetUnique(p.UniqueAttr, true)
	}
	if other.ID != "" {
		s.RemoveProgramFromOrgUnit(other.ID, s.M.Facility.ID)
	}
	t.Logf("10 changes on central in %s while the hub was offline", time.Since(start).Round(time.Millisecond))

	s.reconnect(1)
	want := s.centralCatalog()
	got := s.App.Hub.Catalog()
	for id, pm := range want.Programs {
		g, w := mustJSON(got.Programs[id]), mustJSON(pm)
		if g != w {
			t.Errorf("after one sync, program %s on the hub %s, central %s", id, g, w)
		}
	}
	for id := range got.Programs {
		if want.Programs[id] == nil {
			t.Errorf("after one sync, the hub's catalog still holds program %s", id)
		}
	}
	t.Logf("after one sync: %d programs on the hub, %d on central; %s non-repeatable: %v; onlyEnrollOnce: %v", len(got.Programs),
		len(want.Programs), stage, slices.Contains(got.Programs[p.ID].NonRepeatable, stage), got.Programs[p.ID].OnlyEnrollOnce)

	s.Offline()
	ev1, ev2 := tk.UID(), tk.UID()
	st1, _ := s.save(nurse, "an event in the stage", s.event(p, stage, a.Enrollment, ev1, p.EventValues[stage]))
	st2, b2 := s.save(nurse, "a second event in the stage", s.event(p, stage, a.Enrollment, ev2, p.EventValues[stage]))
	t.Logf("offline after the sync: first event %d, second %d %v", st1, st2, errorCodes(b2))
	if st2 == 200 {
		t.Error("the hub takes a second event offline in a stage that ended non-repeatable")
	}
	st, b := nurse.Do("GET", stagePath, nil)
	t.Logf("offline, the client's cached copy of the stage: %d %.100s (central now: repeatable=false)", st, b)
	s.reconnect(1)
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
}

// The hub answers a client's metadata reads offline from that user's last online read
// of the same URL: after a sync that learned the change, offline the nurse still reads
// the stage as repeatable, while the hub itself refuses a second event in it (E1039).
// A client that reloads its metadata offline (Capture web) offers what the hub refuses.
func TestA15CachedMetadataFollowsCentral(t *testing.T) {
	finding(t, "A15: offline the hub replays a user's metadata read from before a change it has since synced (a stage still repeatable)")
	s := begin(t)
	if !s.Live() {
		t.Skip("metadata on a real DHIS2")
	}
	stage := s.M.Primary().Stages[0]
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	path := "/api/programStages/" + stage + "?fields=repeatable"
	st, b := nurse.Do("GET", path, nil)
	t.Logf("online: %d %s", st, b)
	s.SetRepeatable(stage, false)
	s.Sync()
	s.Offline()
	st, b = nurse.Do("GET", path, nil)
	t.Logf("offline after the sync: %d %s", st, b)
	if st == 200 && strings.Contains(string(b), `"repeatable":true`) {
		t.Error("offline the hub gives the stage as central had it before the change it synced")
	}
}

// centralCatalog is the catalog as the hub would build it from central now: with its
// own token and every user's, merged.
func (s *scene) centralCatalog() *access.Catalog {
	s.t.Helper()
	h := s.App.Hub
	ctx := context.Background()
	c, err := h.FetchCatalog(ctx, h.HubAuth(), h.Facility())
	if err != nil {
		s.t.Fatal(err)
	}
	users, _ := h.Store.Users()
	for _, u := range users {
		if auth, err := h.UpstreamAuth(u.ID); err == nil {
			if uc, err := h.FetchCatalog(ctx, auth, h.Facility()); err == nil {
				c.Merge(uc)
			}
		}
	}
	return c
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A16: a large metadata import on central while the hub runs its first pull: n new
// data elements, then imported again and again (as updates) until the pull ends, so
// central is importing for all of it. Must hold: the pull completes or retries cleanly;
// afterwards the hub holds for the nurse what central lists.
func TestA16LargeImportDuringPull(t *testing.T) {
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("a real DHIS2's import")
	}
	s := &scene{Env: e, t: t, l: e.Ledger()}
	const n = 2000
	tag := strings.ToLower(tk.UID()[:5])
	var des []any
	for i := range n {
		des = append(des, map[string]any{"id": tk.UID(), "name": fmt.Sprintf("Adversarial A16 %s %d", tag, i),
			"shortName": fmt.Sprintf("A16 %s %d", tag, i), "valueType": "NUMBER", "domainType": "AGGREGATE", "aggregationType": "SUM"})
	}
	payload := map[string]any{"dataElements": des}
	t.Cleanup(func() {
		// In batches: one delete of all 2000 outlasts the client's timeout on a large
		// central (Sierra Leone's).
		start := time.Now()
		for i := 0; i < n; i += 200 {
			batch := map[string]any{"dataElements": des[i:min(i+200, n)]}
			if st, b, err := s.Central.Do("POST", "/api/metadata?importStrategy=DELETE", batch); err != nil || st/100 != 2 {
				t.Errorf("NOT UNDONE on central: imported data elements %d to %d: %d %v %.300s", i, i+200, st, err, b)
			}
		}
		var left struct {
			DataElements []struct{ ID string } `json:"dataElements"`
		}
		q := url.Values{"filter": {"name:like:Adversarial A16 " + tag + " "}, "fields": {"id"}, "paging": {"false"}}
		if err := s.Central.JSON("GET", "/api/dataElements?"+q.Encode(), nil, &left); err != nil || len(left.DataElements) > 0 {
			t.Errorf("NOT UNDONE on central: %d imported data elements left (%v)", len(left.DataElements), err)
		}
		t.Logf("the %d data elements deleted in %s", n, time.Since(start).Round(time.Second))
	})
	pullDone := make(chan struct{})
	type imports struct {
		n        int
		failed   []string
		durs     []time.Duration
		start    time.Time
		end      time.Time
		firstErr error
	}
	res := make(chan imports, 1)
	go func() {
		r := imports{start: time.Now()}
		for i := 0; ; i++ {
			at := time.Now()
			st, b, err := s.Central.Do("POST", "/api/metadata", payload)
			r.n++
			r.durs = append(r.durs, time.Since(at))
			if err != nil || st/100 != 2 {
				r.failed = append(r.failed, fmt.Sprintf("%d %v %.100s", st, err, b))
			}
			select {
			case <-pullDone:
				r.end = time.Now()
				res <- r
				return
			default:
			}
		}
	}()
	time.Sleep(time.Second) // the first import under way
	pullStart := time.Now()
	e.SetupOnly(tk.RoleAdmin)
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	var cycleErr error
	cycles := 0
	for cycles < 5 {
		cycles++
		if cycleErr = e.App.Worker.Cycle(context.Background()); cycleErr == nil {
			break
		}
		t.Logf("pull cycle %d: %v", cycles, cycleErr)
	}
	pullEnd := time.Now()
	close(pullDone)
	r := <-res
	t.Logf("set-up and first pull under import: %d cycle(s), %s; central imported %d data elements %d times from %s to %s (each %s), %d failed %v",
		cycles, pullEnd.Sub(pullStart).Round(time.Millisecond), n, r.n, r.start.Format("15:04:05"), r.end.Format("15:04:05"),
		durStats(r.durs), len(r.failed), r.failed)
	if cycleErr != nil {
		t.Errorf("the pull did not complete in %d cycles: %v", cycles, cycleErr)
	}
	// What the nurse sees offline, both ways against central.
	p := s.M.Primary()
	q := "/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID}, "fields": {"trackedEntity"},
		"paging": {"false"}}.Encode()
	e.Offline()
	if x := s.leaks(nurse, q, "trackedEntities", "trackedEntity"); len(x) > 0 {
		t.Errorf("I3: after the pull the hub shows %d people central does not", len(x))
	}
	_, hb := nurse.Do("GET", q, nil)
	_, cb, _ := s.Central.As(nurse.User, s.M.Password).Do("GET", q, nil)
	var h, c struct {
		TEs []struct{ TrackedEntity string } `json:"trackedEntities"`
	}
	json.Unmarshal(hb, &h)
	json.Unmarshal(cb, &c)
	t.Logf("after the pull, offline: the hub lists %d people, central %d", len(h.TEs), len(c.TEs))
	if len(h.TEs) != len(c.TEs) {
		t.Errorf("after the pull the hub holds %d people of %d", len(h.TEs), len(c.TEs))
	}
}
