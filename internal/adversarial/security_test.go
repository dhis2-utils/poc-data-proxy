package adversarial

// Security abuse, section L of docs/ADVERSARIAL-TESTING.md, against a real DHIS2.
// Each scenario first shows what the hub does; an invariant that fails is a finding,
// gated with finding() so the suite stays green, and written up in the report.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/devkey"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
	"github.com/dhis2/facility-hub/internal/ui"
)

// dropHubEntries deletes a hub's registry and heartbeat entries when the test ends, so
// the users it named can be deleted: central keeps an entry's last writer and the
// hub's token owner, and refuses to delete a user still named there (as wave3b's
// dropEntries does for G8). It drops the entries both as the superuser and, for the
// registry entry an ordinary user may own on an unprotected instance, is harmless if
// already gone.
func dropHubEntries(t *testing.T, c *tk.Central, hubID string) {
	t.Helper()
	if hubID == "" {
		return
	}
	t.Cleanup(func() {
		for _, ns := range []string{"facility-hub-registry", "facility-hub-status"} {
			c.Do("DELETE", "/api/dataStore/"+ns+"/"+hubID, nil)
		}
		// The hub's issue reports (refused changes, as <hub ID>-<n>) name their writer too.
		var keys []string
		if c.JSON("GET", "/api/dataStore/facility-hub-issues", nil, &keys) == nil {
			for _, k := range keys {
				if strings.HasPrefix(k, hubID+"-") {
					c.Do("DELETE", "/api/dataStore/facility-hub-issues/"+k, nil)
				}
			}
		}
	})
}

