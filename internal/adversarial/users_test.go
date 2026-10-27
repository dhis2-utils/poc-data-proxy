package adversarial

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/sync"
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
	finding(t, "B1: for up to an hour, a user moved away reads the facility through the hub online (central's token cache; accepted)")
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
// After the hour, central answers the token 401 and the hub holds the saves for an
// online login that cannot come. After 7 days they become refusals (NO_LOGIN), and a
// hub manager resends them as another user, as for a deleted user (B8).
func TestB2UserDisabled(t *testing.T) {
	userDisabled(t, true)
}

// Within the hour, central accepts the disabled user's token: the saves are pushed,
// and the user goes on working through the hub online and offline, while central
// refuses their password ("Account disabled"). Accepted: docs/FINDINGS.md.
func TestB2UserDisabledWithinTheHour(t *testing.T) {
	finding(t, "B2: for up to an hour, a disabled user goes on working through the hub (central's token cache; accepted)")
	userDisabled(t, false)
}

func userDisabled(t *testing.T, expire bool) {
	s := begin(t)
	u := s.NewUserLike(tk.RoleNurse)
	other := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	s.connect(other.Username)
	s.offlineWork(c, "AdvB2")

	s.SetUserDisabled(u, true)
	if expire {
		s.ExpireTokenCache()
		// Saves waiting for a login: the hub sends one each cycle to learn whether
		// central takes the user's token again; they are not refusals sent again.
		s.reconnect(3)
	} else {
		s.noRepeats(s.reconnect(3))
	}
	if expire {
		s.resentAfterAWeek(u, other)
	} else {
		s.expect(nil, tk.OnCentral, tk.RefusedKept)
	}
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 401 {
		t.Errorf("disabled user online: %d %.300s", st, b)
	}
	s.Offline()
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 401 {
		t.Errorf("disabled user offline, after the hub learned: %d %.300s", st, b)
	}
}

// resentAfterAWeek checks what happens to the saves of a user central stopped
// accepting: they wait for a login; 7 days after the hub marked the user (backdated
// here) they are refused and kept (NO_LOGIN); a hub manager's Resend as other sends
// them, and central records other as the author of the people.
func (s *scene) resentAfterAWeek(gone, other tk.User) {
	s.t.Helper()
	if !s.needsLogin(gone.Username) {
		s.t.Fatalf("%s is not marked as needing an online login", gone.Username)
	}
	s.expect(nil, tk.OnCentral, tk.Waiting)
	at := time.Now().Add(-(7*24*time.Hour + time.Minute)).UTC().Format(time.RFC3339Nano)
	if _, err := s.App.Hub.Store.Keys.Exec(`UPDATE key_slots SET needs_login_at=? WHERE username=?`, at, gone.Username); err != nil {
		s.t.Fatal(err)
	}
	s.reconnect(1)
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
	// The refusals as the hub's heartbeat lists them for the Facility Hub app.
	groups, _ := sync.Status(s.App.Hub)["refused"].([]map[string]any)
	// Resend on each NO_LOGIN group, as a hub manager does in the app.
	var retries []any
	for _, g := range groups {
		if g["code"] == "NO_LOGIN" {
			retries = append(retries, map[string]any{"id": "adv-" + tk.UID(), "kind": g["kind"], "program": g["program"],
				"code": "NO_LOGIN", "at": time.Now().UTC().Format(time.RFC3339), "by": "adversarial", "user": other.Username})
		}
	}
	if len(retries) == 0 {
		s.t.Fatalf("no NO_LOGIN refusal: %v", groups)
	}
	body := map[string]any{"retries": retries}
	if err := s.Central.JSON("PUT", "/api/dataStore/facility-hub-admin/retries", body, nil); err != nil {
		if err := s.Central.JSON("POST", "/api/dataStore/facility-hub-admin/retries", body, nil); err != nil {
			s.t.Fatal(err)
		}
	}
	s.reconnect(2)
	// Live, the ledger compares central's createdBy with the author: the resent saves
	// differ in that alone, and name other.
	resent := fmt.Sprintf("createdBy %q", other.Username)
	for _, ln := range s.l.Check() {
		s.t.Logf("ledger: %s", ln)
		switch {
		case ln.Outcome == tk.OnCentral && (!s.Live() || ln.Online):
		case ln.Outcome == tk.ChangedOnCentral && s.Live() && !ln.Online && ln.Detail == resent:
		default:
			s.t.Errorf("%s %s: %s %s", ln.Kind, ln.UID, ln.Outcome, ln.Detail)
		}
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
// refused and kept, visible to a manager; pushed as someone else only when a hub
// manager resends them so.
//
// Central answers the deleted user's token 401; the hub holds the saves for an
// online login that cannot come, and after 7 days refuses them (NO_LOGIN).
func TestB8UserDeleted(t *testing.T) {
	s := begin(t)
	u := s.NewUserLike(tk.RoleNurse)
	other := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	s.connect(other.Username)
	s.offlineWork(c, "AdvB8")

	s.DeleteUser(u)
	// The hub sends one waiting save each cycle to learn whether central takes the
	// user's token again (B2).
	s.reconnect(3)
	s.resentAfterAWeek(u, other)
}
