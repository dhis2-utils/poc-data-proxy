package adversarial

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/config"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// webUser is a browser client of the hub for any user.
func (s *scene) webUser(username, password string) *tk.Client {
	c := s.Web(tk.RoleNurse)
	c.User, c.Role, c.Password = username, username, password
	return c
}

// tryLogin logs a browser in through the hub and returns the status and whether the
// hub said SUCCESS.
func tryLogin(c *tk.Client) (int, string) {
	st, b := c.Do("POST", "/api/auth/login", map[string]string{"username": c.User, "password": c.Password})
	return st, string(b)
}

// B3: a user's password is changed on central. Must hold: offline login with the old
// password until the hub learns; documented. Measured: what each password gets through
// the hub, online and offline, before and after the hub learns, and what happens to the
// user's queued saves.
func TestB3PasswordChangedOnCentral(t *testing.T) { passwordChanged(t, false) }

// Online, the hub keeps taking the old password from Android for up to BasicRecheck
// (5 minutes) after the change, though central refuses it, and though a web login
// through the hub with that password has just failed: a cached Basic credential is
// checked again only when it is 5 minutes old (core.Sessions.Basic), and a failed web
// login does not drop it. Requests pass through with the user's token.
func TestB3OldPasswordOnlineThroughCache(t *testing.T) {
	finding(t, "B3: online, Android's old password is taken through the hub for up to 5 minutes after central changed it (cached Basic credential)")
	passwordChanged(t, true)
}

func passwordChanged(t *testing.T, strict bool) {
	s := begin(t)
	if !s.Live() {
		t.Skip("passwords on a real DHIS2")
	}
	u := s.NewUserLike(tk.RoleNurse)
	oldPW, newPW := s.M.Password, "Hub-Changed-2026!"
	android := s.connect(u.Username)
	if st, b := tryLogin(s.webUser(u.Username, oldPW)); st != 200 || !strings.Contains(b, "SUCCESS") {
		t.Fatalf("online web login: %d %.200s", st, b)
	}
	p := s.M.Primary()
	a := s.register(android, p, "AdvB3a")
	s.Sync()

	s.SetUserPassword(u, newPW)
	try := func(when string) map[string]int {
		t.Helper()
		got := map[string]int{}
		for _, pw := range []string{oldPW, newPW} {
			name := map[string]string{oldPW: "old", newPW: "new"}[pw]
			st, b := tryLogin(s.webUser(u.Username, pw))
			if st == 200 && !strings.Contains(b, "SUCCESS") {
				st = 401 // a login that failed with 200 and no SUCCESS
			}
			got["web "+name] = st
			ac := s.AndroidUser(u.Username)
			ac.Password = pw
			got["android "+name], _ = ac.Do("GET", "/api/me?fields=id", nil)
		}
		t.Logf("%s (online=%v): %v", when, !s.Net.Down(), got)
		return got
	}

	s.Offline()
	before := try("offline, before the hub learns")
	stage := p.Stages[0]
	ev := tk.UID()
	if st, _ := s.save(android, "an event offline, old password", s.event(p, stage, a.Enrollment, ev, p.EventValues[stage])); st != 200 {
		t.Error("offline, before the hub learns, the old password's save is refused")
	}
	s.Online()
	online := try("online") // the old password first, then the new one
	s.Offline()
	after := try("offline, after both were tried online")
	s.Online()
	s.noRepeats(s.reconnect(2))
	s.expect(nil, tk.OnCentral)

	if before["web old"] != 200 || before["web new"] == 200 {
		t.Errorf("offline before the hub learns: want the old password only (documented), got %v", before)
	}
	if online["web old"] == 200 || online["web new"] != 200 || online["android new"] != 200 {
		t.Errorf("online: want the new password only, as central, got %v", online)
	}
	if strict && online["android old"] == 200 {
		t.Errorf("online, Android's old password is still taken through the hub")
	}
	if after["web old"] == 200 || after["android old"] == 200 || after["web new"] != 200 || after["android new"] != 200 {
		t.Errorf("offline after the online logins: want the new password only, got %v", after)
	}
}