// anon makes a request to the hub with no credentials at all: a device on the facility
// network that never logged in. accept is the Accept header (text/html for a browser
// navigation, else an XHR-style JSON request). It returns status, headers and body.
func (s *scene) anon(method, path, accept string, body []byte) (int, http.Header, []byte) {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, s.Srv.URL+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// hasTrackerItems reports whether a body carries tracker records (people, enrollments
// or events) in any of the shapes central answers with.
func hasTrackerItems(b []byte) (string, bool) {
	var r map[string]any
	if json.Unmarshal(b, &r) != nil {
		return "", false
	}
	for _, k := range []string{"trackedEntities", "enrollments", "events", "instances"} {
		if l, ok := r[k].([]any); ok && len(l) > 0 {
			return k, true
		}
	}
	return "", false
}

// L5: a non-staff device on the facility network, with no login, asks the hub for
// everything it can think of, online and offline. Must hold: it gets no patient data
// (I3); the hub fails closed (I5); the setup page is gone once the hub is set up; the
// anonymous body limit holds. Every unauthenticated route's answer is recorded (I8).
func TestL5NoLoginGetsNothing(t *testing.T) {
	s := begin(t)
	dropHubEntries(t, s.Central, s.App.Hub.HubID())
	p := s.M.Primary()
	fac := s.M.Facility.ID

	// Data routes a client would try, and the hub's own pages.
	dataRoutes := []string{
		"/api/tracker/trackedEntities?program=" + p.ID + "&orgUnits=" + fac + "&fields=*&pageSize=100",
		"/api/tracker/trackedEntities?orgUnitMode=ACCESSIBLE&program=" + p.ID + "&fields=*",
		"/api/tracker/trackedEntities?trackedEntityType=" + p.TET + "&orgUnits=" + fac + "&fields=*",
		"/api/tracker/events?program=" + p.ID + "&orgUnit=" + fac + "&fields=*",
		"/api/tracker/enrollments?program=" + p.ID + "&orgUnits=" + fac + "&fields=*",
		"/api/trackedEntityInstances.json?ou=" + fac + "&program=" + p.ID,
		"/api/events.json?orgUnit=" + fac + "&program=" + p.ID,
		"/api/me",
		"/api/me?fields=*",
		"/api/dataStore/facility-hub-admin/accounts",
		"/api/dataStore/facility-hub-registry/" + s.App.Hub.HubID(),
		"/api/dataValueSets.json?orgUnit=" + fac + "&dataSet=any&period=202601",
		"/api/apiToken",
	}
	hubRoutes := []string{"/hub/status", "/hub/setup", "/hub/ui", "/hub/ui/endpoints", "/hub/ca.crt", "/hub/login", "/"}

	leaked := func(label string, routes []string) {
		for _, path := range routes {
			for _, accept := range []string{"application/json", "text/html"} {
				st, h, b := s.anon("GET", path, accept, nil)
				body := b
				if len(body) > 160 {
					body = body[:160]
				}
				s.t.Logf("%s anon GET %s (accept %s): %d %s %.160s", label, path, accept[:4], st, h.Get("Location"), body)
				if st == 200 {
					if k, ok := hasTrackerItems(b); ok {
						t.Errorf("I3: anon GET %s returned %s data without a login (%d items region)", path, k, len(b))
					}
				}
			}
		}
	}

	// Online: the hub forwards an anonymous request to central with no credentials.
	leaked("online", dataRoutes)
	// Offline: the hub answers from its caches, or refuses.
	s.Offline()
	leaked("offline", dataRoutes)
	s.Online()

	// The hub's own pages, online. /hub/status and /hub/ca.crt are public by design
	// (status shows only counts and UIDs; the CA is for installing on devices).
	for _, path := range hubRoutes {
		st, h, b := s.anon("GET", path, "text/html", nil)
		s.t.Logf("anon GET %s: %d %s %.120s", path, st, h.Get("Location"), b[:min(len(b), 120)])
	}
	// The setup page is gone once the hub is set up: it must not take another set-up.
	if st, h, _ := s.anon("GET", "/hub/setup", "text/html", nil); st == 200 {
		t.Errorf("I5: the setup page is served after set-up (%d)", st)
	} else {
		s.t.Logf("setup after set-up: %d -> %s", st, h.Get("Location"))
	}
	// /hub/status must carry no patient data, only counts and UIDs.
	if _, _, b := s.anon("GET", "/hub/status", "application/json", nil); func() bool { _, ok := hasTrackerItems(b); return ok }() {
		t.Errorf("I3: /hub/status carries tracker data: %.300s", b)
	}

	// The anonymous body limit (64 KiB): a device must not be able to make the hub
	// buffer a large body before it is even authenticated (I7). The proxy routes cap at
	// MaxAnonymousBody and answer 413; /api/auth/login has its own 64 KiB cap and
	// rejects an oversized body with 400 before reading it whole. Both fail closed.
	big := make([]byte, int(core.MaxAnonymousBody)+1<<10)
	for _, path := range []string{"/api/tracker", "/api/dataValueSets"} {
		st, _, _ := s.anon("POST", path, "application/json", big)
		s.t.Logf("anon POST %s with %d KiB: %d", path, len(big)/1024, st)
		if st != http.StatusRequestEntityTooLarge {
			t.Errorf("I7: anon POST %s with %d KiB got %d, want 413", path, len(big)/1024, st)
		}
	}
	if st, _, _ := s.anon("POST", "/api/auth/login", "application/json", big); st != 400 && st != http.StatusRequestEntityTooLarge {
		t.Errorf("I7: anon POST /api/auth/login with a large body got %d, want 400 or 413", st)
	} else {
		s.t.Logf("anon POST /api/auth/login with %d KiB: %d (bounded)", len(big)/1024, st)
	}
	// A normal login still works: the hub is not simply refusing everyone.
	c := s.Web(tk.RoleNurse)
	c.Login()
}

// craftedQueries are the shapes a user might try to reach beyond their scope with: all
// org-unit modes, paging to the end, a fields explosion, includeDeleted, by tracked
// entity type without a program, and (where the program has one) a filter on the
// unique attribute to enumerate a known value. Each is answered by the hub itself
// offline, so each is a chance to serve more than central would.
func craftedQueries(m *tk.Manifest, p tk.Program, uniqueValue string) []string {
	fac, dist := m.Facility.ID, m.District.ID
	te := "trackedEntity,orgUnit,createdBy,attributes[attribute,value],enrollments[enrollment,status,orgUnit,events[event,programStage,dataValues]]"
	q := func(v url.Values) string { return "/api/tracker/trackedEntities?" + v.Encode() }
	ev := func(v url.Values) string { return "/api/tracker/events?" + v.Encode() }
	var out []string
	out = append(out,
		q(url.Values{"program": {p.ID}, "orgUnitMode": {"ACCESSIBLE"}, "fields": {te}, "paging": {"false"}}),
		q(url.Values{"program": {p.ID}, "orgUnitMode": {"DESCENDANTS"}, "orgUnits": {dist}, "fields": {te}, "paging": {"false"}}),
		q(url.Values{"program": {p.ID}, "orgUnits": {fac}, "fields": {te}, "includeDeleted": {"true"}, "page": {"1"}, "pageSize": {"10000"}}),
		q(url.Values{"trackedEntityType": {p.TET}, "orgUnitMode": {"ACCESSIBLE"}, "fields": {te}, "paging": {"false"}}),
		ev(url.Values{"program": {p.ID}, "orgUnit": {fac}, "fields": {"event,programStage,status,orgUnit,dataValues"}, "paging": {"false"}}),
	)
	// The hidden stage, by stage, and the whole district's events: the restricted role's
	// stage must not surface this way.
	if p.RestrictedStage != "" {
		out = append(out, ev(url.Values{"program": {p.ID}, "orgUnit": {fac}, "programStage": {p.RestrictedStage},
			"fields": {"event,programStage,status,dataValues"}, "paging": {"false"}}))
	}
	if p.UniqueAttr != "" && uniqueValue != "" {
		out = append(out, q(url.Values{"program": {p.ID}, "orgUnitMode": {"ACCESSIBLE"}, "fields": {te},
			"filter": {p.UniqueAttr + ":eq:" + uniqueValue}}))
	}
	return out
}

// singleLeak compares a by-uid read of one object through the hub with central's answer
// to the same user: a leak is the hub serving (200) an object central hides from them.
func (s *scene) singleLeak(c *tk.Client, path string) bool {
	s.t.Helper()
	hst, _, hb := c.DoHeader("GET", path)
	var cst int
	if s.Live() {
		cst, _, _ = s.Central.As(c.User, s.M.Password).Do("GET", path, nil)
	}
	leak := hst == 200 && cst != 200
	s.t.Logf("by-uid %s as %s (online=%v): hub %d, central %d%s", path, c.User, !s.Net.Down(), hst, cst,
		map[bool]string{true: " LEAK"}[leak])
	if leak && len(hb) > 0 {
		s.t.Logf("  hub served: %.200s", hb)
	}
	return leak
}

// L1: a facility user with a narrow scope asks the hub, with crafted queries, for data
// other users hold at the same facility — other programs, a hidden stage, people by
// unique value, single objects by uid. Must hold: nothing beyond central (I3), online
// and offline. The hub's answer is compared with central's for the very same user, so
// what central would refuse the hub must refuse too.
func TestL1CraftedQueriesForOthersData(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	dropHubEntries(t, s.Central, s.App.Hub.HubID())

	// The nurse plants records other users should not fully see: in the protected
	// program, and in a program with a unique attribute to enumerate.
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	planted := map[string]*tk.Person{}
	uniques := map[string]string{}
	for _, p := range s.M.Programs {
		if p.Event {
			continue
		}
		name := "ZqL1" + strings.ToLower(tk.UID()[:5])
		pr := s.M.NewPerson(p, name)
		nurse.Complete(p, pr)
		if st, b := s.save(nurse, "plant in "+p.ID, pr.Payload); st != 200 {
			s.t.Logf("could not plant in %s (%d %.120s); skipping it", p.ID, st, b)
			continue
		}
		planted[p.ID] = pr
		if p.UniqueAttr != "" {
			uniques[p.ID] = uniqueValueOf(pr, p.UniqueAttr)
		}
	}
	s.Sync()

	// The attackers: a user whose stage is hidden, and a matrix user with no access to
	// the protected program. Two is enough to keep the run under the test timeout; the
	// oracle covers every user.
	attackers := []string{tk.RoleRestricted}
	for _, mu := range s.M.Matrix {
		if mu.Dims["protected"] == "none" && mu.Dims["capture"] == "facility" {
			attackers = append(attackers, mu.Username)
			break
		}
	}

	// Targeted programs: the protected one, the primary (its restricted stage), and one
	// with a unique attribute, deduped.
	targets := map[string]tk.Program{}
	for _, p := range s.M.Programs {
		if p.Event {
			continue
		}
		if p.AccessLevel == "PROTECTED" || p.ID == s.M.Primary().ID || p.RestrictedStage != "" || (p.UniqueAttr != "" && len(targets) < 3) {
			targets[p.ID] = p
		}
	}

	leaks, lenient := 0, 0
	visible := map[string]map[string]bool{}
	for _, who := range attackers {
		u, ok := s.M.User(who)
		username := who
		if ok {
			username = u.Username
		} else if !strings.HasPrefix(who, "syn_") {
			s.t.Logf("no %s role in this setup; skipping", who)
			continue
		}
		c := s.connect(username)
		s.Sync()
		for _, offline := range []bool{false, true} {
			if offline {
				s.Offline()
			} else {
				s.Online()
			}
			for _, p := range targets {
				for _, path := range craftedQueries(s.M, p, uniques[p.ID]) {
					coll := "trackedEntities"
					key := "trackedEntity"
					if strings.Contains(path, "/events?") {
						coll, key = "events", "event"
					}
					extra := s.leaks(c, path, coll, key)
					if len(extra) > 0 && coll == "trackedEntities" {
						// Central may refuse the shape itself (409: an ACCESSIBLE search
						// needs an attribute; the oracle's "lenient"). Then what the hub
						// answers must still be people central lists to this user at the
						// facility, page by page.
						scope := "program=" + p.ID
						if strings.Contains(path, "trackedEntityType=") && !strings.Contains(path, "program=") {
							scope = "trackedEntityType=" + p.TET
						}
						vis := s.visibleAtFacility(c.User, scope, visible)
						before := len(extra)
						extra = slices.DeleteFunc(extra, func(u string) bool { return vis[u] })
						lenient += before - len(extra)
					}
					if len(extra) > 0 {
						leaks += len(extra)
						t.Errorf("I3: %s (offline=%v) sees via the hub %d %s central does not list to them: %v", username, offline, len(extra), coll, first(extra))
					}
				}
				// By uid: the planted person, with and without a program.
				if pr := planted[p.ID]; pr != nil {
					for _, path := range []string{
						"/api/tracker/trackedEntities/" + pr.TE + "?program=" + p.ID + "&fields=*",
						"/api/tracker/trackedEntities/" + pr.TE + "?fields=*",
					} {
						if s.singleLeak(c, path) {
							leaks++
							t.Errorf("I3: %s (offline=%v) reads %s by uid where central refuses", username, offline, pr.TE)
						}
					}
				}
			}
			s.Online()
		}
	}
	s.t.Logf("L1: %d attacker(s), %d targeted program(s); leaks found: %d; hub answers to shapes central refuses (409), all within the user's facility listing: %d", len(attackers), len(targets), leaks, lenient)
}

// visibleAtFacility is every tracked entity central lists to user at the facility in a
// scope ("program=..." or "trackedEntityType=..."), read page by page; memo caches it.
func (s *scene) visibleAtFacility(user, scope string, memo map[string]map[string]bool) map[string]bool {
	s.t.Helper()
	if v := memo[user+"|"+scope]; v != nil {
		return v
	}
	as := s.Central.As(user, s.M.Password)
	out := map[string]bool{}
	for page := 1; ; page++ {
		var res struct {
			TEs []struct{ TrackedEntity string } `json:"trackedEntities"`
		}
		path := "/api/tracker/trackedEntities?" + scope + "&orgUnits=" + s.M.Facility.ID +
			"&fields=trackedEntity&pageSize=1000&page=" + fmt.Sprint(page)
		if err := as.JSON("GET", path, nil, &res); err != nil {
			s.t.Logf("listing %s for %s: %v", scope, user, err)
			break
		}
		for _, te := range res.TEs {
			out[te.TrackedEntity] = true
		}
		if len(res.TEs) < 1000 {
			break
		}
	}
	memo[user+"|"+scope] = out
	return out
}

// L2: offline password guessing against the hub. A device on the facility network
// tries many passwords for a known staff account while the uplink is down. Must hold:
// the attempts are throttled (the login throttle, M3/S12); a wrong password never opens
// a key slot, so a locked store stays locked; the failure is uniform (an unknown user
// costs the same as a wrong password, so the hub does not reveal which usernames it
// knows). The per-guess Argon2id cost against a copied slot is measured, not cracked.
func TestL2OfflinePasswordGuessing(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	dropHubEntries(t, s.Central, s.App.Hub.HubID())
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	if st, _ := nurse.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("nurse online login failed")
	}
	s.Sync()
	user := nurse.User

	// The failure is uniform: a wrong password for a known user and any password for an
	// unknown user both pay one Argon2id derivation and return the same answer.
	s.Offline()
	login := func(username, pw string) (int, time.Duration) {
		c := s.Web(tk.RoleNurse) // a web client; the username below is what matters
		c.User = username
		start := time.Now()
		st, _, _, err := c.Try("POST", "/api/auth/login", map[string]string{"username": username, "password": pw})
		if err != nil {
			t.Fatal(err)
		}
		return st, time.Since(start)
	}
	var known, unknown time.Duration
	for i := 0; i < 3; i++ {
		stK, dK := login(user, "wrong-"+tk.UID())
		stU, dU := login("nouser-"+strings.ToLower(tk.UID()[:6]), "wrong-"+tk.UID())
		known, unknown = known+dK, unknown+dU
		if stK != stU {
			t.Errorf("S/M3: offline a wrong password (%d) and an unknown user (%d) answer differently: the hub reveals which usernames it knows", stK, stU)
		}
	}
	s.t.Logf("L2 uniform failure: wrong password for a known user %s, unknown user %s (per attempt, 3 each)", (known / 3).Round(time.Millisecond), (unknown / 3).Round(time.Millisecond))

	// The per-guess cost against a copied key slot: the only offline verifier the hub
	// keeps is the key slot (Argon2id over the password). Time one unwrap with a wrong
	// password, as an attacker with the stolen store would pay per guess.
	u, err := s.App.Hub.Store.UserByName(user)
	if err != nil || u == nil || len(u.WrappedDK) == 0 {
		t.Fatalf("no key slot for %s to measure against: %v", user, err)
	}
	start := time.Now()
	if _, _, err := hubcrypto.UnwrapSlot(u.WrappedDK, "definitely-wrong", u.KEKSalt, devkey.Key()); err == nil {
		t.Error("a wrong password opened the key slot")
	}
	s.t.Logf("L2 copied-slot cost: one offline guess against %s's key slot costs %s of Argon2id (no key slot opened)", user, time.Since(start).Round(time.Millisecond))

	// Throttling: five wrong passwords from this device lock the account from it, and a
	// sixth attempt is refused (429) even with the right password: the hub fails closed.
	s.Online() // online or offline, the throttle fires before central is asked
	for i := 0; i < 5; i++ {
		login(user, "guess-"+tk.UID())
	}
	st, _ := login(user, s.M.Password)
	if st != http.StatusTooManyRequests {
		t.Errorf("S/M3: after five wrong passwords the sixth got %d, want 429 (throttle)", st)
	}
	// Basic auth while the account is locked out. The nurse's tablet logged in with
	// Basic earlier, so the hub holds that credential in its cache (live for up to 12
	// hours, idle 1 hour, core/sessions.go). Guesses over Basic from another device on
	// the facility network, locked out as this one is: how fast are they answered, and
	// do the right and wrong passwords answer alike? (They told them apart, FINDINGS.md.)
	device := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "10.66.0.9:40000" // the other device's address, as the hub sees it
		s.App.Handler.ServeHTTP(w, r)
	}))
	defer device.Close()
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("POST", device.URL+"/api/auth/login",
			strings.NewReader(`{"username":"`+user+`","password":"guess-`+tk.UID()+`"}`))
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	basicGuess := func(pw string) (int, time.Duration) {
		start := time.Now()
		req, _ := http.NewRequest("GET", device.URL+"/api/me?fields=id", nil)
		req.SetBasicAuth(user, pw)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, time.Since(start)
	}
	wrongCodes := map[int]int{}
	var wrongTime time.Duration
	const guesses = 50
	for i := 0; i < guesses; i++ {
		st, d := basicGuess("guess-" + tk.UID())
		wrongCodes[st]++
		wrongTime += d
	}
	rightSt, rightD := basicGuess(s.M.Password)
	s.t.Logf("L2 Basic during lockout: %d wrong guesses answered %v in %s on average; the right password answered %d in %s",
		guesses, wrongCodes, (wrongTime / guesses).Round(time.Microsecond), rightSt, rightD.Round(time.Microsecond))
	oracle := rightSt == 200 && wrongCodes[200] == 0
	if other, ok := s.M.User(tk.RoleClerk); ok {
		s.Online()
		oc := s.connect(other.Username)
		if ost, _ := oc.Do("GET", "/api/me?fields=id", nil); ost != 200 {
			t.Errorf("another user locked out by guesses against the nurse: %d", ost)
		} else {
			s.t.Logf("L2 throttle: the nurse's device is locked out (429); the clerk is unaffected (200)")
		}
	}
	if st, _ := nurse.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Errorf("L2: during the lockout the nurse's own tablet, with its cached credential, got %d, want 200", st)
	}
	if oracle {
		t.Errorf("L2: locked-out account, Basic auth: %d wrong guesses all refused fast (%v) while the right password is accepted (%d): the lockout tells right from wrong without Argon2 or central", guesses, wrongCodes, rightSt)
	}
}

