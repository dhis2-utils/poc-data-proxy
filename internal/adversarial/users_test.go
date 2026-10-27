package adversarial

import (
	"net/url"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// offlineWork has a user register a person online, then, offline, add an event for
// them and register another person with an event. It returns the online person.
func (s *scene) offlineWork(c *tk.Client, name string) *tk.Person {
	s.t.Helper()
	p := s.M.Primary()
	a := s.register(c, p, name+"a")
	s.Offline()
	stage := p.Stages[0]
	b := s.M.NewPerson(p, name+"b", stage)
	c.Complete(p, b)
	if st, _ := s.save(c, "an event for "+name+"a", s.event(p, stage, a.Enrollment, tk.UID(), p.EventValues[stage])); st != 200 {
		s.t.Fatal("the hub refused valid work offline")
	}
	if st, _ := s.save(c, "registering "+name+"b", b.Payload); st != 200 {
		s.t.Fatal("the hub refused valid work offline")
	}
	return a
}

// noLeaks checks, online and offline, that the user sees through the hub no more
// people at the facility than on central (I3).
func (s *scene) noLeaks(c *tk.Client) {
	s.t.Helper()
	p := s.M.Primary()
	q := url.Values{"program": {p.ID}, "orgUnits": {s.M.Facility.ID}, "fields": {"trackedEntity"}, "pageSize": {"200"}}
	for _, offline := range []bool{false, true} {
		if offline {
			s.Offline()
			defer s.Online()
		}
		if l := s.leaks(c, "/api/tracker/trackedEntities?"+q.Encode(), "trackedEntities", "trackedEntity"); len(l) > 0 {
			s.t.Errorf("I3: the hub (offline=%v) shows %s %d people central does not: %v", offline, c.User, len(l), l)
		}
	}
}

// Central (2.42) keeps a personal access token's authentication, with the user's
// access as it was, for an hour after its first use (apiTokensCache), and drops it
// only when the token is deleted. The hub works with users' tokens, so the B1 and B2
// scenarios run twice: within that hour, and after it (central's cache cleared).

// B1: a user loses the facility (org unit removed) while their offline work waits.
// Must hold: their saves as central decides on push, refusals kept; their offline
// reads stop at the next sync.
func TestB1UserLosesFacility(t *testing.T) { userLosesFacility(t, true) }

// Within the hour, the offline saves are accepted with the token, though central
// refuses the same write to the user's password (E1000). Offline the hub refuses the
// user (403); online it passes their reads through with the token, and central
// answers with the old scope: a leak through the hub.
func TestB1UserLosesFacilityWithinTheHour(t *testing.T) {
	finding(t, "B1: for up to an hour, a user moved away reads the facility through the hub online (central's token cache)")
	userLosesFacility(t, false)
}

func userLosesFacility(t *testing.T, expire bool) {
	s := begin(t)
	u := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	s.offlineWork(c, "AdvB1")

	s.SetUserOrgUnits(u, s.M.Sibling.ID)
	if expire {
		s.ExpireTokenCache()
	}
	s.noRepeats(s.reconnect(3))
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
	s.noLeaks(c)
}

// B2: a user is disabled on central while their offline work waits. Must hold: they
// can no longer log in once the hub knows; their saves are pushed with their token
// or refused and kept, never lost silently.
//
// After the hour, central answers the token 401; the hub holds the saves for an
// online login that cannot come, as for a deleted user (B8).
func TestB2UserDisabled(t *testing.T) {
	finding(t, "B2: a disabled user's saves wait for ever for a login, pushed again every cycle, not listed as refused")
	userDisabled(t, true)
}

// Within the hour, central accepts the disabled user's token: the saves are pushed,
// and the user goes on working through the hub online and offline, while central
// refuses their password ("Account disabled").
func TestB2UserDisabledWithinTheHour(t *testing.T) {
	finding(t, "B2: for up to an hour, a disabled user goes on working through the hub (central's token cache)")
	userDisabled(t, false)
}

func userDisabled(t *testing.T, expire bool) {
	s := begin(t)
	u := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	s.offlineWork(c, "AdvB2")

	s.SetUserDisabled(u, true)
	if expire {
		s.ExpireTokenCache()
	}
	s.noRepeats(s.reconnect(3))
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 401 {
		t.Errorf("disabled user online: %d %.300s", st, b)
	}
	s.Offline()
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 401 {
		t.Errorf("disabled user offline, after the hub learned: %d %.300s", st, b)
	}
}

// needsLogin reports whether the hub holds the user as needing an online login.
func (s *scene) needsLogin(username string) bool {
	s.t.Helper()
	u, err := s.App.Hub.Store.UserByName(username)
	if err != nil || u == nil {
		s.t.Fatalf("user %s on the hub: %v", username, err)
	}
	return u.NeedsLogin
}

// B4: a user's personal access tokens are revoked on central while their saves wait.
// Must hold: the saves wait, the hub says the user needs an online login, and they
// are pushed once the user logs in online.
func TestB4TokensRevoked(t *testing.T) {
	s := begin(t)
	u := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	s.offlineWork(c, "AdvB4")

	n := s.RevokeTokens(u)
	t.Logf("revoked %d tokens of %s", n, u.Username)
	if n == 0 {
		t.Fatal("the user had no token to revoke")
	}
	s.reconnect(2)
	s.expect(nil, tk.OnCentral, tk.Waiting)
	if !s.needsLogin(u.Username) {
		t.Error("the hub does not say the user needs an online login")
	}

	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("online login after the revocation: %d %.300s", st, b)
	}
	s.noRepeats(s.reconnect(2))
	s.expect(nil, tk.OnCentral)
	if s.needsLogin(u.Username) {
		t.Error("the hub still says the user needs an online login")
	}
}