// B5: a role loses an authority (F_UNCOMPLETE_EVENT, reopening a completed event)
// while a reopening waits offline. Must hold: refused as central; refused offline at
// the next sync.
func TestB5RoleLosesAuthority(t *testing.T) { roleLosesAuthority(t, false) }

// The hub checks no tracker authority offline: after the sync it still takes the
// reopening of a completed event, which central refuses at push. Nothing is lost.
func TestB5HubRefusesAfterSync(t *testing.T) {
	finding(t, "B5: after the sync the hub still takes offline what a lost authority forbids (reopening a completed event); central refuses it at push")
	roleLosesAuthority(t, true)
}

func roleLosesAuthority(t *testing.T, strict bool) {
	s := begin(t)
	if !s.Live() {
		t.Skip("roles on a real DHIS2")
	}
	const auth = "F_UNCOMPLETE_EVENT"
	nurse := s.M.Users[tk.RoleNurse]
	var cur struct {
		UserRoles []struct {
			ID          string
			Authorities []string
		} `json:"userRoles"`
	}
	if err := s.Central.JSON("GET", "/api/users/"+nurse.UID+"?fields=userRoles[id,authorities]", nil, &cur); err != nil {
		t.Fatal(err)
	}
	var auths []string
	for _, r := range cur.UserRoles {
		auths = append(auths, r.Authorities...)
	}
	if !slices.Contains(auths, auth) {
		t.Skipf("the nurse's roles have no %s", auth)
	}
	role := tk.UID()
	if st, b, err := s.Central.Do("POST", "/api/userRoles", map[string]any{"id": role, "name": "Adversarial B5 " + role,
		"authorities": auths}); err != nil || st/100 != 2 {
		t.Fatalf("role: %d %v %.300s", st, err, b)
	}
	t.Cleanup(func() {
		if st, b, err := s.Central.Do("DELETE", "/api/userRoles/"+role, nil); err != nil || st/100 != 2 {
			t.Errorf("NOT UNDONE on central: role %s: %d %v %.200s", role, st, err, b)
		}
	})
	u := s.NewUserLike(tk.RoleNurse)
	s.patchUser(u, "/userRoles", []any{map[string]any{"id": role}})
	c := s.connect(u.Username)
	p := s.M.Primary()
	stage := p.Stages[0]
	a := s.register(c, p, "AdvB5a")
	b := s.register(c, p, "AdvB5b")
	evA, evB := tk.UID(), tk.UID()
	for _, x := range []struct{ ev, enr string }{{evA, a.Enrollment}, {evB, b.Enrollment}} {
		if st, _ := s.save(c, "a completed event", completed(s.event(p, stage, x.enr, x.ev, p.EventValues[stage]))); st != 200 {
			t.Fatal("the hub refused a completed event online")
		}
	}
	s.Sync()

	s.Offline()
	if st, _ := s.save(c, "a's event reopened offline", s.event(p, stage, a.Enrollment, evA, p.EventValues[stage])); st != 200 {
		t.Fatal("the hub refused reopening an event offline while the user may")
	}
	other := tk.UID()
	if st, _ := s.save(c, "an unrelated event", s.event(p, p.Stages[1], b.Enrollment, other, p.EventValues[p.Stages[1]])); st != 200 {
		t.Fatal("the hub refused valid work offline")
	}
	s.patchRole(role, "/authorities", slices.DeleteFunc(slices.Clone(auths), func(x string) bool { return x == auth }))
	s.ExpireTokenCache()
	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{evA: tk.RefusedKept, other: tk.OnCentral}, tk.OnCentral)
	acc := s.App.Hub.Access(s.hubUserID(u.Username))
	t.Logf("the hub's access list holds %s: %v", auth, slices.Contains(acc.Authorities, auth))

	s.Offline()
	st, body := s.save(c, "b's event reopened offline, after the sync", s.event(p, stage, b.Enrollment, evB, p.EventValues[stage]))
	t.Logf("offline after the sync, reopening a completed event without %s: %d %v", auth, st, errorCodes(body))
	if strict && st == 200 {
		t.Errorf("the hub takes offline what central refuses the user (%s)", auth)
	}
	s.reconnect(1)
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
}