// L4: a copied data folder read on another machine. Patient data the hub holds must
// not be readable without a password: the store files are encrypted at rest. Must hold:
// a distinctive value the hub pulled and stored appears in cleartext in none of the
// files in the data directory (I3). What is plaintext there by design (the hub's CA and
// server key, the setup token, the network config — docs/DEPLOYMENT.md section 2) is
// listed and checked to hold no patient data. Sandbox part only; the USB stick's copy
// is the same encrypted store (docs/ADVERSARIAL-TESTING.md, L4).
func TestL4CopiedDiskReadElsewhere(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	dropHubEntries(t, s.Central, s.App.Hub.HubID())

	// A person with a value found nowhere else, pulled into the hub's store.
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	needle := "ZqL4needle" + strings.ToUpper(tk.UID()[:8])
	p := s.M.Primary()
	pr := s.M.NewPerson(p, needle)
	nurse.Complete(p, pr)
	if st, b := s.save(nurse, "register the needle", pr.Payload); st != 200 {
		t.Fatalf("registering the needle: %d %.200s", st, b)
	}
	s.Sync()
	// Read it back through the hub offline, so the hub has it cached and indexed.
	s.Offline()
	if st, _ := nurse.Do("GET", "/api/tracker/trackedEntities/"+pr.TE+"?program="+p.ID+"&fields=*", nil); st != 200 {
		s.t.Logf("the needle reads back offline: %d", st)
	}
	s.Online()
	s.Sync()

	// Walk every file a thief would have on the copied disk.
	var files []string
	if err := filepath.WalkDir(s.Dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	needleB := []byte(needle)
	inClear := 0
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			s.t.Logf("  %s: unreadable (%v)", filepath.Base(path), err)
			continue
		}
		printable := isMostlyText(b)
		has := bytes.Contains(b, needleB)
		s.t.Logf("  %s: %d bytes, text=%v, holds the needle=%v", strings.TrimPrefix(path, s.Dir+"/"), len(b), printable, has)
		if has {
			inClear++
			t.Errorf("I3: patient data (%q) is in cleartext in %s", needle, filepath.Base(path))
		}
	}
	s.t.Logf("L4: %d file(s) in the data folder; patient data in clear in %d of them", len(files), inClear)
}