// B6: a user group's sharing of the program is removed while its members' saves
// wait. Must hold: offline reads and writes stop at the next sync; queued writes
// refused and kept.
func TestB6ProgramSharingRemoved(t *testing.T) {
	s := begin(t)
	p := s.M.Primary()
	u := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	a := s.offlineWork(c, "AdvB6")

	if s.Live() {
		s.RemoveSharing("program", p.ID, s.writingGroup(u, "program", p.ID))
	} else {
		s.RemoveSharing("program", p.ID, u.UID)
	}
	s.noRepeats(s.reconnect(3))
	for _, ln := range s.expect(nil, tk.OnCentral, tk.RefusedKept) {
		if s.Live() && !ln.Online && ln.Kind != "trackedEntities" && ln.Outcome != tk.RefusedKept {
			t.Errorf("%s %s written offline in the program the user lost: %s", ln.Kind, ln.UID, ln.Outcome)
		}
	}
	s.noLeaks(c)
	s.Offline()
	stage := p.Stages[0]
	if st, _ := s.save(c, "an event after the sync", s.event(p, stage, a.Enrollment, tk.UID(), p.EventValues[stage])); st == 200 {
		t.Error("the hub takes an event offline in a program the user lost")
	}
}

// writingGroup is the user's group that may write data of a metadata object.
func (s *scene) writingGroup(u tk.User, kind, id string) string {
	s.t.Helper()
	var user struct {
		UserGroups []struct{ ID string } `json:"userGroups"`
	}
	var sh struct {
		Object struct {
			UserGroupAccesses []struct{ ID, Access string } `json:"userGroupAccesses"`
		} `json:"object"`
	}
	if err := s.Central.JSON("GET", "/api/users/"+u.UID+"?fields=userGroups[id]", nil, &user); err != nil {
		s.t.Fatal(err)
	}
	if err := s.Central.JSON("GET", "/api/sharing?type="+kind+"&id="+id, nil, &sh); err != nil {
		s.t.Fatal(err)
	}
	for _, g := range sh.Object.UserGroupAccesses {
		for _, ug := range user.UserGroups {
			if g.ID == ug.ID && len(g.Access) > 3 && g.Access[3] == 'w' {
				return g.ID
			}
		}
	}
	s.t.Fatalf("no group of %s writes data of %s %s", u.Username, kind, id)
	return ""
}

// B8: a user is deleted on central while their saves wait. Must hold: the saves are
// refused and kept, visible to a manager; never pushed as someone else.
//
// Central answers the deleted user's token 401; the hub holds the saves for an
// online login that cannot come, sends one of them again every cycle, and the
// Facility Hub app says only "2 saves wait for their user to log in on the hub
// again". Nothing is lost or pushed as someone else.
func TestB8UserDeleted(t *testing.T) {
	finding(t, "B8: a deleted user's saves wait for ever for a login, pushed again every cycle, not listed as refused")
	s := begin(t)
	u := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	s.offlineWork(c, "AdvB8")

	s.DeleteUser(u)
	s.noRepeats(s.reconnect(3))
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
}
