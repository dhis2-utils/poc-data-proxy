package adversarial

import (
	"maps"
	"net/url"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/sync"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// A11: the facility is closed on central (a closed date, yesterday) while work made
// offline today waits. Must hold: everything after the date refused, as central
// decides, and kept; the hub says so; it keeps working for reads.
func TestA11FacilityClosed(t *testing.T) {
	ag := beginAggregate(t)
	s := ag.scene
	p := s.M.Primary()
	stage := p.Stages[0]
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	web := s.Web(tk.RoleNurse)
	web.Login()
	a := s.register(nurse, p, "AdvA11a")
	ag.DeleteCentralDataValue(ag.ag.DataSet, ag.pe, ag.ag.Number, ag.ag.COC)
	s.Sync()

	s.Offline()
	ev := tk.UID()
	if st, _ := s.save(nurse, "an event dated today", s.event(p, stage, a.Enrollment, ev, p.EventValues[stage])); st != 200 {
		t.Fatal("the hub refused valid work offline")
	}
	b := s.M.NewPerson(p, "AdvA11b")
	nurse.Complete(p, b)
	if st, _ := s.save(nurse, "a registration today", b.Payload); st != 200 {
		t.Fatal("the hub refused valid work offline")
	}
	ag.enter(web, ag.pe, ag.ag.Number, "11")
	// Next month, which starts after the closed date (the data set opens one future
	// period).
	next := nextMonth(ag.pe)
	ag.DeleteCentralDataValue(ag.ag.DataSet, next, ag.ag.Number, ag.ag.COC)
	stNext, bNext := web.PostDataValue(ag.values, map[string]string{"ds": ag.ag.DataSet, "pe": next, "ou": s.M.Facility.ID,
		"de": ag.ag.Number, "co": ag.ag.COC, "value": "13"})
	t.Logf("offline, a value for %s: %d %.200s", next, stNext, bNext)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	s.SetField("/api/organisationUnits/"+s.M.Facility.ID, "closedDate", yesterday)
	s.noRepeats(s.reconnect(3))
	for _, ln := range s.expect(nil, tk.OnCentral, tk.RefusedKept) {
		if !ln.Online && ln.Outcome == tk.OnCentral {
			t.Logf("central accepted %s %s dated after the facility's closed date %s", ln.Kind, ln.UID, yesterday)
		}
	}
	v, by := ag.onCentral(ag.pe, ag.ag.Number)
	vNext, _ := ag.onCentral(next, ag.ag.Number)
	t.Logf("aggregate: this month's value on central %q by %q, next month's %q; refusals kept %v", v, by, vNext, ag.issues())
	if v == "" || (stNext/100 == 2 && vNext == "" && len(ag.issues()) != 1) {
		t.Errorf("I1: a value entered offline is neither on central nor kept as refused")
	}
	s.save(nurse, "online, an event through the hub at the closed facility", s.event(p, stage, a.Enrollment, tk.UID(), p.EventValues[stage]))
	direct := tk.UID()
	cst, cb, _ := s.Central.As(nurse.User, s.M.Password).Do("POST", tk.SyncImport,
		s.event(p, stage, a.Enrollment, direct, p.EventValues[stage]))
	t.Logf("the same straight to central as the nurse: %d %v", cst, errorCodes(cb))
	if cst == 200 {
		s.CentralDelete("events", direct) // not the hub's: out of the ledger's duplicate search
	}
	t.Logf("the hub's status after the sync: lastError %v, refused %v", sync.Status(s.App.Hub)["lastError"], sync.Status(s.App.Hub)["refused"])

	s.Offline()
	if got := s.sameAsCentral(nurse, "closed, offline", "/api/tracker/trackedEntities/"+a.TE+"?program="+p.ID+"&fields=trackedEntity",
		"/api/tracker/trackedEntities?"+url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID}, "fields": {"trackedEntity"},
			"pageSize": {"5"}}.Encode()); got[0] != 200 || got[1] != 200 {
		t.Errorf("reads at the closed facility offline: %v, want 200", got)
	}
	st, body := s.save(nurse, "an event offline, after the sync", s.event(p, stage, a.Enrollment, tk.UID(), p.EventValues[stage]))
	t.Logf("offline after the sync, a new event at the closed facility: %d %v", st, errorCodes(body))
	st, body = web.PostDataValue(ag.values, map[string]string{"ds": ag.ag.DataSet, "pe": ag.pe, "ou": s.M.Facility.ID,
		"de": ag.ag.Integer, "co": ag.ag.COC, "value": "12"})
	t.Logf("offline after the sync, an aggregate value at the closed facility: %d %.200s", st, body)
	s.reconnect(1)
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
}