// isMostlyText reports whether a blob looks like text (for the at-rest inventory): the
// encrypted store files read as binary, the CA and config files as text.
func isMostlyText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	n := min(len(b), 2048)
	nonText := 0
	for _, c := range b[:n] {
		if c < 9 || (c > 13 && c < 32) {
			nonText++
		}
	}
	return nonText*20 < n // under 5% control bytes
}

// writeDataStore writes a dataStore entry as a given central user and returns the
// status central answered. When the test ends the entry is put back as it was: its
// earlier value restored (hubtest provisions facility-hub-admin/central-move), or deleted
// if it did not exist. A refused write (403 to an ordinary user) is returned, not fatal.
func (s *scene) writeDataStore(as *tk.Central, ns, key string, body any) int {
	s.t.Helper()
	path := "/api/dataStore/" + ns + "/" + key
	ost, old, _ := s.Central.Do("GET", path, nil)
	st, b, err := as.Do("PUT", path, body)
	if err == nil && st == 404 {
		st, b, err = as.Do("POST", path, body)
	}
	if err != nil {
		s.t.Fatalf("writing %s as %s: %v", path, as.User, err)
	}
	s.t.Logf("%s writes %s: %d %.150s", as.User, path, st, b)
	if st < 300 {
		s.t.Cleanup(func() {
			if ost == 200 {
				s.Central.Do("PUT", path, old)
			} else {
				s.Central.Do("DELETE", path, nil)
			}
		})
	}
	return st
}