func (s *scene) patchRole(role, path string, v any) {
	s.t.Helper()
	if st, b, err := s.Central.Do("PATCH", "/api/userRoles/"+role, []any{map[string]any{"op": "replace", "path": path, "value": v}}); err != nil || st/100 != 2 {
		s.t.Fatalf("PATCH role %s: %d %v %.300s", role, st, err, b)
	}
}

// hubUserID is the hub's id of a user.
func (s *scene) hubUserID(username string) int64 {
	s.t.Helper()
	u, err := s.App.Hub.Store.UserByName(username)
	if err != nil || u == nil {
		s.t.Fatalf("user %s on the hub: %v", username, err)
	}
	return u.ID
}

// B9: a brand-new user arrives while the facility is offline. Must hold: they cannot
// log in (the first login must be online), with a clear message; online they can, and
// offline after that.
func TestB9NewUserWhileOffline(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("users on a real DHIS2; the fake's is in e2e")
	}
	u := s.NewUserLike(tk.RoleNurse)
	web := s.webUser(u.Username, s.M.Password)
	android := s.AndroidUser(u.Username)
	s.Offline()
	st, b := tryLogin(web)
	t.Logf("offline, web login: %d %.300s", st, b)
	if strings.Contains(b, "SUCCESS") {
		t.Error("a user the hub never saw logs in offline")
	}
	for _, path := range []string{"/api/me?fields=id", "/api/system/info"} {
		st, b := android.Do("GET", path, nil)
		t.Logf("offline, Android %s: %d %.300s", path, st, b)
		if st == 200 && path != "/api/system/info" {
			t.Errorf("a user the hub never saw gets %s offline", path)
		}
	}
	p := s.M.Primary()
	pr := s.M.NewPerson(p, "AdvB9")
	if st, b := android.Do("POST", tk.SyncImport, pr.Payload); st == 200 {
		t.Errorf("a user the hub never saw saves offline: %.200s", b)
	}
	s.Online()
	if st, b := tryLogin(web); !strings.Contains(b, "SUCCESS") {
		t.Fatalf("online, the new user's login: %d %.300s", st, b)
	}
	if st, _ := android.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("online, Android: %d", st)
	}
	s.Sync()
	s.Offline()
	if st, b := tryLogin(s.webUser(u.Username, s.M.Password)); !strings.Contains(b, "SUCCESS") {
		t.Errorf("offline after the first online login: %d %.300s", st, b)
	}
	android.Complete(p, pr)
	if st, _ := s.save(android, "a registration offline after the first login", pr.Payload); st != 200 {
		t.Error("the new user cannot save offline after their first online login")
	}
	s.reconnect(1)
	s.expect(nil, tk.OnCentral)
}

// B11: user group membership changes for many users at once: n users lose the group
// that shares the program with them. Must hold: access refreshed for all within the
// refresh interval. Measured: the cycle that refreshes them, its requests to central,
// and what the hub serves them before it.
func TestB11GroupMembershipOfManyUsers(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("user groups on a real DHIS2")
	}
	const n = 10
	p := s.M.Primary()
	var users []tk.User
	var clients []*tk.Client
	for range n {
		u := s.NewUserLike(tk.RoleNurse)
		users = append(users, u)
		clients = append(clients, s.connect(u.Username))
	}
	group := s.writingGroup(users[0], "program", p.ID)
	var people []*tk.Person
	for i, c := range clients {
		people = append(people, s.register(c, p, fmt.Sprintf("AdvB11u%d", i)))
	}
	s.Sync()
	s.Offline()
	stage := p.Stages[0]
	for i, c := range clients {
		if st, _ := s.save(c, "an event offline", s.event(p, stage, people[i].Enrollment, tk.UID(), p.EventValues[stage])); st != 200 {
			t.Fatal("the hub refused valid work offline")
		}
	}
	start := time.Now()
	for _, u := range users {
		if st, b, err := s.Central.Do("DELETE", "/api/userGroups/"+group+"/users/"+u.UID, nil); err != nil || st/100 != 2 {
			t.Fatalf("removing %s from %s: %d %v %.200s", u.Username, group, st, err, b)
		}
	}
	t.Logf("removed %d users from %s on central in %s", n, group, time.Since(start).Round(time.Millisecond))
	s.ExpireTokenCache()

	// Before the hub's access refresh: what it serves them offline.
	q := url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID}, "fields": {"trackedEntity"}, "pageSize": {"5"}}
	before := 0
	for _, c := range clients {
		if st, _ := c.Do("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil); st == 200 {
			before++
		}
	}
	t.Logf("before the refresh, offline: %d of %d users still read the program through the hub (for up to the refresh interval, %s by default, after central's token cache)",
		before, n, config.Default().AccessRefreshInterval)

	s.Online()
	s.Net.Calls()
	cycle := time.Now()
	s.Sync()
	took := time.Since(cycle)
	calls := s.Net.Calls()
	me := 0
	for _, c := range calls {
		if c.URL.Path == "/api/me" {
			me++
		}
	}
	t.Logf("one sync cycle refreshed %d users in %s with %d requests to central (%d /api/me)", n+1, took.Round(time.Millisecond), len(calls), me)
	s.reconnect(2)
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
	s.Offline()
	after := 0
	for _, c := range clients {
		if st, _ := c.Do("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil); st == 200 {
			after++
		}
	}
	t.Logf("after the refresh, offline: %d of %d users read the program through the hub", after, n)
	s.Online()
	for _, c := range clients[:2] {
		s.noLeaks(c)
	}
	if after > 0 {
		t.Errorf("I3: %d users still read the program offline after the access refresh", after)
	}
}