// nextMonth is the monthly period after pe (yyyyMM).
func nextMonth(pe string) string {
	t, _ := time.Parse("200601", pe)
	return t.AddDate(0, 1, 0).Format("200601")
}

// A12: the facility moves to another district (its parent changes) on central. Must
// hold: users' scope recomputed; nothing leaks. The district officer of the old
// district loses the facility on central; one of the new district gains it.
//
// The hub decides with the facility's path who is facility staff
// (access.List.InFacility). It kept the path from set-up, so after the move it showed
// the old district's officer every person at the facility offline, where central
// answers 403 (FINDINGS.md). The catalog refresh now reads the path again.
func TestA12FacilityMovesDistrict(t *testing.T) {
	s, north, south, other := movedFacility(t)
	p := s.M.Primary()
	listing := "/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID},
		"fields": {"trackedEntity"}, "paging": {"false"}}.Encode()
	for _, c := range []*tk.Client{north, south} {
		st, b := c.Do("GET", "/api/me?fields=id,organisationUnits", nil)
		t.Logf("online login through the hub as %s after the move: %d %.200s", c.User, st, b)
		if x := s.leaks(c, listing, "trackedEntities", "trackedEntity"); len(x) > 0 {
			t.Errorf("I3: online the hub shows %s %d people central does not", c.User, len(x))
		}
	}
	s.Offline()
	for _, c := range []*tk.Client{north, south} {
		if x := s.leaks(c, listing, "trackedEntities", "trackedEntity"); len(x) > 0 {
			t.Errorf("I3: offline the hub shows %s %d people at the facility central no longer shows them", c.User, len(x))
		}
	}
	s.Online()
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
	t.Logf("the hub's facility path: %s; central's: %s", s.App.Hub.FacilityPath(), other)
}

// movedFacility sets a hub up, with the old district's officer (north) and a user
// like them in the new district (south) known to it, moves the facility to the other
// district on central, and syncs. The move is undone when the test ends.
func movedFacility(t *testing.T) (s *scene, north, south *tk.Client, path string) {
	s = begin(t)
	if !s.Live() {
		t.Skip("the org unit tree on a real DHIS2")
	}
	var ous struct {
		OrganisationUnits []struct{ ID, Path string } `json:"organisationUnits"`
	}
	if err := s.Central.JSON("GET", "/api/organisationUnits?"+url.Values{"filter": {"level:eq:2"},
		"fields": {"id,path"}, "paging": {"false"}}.Encode(), nil, &ous); err != nil {
		t.Fatal(err)
	}
	to := ""
	for _, o := range ous.OrganisationUnits {
		if o.ID != s.M.District.ID && to == "" {
			to = o.ID
		}
	}
	if to == "" {
		t.Skip("no second district")
	}
	north = s.connect(s.M.Users[tk.RoleDistrict].Username)
	su := s.NewUserLike(tk.RoleDistrict)
	s.SetUserOrgUnits(su, to)
	s.patchUser(su, "/teiSearchOrganisationUnits", []any{map[string]any{"id": to}})
	south = s.AndroidUser(su.Username)
	st, b := south.Do("GET", "/api/me?fields=id", nil)
	t.Logf("before the move, the new district's officer logs in through the hub: %d %.200s", st, b)
	s.Sync()
	t.Cleanup(func() { // after the move is undone (cleanups run last first)
		var g struct{ Path string }
		s.Central.JSON("GET", "/api/organisationUnits/"+s.M.Facility.ID+"?fields=path", nil, &g)
		if g.Path != s.M.Facility.Path {
			t.Errorf("NOT UNDONE on central: the facility's path is %s, was %s", g.Path, s.M.Facility.Path)
		}
	})
	s.SetField("/api/organisationUnits/"+s.M.Facility.ID, "parent", map[string]any{"id": to})
	var f struct{ Path string }
	s.Central.JSON("GET", "/api/organisationUnits/"+s.M.Facility.ID+"?fields=path", nil, &f)
	t.Logf("the facility moved to %s on central: path %s", to, f.Path)
	s.ExpireTokenCache()
	s.reconnect(2)
	return s, north, south, f.Path
}