// setPublicAccess sets a dataStore entry's public access (e.g. "rw------" for an
// unprotected entry) and restores its sharing when the test ends.
func (s *scene) setPublicAccess(path, access string) {
	s.t.Helper()
	var meta struct {
		ID string `json:"id"`
	}
	if err := s.Central.JSON("GET", path+"/metaData", nil, &meta); err != nil || meta.ID == "" {
		s.t.Fatalf("metadata of %s: %v", path, err)
	}
	sp := "/api/sharing?type=dataStore&id=" + meta.ID
	var cur map[string]any
	if err := s.Central.JSON("GET", sp, nil, &cur); err != nil {
		s.t.Fatalf("sharing of %s: %v", path, err)
	}
	obj, _ := cur["object"].(map[string]any)
	was := map[string]any{}
	for k, v := range obj {
		was[k] = v
	}
	obj["publicAccess"] = access
	if err := s.Central.JSON("POST", sp, map[string]any{"object": obj}, nil); err != nil {
		s.t.Fatalf("sharing %s publicly %s: %v", path, access, err)
	}
	s.t.Cleanup(func() { s.Central.Do("POST", sp, map[string]any{"object": was}) })
}

// L3 (move): a central-move announcement forged to point the hub at an address of the
// forger's choosing. Must hold: the hub never adopts it. Two layers: (1) an ordinary
// user cannot write facility-hub-admin/central-move where the namespace is protected, as
// hubtest provisions it (public read-only); (2) where it is not (the entry made writable
// by any user here), the hub itself ignores the announcement (core/move.go
// refreshCentralMove, docs/DEPLOYMENT.md section 6). Either way the central URL stays
// and the forged address is never contacted.
func TestL3ForgedMoveAnnouncement(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	dropHubEntries(t, s.Central, s.App.Hub.HubID())
	h := s.App.Hub
	before := h.Central.BaseURL()

	// A recording stand-in the forged announcement points at.
	var hits int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(404)
	}))
	defer fake.Close()
	forged := map[string]any{"url": "https://" + strings.TrimPrefix(fake.URL, "http://"), "from": "2000-01-01T00:00:00Z"}
	path := "/api/dataStore/facility-hub-admin/central-move"
	check := func(what string) {
		s.Sync() // RefreshCatalog -> refreshCentralMove
		h.TryCentralMove(context.Background())
		next := h.Store.Meta(core.MetaCentralNext)
		s.t.Logf("L3 move, %s: central URL %q, stored next %q, forged address contacted %d time(s)", what, h.Central.BaseURL(), next, atomic.LoadInt32(&hits))
		if got := h.Central.BaseURL(); got != before {
			t.Errorf("L3 (%s): a forged move announcement changed the central URL to %q", what, got)
		}
		if next != "" {
			t.Errorf("L3 (%s): the hub stored the forged address %q as central's next", what, next)
		}
		if n := atomic.LoadInt32(&hits); n != 0 {
			t.Errorf("L3 (%s): the hub contacted the forged address %d time(s)", what, n)
		}
	}

	// (1) An ordinary user, against the namespace as provisioned.
	nurse, _ := s.M.User(tk.RoleNurse)
	if st := s.writeDataStore(s.Central.As(nurse.Username, s.M.Password), "facility-hub-admin", "central-move", forged); st < 300 {
		t.Logf("an ordinary user may write central-move here (%d): the namespace is not protected", st)
	}
	check("ordinary user, namespace as provisioned")

	// (2) The entry left writable by any user (an instance that skipped the protection
	// step), carrying the forged address.
	s.setPublicAccess(path, "rw------")
	s.writeDataStore(s.Central, "facility-hub-admin", "central-move", forged)
	check("entry writable by any user")
}