// B12: a user's search scope changes (search org units). Must hold: offline search
// follows at the next sync. The user's search scope goes from the district to the
// other district, which leaves out the facility and the sibling.
func TestB12SearchScopeChanges(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("search scope on a real DHIS2")
	}
	p := s.M.Primary()
	u := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	a := s.register(c, p, "AdvB12a")
	x := s.M.NewPerson(p, "AdvB12x")
	s.atSibling(x)
	s.CentralComplete(p, x)
	s.CentralImport(x.Payload)
	s.Sync()
	searches := []string{
		"/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID},
			"filter": {p.SearchAttr + ":like:AdvB12"}, "fields": {"trackedEntity"}}.Encode(),
		"/api/tracker/trackedEntities?" + url.Values{"trackedEntityType": {p.TET}, "orgUnits": {s.M.Facility.ID},
			"filter": {p.SearchAttr + ":eq:AdvB12a"}, "fields": {"trackedEntity"}}.Encode(),
		"/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnitMode": {"ACCESSIBLE"},
			"filter": {p.SearchAttr + ":like:AdvB12"}, "fields": {"trackedEntity"}}.Encode(),
		"/api/tracker/trackedEntities/" + x.TE + "?program=" + p.ID + "&fields=trackedEntity",
		"/api/tracker/trackedEntities/" + a.TE + "?program=" + p.ID + "&fields=trackedEntity",
	}
	look := func(when string) {
		t.Helper()
		for _, down := range []bool{false, true} {
			if down {
				s.Offline()
			}
			s.sameAsCentral(c, when, searches...)
			for _, q := range searches[:3] {
				if l := s.leaks(c, q, "trackedEntities", "trackedEntity"); len(l) > 0 {
					t.Errorf("I3: %s: the hub (online=%v) finds %v central does not", when, !down, l)
				}
			}
			if down {
				s.Online()
			}
		}
	}
	look("search scope the district")
	var other struct {
		OrganisationUnits []struct{ ID string } `json:"organisationUnits"`
	}
	s.Central.JSON("GET", "/api/organisationUnits?filter=level:eq:2&filter=id:ne:"+s.M.District.ID+"&fields=id", nil, &other)
	if len(other.OrganisationUnits) == 0 {
		t.Skip("no second district")
	}
	s.patchUser(u, "/teiSearchOrganisationUnits", []any{map[string]any{"id": other.OrganisationUnits[0].ID}})
	s.ExpireTokenCache()
	s.Offline()
	s.sameAsCentral(c, "search scope changed, before the hub's sync, offline", searches...)
	s.Online()
	s.reconnect(1)
	look("search scope the other district")
	s.expect(nil, tk.OnCentral)
}