// patch sets one field of a user on central (the user is deleted at the end).
func (s *scene) patchUser(u tk.User, path string, v any) {
	s.t.Helper()
	if st, b, err := s.Central.Do("PATCH", "/api/users/"+u.UID, []any{map[string]any{"op": "replace", "path": path, "value": v}}); err != nil || st/100 != 2 {
		s.t.Fatalf("PATCH user %s %s: %d %v %.300s", u.Username, path, st, err, b)
	}
}

// secondHub sets up another hub at org unit ou, as a hub manager there would: a new
// set-up user like the admin, assigned to ou.
func (s *scene) secondHub(ou tk.OrgUnit) *scene {
	s.t.Helper()
	admin := s.NewUserLike(tk.RoleAdmin)
	s.SetUserOrgUnits(admin, ou.ID)
	// What the hub wrote to central as its set-up user: central deletes the user only
	// without them (a foreign key). Removed after the hub stops, before the user.
	var e *tk.Env
	s.t.Cleanup(func() {
		for _, ns := range []string{"facility-hub-registry", "facility-hub-status"} {
			if st, b, err := s.Central.Do("DELETE", "/api/dataStore/"+ns+"/"+e.App.Hub.HubID(), nil); err != nil || (st/100 != 2 && st != 404) {
				s.t.Errorf("NOT UNDONE on central: %s/%s: %d %v %.200s", ns, e.App.Hub.HubID(), st, err, b)
			}
		}
	})
	e = tk.Start(s.t)
	m := *e.M
	m.Facility = ou
	m.Users = maps.Clone(m.Users)
	m.Users[tk.RoleAdmin] = admin
	e.M = &m
	e.Setup()
	return &scene{Env: e, t: s.t, l: e.Ledger()}
}

// B7: a user moves from the facility to the sibling, where another hub runs. Must
// hold: after their login, each hub shows them only what central shows them: the new
// facility's people on the sibling's hub, and nothing of the old facility, online or
// offline, on the first.
func TestB7UserMovesToAnotherHub(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("two hubs on a real DHIS2")
	}
	p := s.M.Primary()
	u := s.NewUserLike(tk.RoleNurse)
	c1 := s.connect(u.Username)
	a := s.register(c1, p, "AdvB7a")
	s.Sync()
	h2 := s.secondHub(s.M.Sibling)

	s.SetUserOrgUnits(u, s.M.Sibling.ID)
	s.patchUser(u, "/dataViewOrganisationUnits", []any{map[string]any{"id": s.M.Sibling.ID}})
	s.ExpireTokenCache()
	s.reconnect(2)
	st, b := c1.Do("GET", "/api/me?fields=id,organisationUnits", nil)
	t.Logf("the first hub, online, after the move: /api/me %d %.200s", st, b)
	s.noLeaks(c1)
	s.Offline()
	if st, _ := c1.Do("GET", "/api/tracker/trackedEntities/"+a.TE+"?program="+p.ID+"&fields=trackedEntity", nil); st == 200 {
		t.Errorf("I3: offline the first hub still opens the old facility's person for the moved user")
	}
	if st, _ := s.save(c1, "an event at the old facility, offline", s.event(p, p.Stages[0], a.Enrollment, tk.UID(), p.EventValues[p.Stages[0]])); st == 200 {
		t.Errorf("the first hub takes a save offline from a user who left the facility")
	}
	s.Online()

	c2 := h2.connect(u.Username)
	h2.Sync()
	pr := h2.register(c2, p, "AdvB7b")
	h2.Sync()
	for _, h := range []struct {
		*scene
		c  *tk.Client
		ou string
	}{{h2, c2, s.M.Sibling.ID}, {h2, c2, s.M.Facility.ID}} {
		q := url.Values{"program": {p.ID}, "orgUnits": {h.ou}, "fields": {"trackedEntity"}, "paging": {"false"}}
		for _, down := range []bool{false, true} {
			if down {
				h.Offline()
			}
			if x := h.leaks(h.c, "/api/tracker/trackedEntities?"+q.Encode(), "trackedEntities", "trackedEntity"); len(x) > 0 {
				t.Errorf("I3: the sibling's hub (online=%v) shows %s %d people at %s central does not", !down, h.c.User, len(x), h.ou)
			}
			if down {
				h.Online()
			}
		}
	}
	h2.Offline()
	if st, _ := c2.Do("GET", "/api/tracker/trackedEntities/"+pr.TE+"?program="+p.ID+"&fields=trackedEntity", nil); st != 200 {
		t.Errorf("offline the sibling's hub does not open the person the moved user registered there: %d", st)
	}
	h2.Online()
	s.expect(nil, tk.OnCentral)
	h2.expect(nil, tk.OnCentral)
}