// L3 (registry): an ordinary user forges this hub's registry entry, marking it
// "retired" (or "revoked"). Must hold (as DEPLOYMENT.md requires the namespace
// protected): the hub ignores a status it cannot trust. This instance's namespaces are
// unprotected (no hub owners group is named, so ProtectRegistry leaves the entry open;
// CheckProtection warns). Accepted for "retired" (FINDINGS.md 16, DEPLOYMENT.md "Protect
// the data store namespaces"): it costs offline service, no data, and ends when the entry
// is active again; the test checks exactly that.
func TestL3ForgedRegistryStatus(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	h := s.App.Hub
	dropHubEntries(t, s.Central, h.HubID())
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	s.Sync()

	// Can an ordinary user write this hub's registry entry at all?
	as := s.Central.As(nurse.User, s.M.Password)
	path := core.RegistryPath(h.HubID())
	var reg map[string]any
	if err := as.JSON("GET", path, nil, &reg); err != nil || reg == nil {
		t.Skipf("the ordinary user cannot read the registry entry (%v); the namespace is already protected", err)
	}
	reg["status"] = "retired"
	st, b, err := as.Do("PUT", path, reg)
	s.t.Logf("ordinary user writes status=retired to the hub's registry entry: %d %v %.150s", st, err, b)
	if st >= 300 {
		s.t.Logf("L3 registry: the namespace refuses an ordinary user's write (%d): protected; the hub cannot be forged this way", st)
		return
	}

	s.Sync() // checkRegistry reads the entry
	retired := h.Retired()
	s.t.Logf("after the forged write, the hub considers itself retired: %v; unprotected namespaces reported by the hub: %v", retired, h.Unprotected())

	// Offline, a retired hub stops serving patient data: a forged retire is a denial of
	// service on an unprotected instance.
	stReal := -1
	if retired {
		s.Offline()
		stReal, _ = nurse.Do("GET", "/api/tracker/trackedEntities?program="+s.M.Primary().ID+"&orgUnits="+s.M.Facility.ID+"&fields=trackedEntity&pageSize=5", nil)
		s.Online()
		s.t.Logf("retired hub offline serves the facility's people: %d (503 = refused)", stReal)
	}
	// Restore the entry: the hub must serve again by itself (FINDINGS.md 16: a forged
	// entry "costs offline service, no data, and ends when the entry is active again").
	reg["status"] = "active"
	s.Central.Do("PUT", path, reg)
	s.Sync()
	s.Offline()
	after, _ := nurse.Do("GET", "/api/tracker/trackedEntities?program="+s.M.Primary().ID+"&orgUnits="+s.M.Facility.ID+"&fields=trackedEntity&pageSize=5", nil)
	s.Online()
	s.t.Logf("L3 registry, retired: forged by an ordinary user, the hub retired %v, served offline %d; after the entry was restored, retired %v, served offline %d", retired, stReal, h.Retired(), after)
	if retired && (h.Retired() || after != 200) {
		t.Errorf("I6: the hub did not come back after the forged 'retired' was undone (retired %v, offline read %d)", h.Retired(), after)
	}
	if retired && stReal == 200 {
		t.Errorf("a retired hub still served patient data offline")
	}
}

// L3 (registry, revoked): as above with status "revoked", the Facility Hub app's mark
// for a hub whose tokens a manager revoked. A hub that read it deleted its own and its
// users' tokens on central and stopped syncing until it was reset, with a save waiting
// offline (FINDINGS.md). It now takes "revoked" as real only once central refuses its
// own token: the forged entry is ignored and the save is pushed. A new nurse is used,
// so only a test user's tokens could be deleted.
func TestL3ForgedRegistryRevoked(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	h := s.App.Hub
	dropHubEntries(t, s.Central, h.HubID())
	u := s.NewUserLike(tk.RoleNurse)
	c := s.connect(u.Username)
	s.Sync()
	p := s.M.Primary()
	s.Offline()
	pr := s.M.NewPerson(p, "ZqL3rev"+strings.ToLower(tk.UID()[:5]))
	c.Complete(p, pr)
	if st, _ := s.save(c, "a registration offline", pr.Payload); st != 200 {
		t.Fatal("the hub refused a valid offline save")
	}
	// The forged entry is written before the hub reconnects; the cycle then reads it.
	as := s.Central.As(u.Username, s.M.Password)
	path := core.RegistryPath(h.HubID())
	var reg map[string]any
	if err := as.JSON("GET", path, nil, &reg); err != nil {
		t.Skipf("the ordinary user cannot read the registry entry: %v", err)
	}
	reg["status"] = "revoked"
	if st, b, _ := as.Do("PUT", path, reg); st >= 300 {
		s.t.Logf("the namespace refuses the ordinary user (%d %.100s): protected", st, b)
		return
	}
	s.Online()
	if err := s.App.Worker.Cycle(context.Background()); err != nil {
		s.t.Logf("sync: %v", err)
	}
	pending := h.Store.CountOutbox(store.OutboxPending)
	onCentral := s.CentralObject("trackedEntities", pr.TE) != nil
	s.t.Logf("after a forged 'revoked': hub revoked %v; the offline registration on central %v, still queued %d; status %v",
		h.Revoked(), onCentral, pending, ui.StatusBody(h)["message"])

	// Put the entry back: does the hub come back by itself?
	reg["status"] = "active"
	s.Central.Do("PUT", path, reg)
	for range 2 {
		s.App.Worker.Cycle(context.Background())
	}
	recovered := !h.Revoked()
	st, _ := c.Do("GET", "/api/me?fields=id", nil)
	s.t.Logf("entry restored: hub revoked %v; the nurse's request %d; queued %d; registration on central %v",
		h.Revoked(), st, h.Store.CountOutbox(store.OutboxPending), s.CentralObject("trackedEntities", pr.TE) != nil)

	if h.Revoked() || !recovered || !onCentral {
		t.Errorf("L3: a forged 'revoked' by an ordinary user disabled the hub (revoked %v after the entry was restored; %d save(s) waiting, on central %v)", h.Revoked(), pending, onCentral)
	}
}

// recordingCentral is a stand-in for central that records every request's method,
// path, the credential it carried, and whether a password appeared in the body.
type recordingCentral struct {
	srv    *httptest.Server
	mu     sync.Mutex
	hits   []string
	tokens int
	creds  int
}

func newRecordingCentral(t *testing.T, withTLS bool) *recordingCentral {
	rc := &recordingCentral{}
	newSrv := httptest.NewServer
	if withTLS {
		newSrv = httptest.NewTLSServer // a certificate no client trusts
	}
	rc.srv = newSrv(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cred := "none"
		if r.Header.Get("Authorization") != "" || r.Header.Get("ApiToken") != "" {
			cred = "token/basic"
			rc.mu.Lock()
			rc.tokens++
			rc.mu.Unlock()
		}
		if bytes.Contains(b, []byte(`"password"`)) || bytes.Contains(bytes.ToLower(b), []byte("password=")) {
			rc.mu.Lock()
			rc.creds++
			rc.mu.Unlock()
		}
		rc.mu.Lock()
		rc.hits = append(rc.hits, r.Method+" "+r.URL.Path+" ["+cred+"]")
		rc.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/ping"):
			w.Write([]byte("pong"))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404) // the fake cannot produce this hub's registry entry
			w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *recordingCentral) seen() ([]string, int, int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]string(nil), rc.hits...), rc.tokens, rc.creds
}

// L6: a fake central. What does the hub send to a server pretending to be central?
// Part A (application layer): a correctly-vouched move announcement pointing at the fake
// does not move this pinned hub, and the hub sends nothing to the announced address
// (core/move.go: a pinned hub never moves; a move is attempted only while the current
// central does not answer, and only to a server that proves it holds the hub's registry
// entry). Part B (transport layer): a fake answering at central's address (DNS spoofed
// on the facility network). B1: with a certificate the hub cannot verify, nothing must
// reach it. B2: with the transport fully subverted (plain http here, as a MITM holding a
// valid certificate would be), what crosses is measured: the defense there is verified
// TLS alone.
func TestL6FakeCentral(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	h := s.App.Hub
	dropHubEntries(t, s.Central, h.HubID())
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	s.Sync()
	fake := newRecordingCentral(t, false)
	fakeURL := "https://" + strings.TrimPrefix(fake.srv.URL, "http://")
	before := h.Central.BaseURL()

	// Part A: an announcement the hub would trust (written by the superuser, shared
	// read-only so the writable-by-all guard passes), pointing at the fake.
	path := "/api/dataStore/facility-hub-admin/central-move"
	s.writeDataStore(s.Central, "facility-hub-admin", "central-move",
		map[string]any{"url": fakeURL, "from": "2000-01-01T00:00:00Z"})
	s.setPublicAccess(path, "r-------")
	s.Sync()
	s.t.Logf("L6 part A: the hub took the vouched announcement as central's next address: %q", h.Store.Meta(core.MetaCentralNext))
	h.TryCentralMove(context.Background())
	if got := h.Central.BaseURL(); got != before {
		t.Errorf("L6: a pinned hub moved its central URL to the announced fake: %q", got)
	}
	if hits, _, _ := fake.seen(); len(hits) > 0 {
		t.Errorf("L6: the pinned hub contacted the announced fake: %v", hits)
	}
	s.t.Logf("L6 part A: a vouched announcement did not move the pinned hub; the fake was contacted 0 times (central URL %q)", h.Central.BaseURL())

	// Part B: the uplink is subverted, so the fake answers at central's address. What
	// does the hub send on its housekeeping (its own token) and on a forwarded staff login
	// (loginWeb's online path posts the password verbatim to central)?
	ctx := context.Background()
	loginBody, _ := json.Marshal(map[string]string{"username": nurse.User, "password": s.M.Password})
	contact := func(base string) error {
		h.Central.SetBaseURL(base)
		defer h.Central.SetBaseURL(before)
		_, err := h.FetchMe(ctx, h.HubAuth())
		if resp, lerr := h.Central.Do(ctx, "POST", "/api/auth/login", loginBody, central.Auth{},
			http.Header{"Content-Type": {"application/json"}}); lerr == nil {
			resp.Body.Close()
		} else if err == nil {
			err = lerr
		}
		return err
	}
	// B1: the fake speaks TLS with a certificate the hub does not trust (a DNS spoof
	// without central's key). Verified TLS must stop everything before a header leaves.
	tlsFake := newRecordingCentral(t, true)
	err := contact(tlsFake.srv.URL)
	hits, tokens, creds := tlsFake.seen()
	s.t.Logf("L6 part B1 (fake with an untrusted certificate): %d request(s) reached it, token on %d, password on %d; the hub's error: %v", len(hits), tokens, creds, err)
	if len(hits) > 0 {
		t.Errorf("L6: the hub sent %d request(s) to a server whose certificate it cannot verify: %v", len(hits), hits)
	}
	// B2: the transport fully subverted (plain http here, as a MITM holding a valid
	// certificate would be): measure what crosses. This is what TLS keeps from a fake.
	plain := newRecordingCentral(t, false)
	contact(plain.srv.URL)
	hits, tokens, creds = plain.seen()
	s.t.Logf("L6 part B2 (transport subverted): the fake saw %v; the hub's token on %d request(s), a staff password on %d", hits, tokens, creds)
	if len(hits) == 0 {
		t.Errorf("L6 part B2: nothing reached the plain fake (test wiring)")
	}
}

// uniqueValueOf returns the value a person carries for an attribute, from their
// payload (the attributes are at enrollment or tracked-entity level).
func uniqueValueOf(pr *tk.Person, attr string) string {
	te, _ := pr.Payload["trackedEntities"].([]any)
	if len(te) == 0 {
		return ""
	}
	m, _ := te[0].(map[string]any)
	find := func(list any) string {
		for _, a := range asList(list) {
			am, _ := a.(map[string]any)
			if fmt.Sprint(am["attribute"]) == attr {
				return fmt.Sprint(am["value"])
			}
		}
		return ""
	}
	if v := find(m["attributes"]); v != "" {
		return v
	}
	for _, en := range asList(m["enrollments"]) {
		em, _ := en.(map[string]any)
		if v := find(em["attributes"]); v != "" {
			return v
		}
	}
	return ""
}
