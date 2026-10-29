package adversarial

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/hubaccount"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The hub's own lifecycle (group G), part 2: its store, its keys and its account.

// login logs a web client in with a password, and returns the hub's answer.
func login(c *tk.Client, password string) (int, string) {
	st, h, b, err := c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": password})
	if err != nil {
		return 0, err.Error()
	}
	return st, h.Get("X-Hub-Mode") + " " + string(b[:min(len(b), 200)])
}

// G10: nobody logs in for a long time after a restart (here 3 minutes; on a Pi, days
// after a power cut on a Friday), while the hub holds saves made offline before it.
// Must hold: clients are told (a clear answer, not a hang); nothing is lost; the first
// login unlocks and the queue goes. Measured: what the hub sends to central while
// locked.
func TestG10LockedAfterRestart(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(20 * time.Second)
	stop()
	queued := r.p.Status()["pending"]
	r.p.Kill() // a power cut
	r.e.Net.SetDown(false)
	r.p.Start()
	s := r.p.Status()
	t.Logf("restarted with %v objects queued: locked %v, mode %v, pending %v", queued, s["locked"], s["mode"], s["pending"])
	if s["locked"] != true {
		t.Fatalf("the hub started unlocked")
	}
	r.e.Net.Calls()
	time.Sleep(3 * time.Minute)
	calls := r.e.Net.Calls()
	pushes, _ := pushesOf(calls)
	s = r.p.Status()
	t.Logf("3 minutes locked, online: %s; %d pushes; status locked %v, mode %v", callStats(calls, 3*time.Minute), pushes, s["locked"], s["mode"])

	// What clients get while locked, online and offline: Capture web whose session
	// ended with the restart, and Android Capture syncing with a wrong password.
	q := url.Values{"program": {r.prog.ID}, "orgUnits": {r.e.M.Facility.ID}, "fields": {"trackedEntity"}, "pageSize": {"5"}}
	ask := func(when string) {
		web := r.e.Web(tk.RoleNurse)
		st, h, b, err := web.Try("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil)
		t.Logf("%s: a browser without a session reads: %d %q %v %.160s", when, st, h.Get("X-Hub-Mode"), err, b)
		pr := r.e.M.NewPerson(r.prog, r.tag+"lock")
		st, h, b, err = web.Try("POST", tk.SyncImport, pr.Payload)
		t.Logf("%s: and saves: %d %q %v %.160s", when, st, h.Get("X-Hub-Mode"), err, b)
		if st == 200 && accepted(st, b) {
			t.Errorf("%s: a save without a login was confirmed while the hub is locked", when)
		}
		st, b2 := web.Do("GET", "/hub/status", nil)
		t.Logf("%s: /hub/status %d %.200s", when, st, b2)
	}
	ask("locked, online")
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	ask("locked, offline")

	// The first login, offline: it unlocks; the queue goes when the uplink is back.
	web := r.e.Web(tk.RoleNurse)
	st, b := login(web, r.e.M.Password)
	t.Logf("the first login, offline: %d %s", st, b)
	r.p.WaitFor("unlocked", time.Minute, func(s map[string]any) bool { return s["locked"] == false })
	s = r.p.Status()
	t.Logf("unlocked: pending %v (was %v)", s["pending"], queued)
	if s["pending"] != queued {
		t.Errorf("I1: %v objects pending after the unlock, %v before the restart", s["pending"], queued)
	}
	r.e.Net.SetDown(false)
	r.finish(5 * time.Minute)
}

// G11: every staff member who has a key slot on the hub changes their password on
// central (a password policy forcing it, say) while saves are queued, and then the hub
// restarts. Their new passwords open no slot: nobody can unlock the hub with them.
// Must hold: recovery as RECOVERY.md describes, or the limit documented; nothing
// confirmed lost. Only fresh users of this test have slots (an administrator and a
// nurse made like the manifest's), so the manifest's passwords stay.
func TestG11AllKeySlotsLost(t *testing.T) {
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("adversarial scenarios run against a real DHIS2 (HUB_MANIFEST)")
	}
	r := newRun(t, e)
	admin, nurse := e.NewUserLike(tk.RoleAdmin), e.NewUserLike(tk.RoleNurse)
	e.SetupOnlyAs(admin.Username, e.M.Password)
	dropEntries(t, e.Central, e.App.Hub.HubID())
	web := func() *tk.Client { c := e.Web(tk.RoleNurse); c.User = nurse.Username; return c }
	if st, b := login(web(), e.M.Password); st != 200 { // the nurse's key slot
		t.Fatalf("%s online: %d %s", nurse.Username, st, b)
	}
	e.Sync()
	r.p = e.Detach()
	t.Cleanup(r.keepLog)
	r.p.WaitFor("online", time.Minute, tk.Online)
	w := &netWriter{r: r, c: web(), web: true}
	if st, b := login(w.c, e.M.Password); st != 200 || r.p.Status()["locked"] == true {
		t.Fatalf("%s online: %d %s; locked %v", nurse.Username, st, b, r.p.Status()["locked"])
	}
	saveFor := func(d time.Duration) {
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
			w.once()
		}
	}
	saveFor(10 * time.Second)
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	saveFor(20 * time.Second)
	queued := r.p.Status()["pending"]

	// Every slot's password changes on central, then the power goes.
	const newPassword = "Hub-Changed-2026!x"
	e.SetUserPassword(admin, newPassword)
	e.SetUserPassword(nurse, newPassword)
	r.p.Kill()
	r.e.Net.SetDown(false)
	r.p.Start()
	t.Logf("restarted with %v objects queued; every staff password changed on central", queued)

	n := web()
	st, b := login(n, newPassword)
	t.Logf("the nurse logs in online with the new password: %d %s; locked %v", st, b, r.p.Status()["locked"])
	a := web()
	a.User = admin.Username
	st, b = login(a, newPassword)
	t.Logf("the administrator logs in online with the new password: %d %s; locked %v", st, b, r.p.Status()["locked"])
	st, b = login(web(), e.M.Password)
	t.Logf("the nurse with the old password, online: %d %s; locked %v", st, b, r.p.Status()["locked"])
	locked := r.p.Status()["locked"] == true
	if locked {
		t.Logf("G11: with every staff password changed on central, no online login unlocks the hub")
	}

	// What staff can still do: log in with the old password while the uplink is down.
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	st, b = login(web(), e.M.Password)
	t.Logf("the nurse with the old password, offline: %d %s; locked %v", st, b, r.p.Status()["locked"])
	r.e.Net.SetDown(false)
	r.p.WaitFor("online", time.Minute, tk.Online)
	if r.p.Status()["locked"] == true {
		t.Errorf("I6: the hub cannot be unlocked by any staff member: %v objects wait; RECOVERY.md has no step for it", queued)
		return
	}
	r.p.WaitFor("drained", 5*time.Minute, tk.Drained)
	// The new password binds the slot again at the next online login.
	st, b = login(web(), newPassword)
	t.Logf("the nurse with the new password, online, after the unlock: %d %s", st, b)
	r.p.Kill()
	r.p.Start()
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	st, b = login(web(), newPassword)
	t.Logf("after another restart, the new password offline: %d %s; locked %v", st, b, r.p.Status()["locked"])
	if r.p.Status()["locked"] == true {
		t.Errorf("the new password does not open the hub after it was bound online")
	}
	r.e.Net.SetDown(false)
	r.finish(5 * time.Minute)
}

// G9: the hub's own account (approved as the Facility Hub app does) is disabled on
// central, and before that its token deleted. Must hold: staff keep working (their saves
// push with their own tokens); the hub's housekeeping (heartbeat, registry, catalog)
// is taken over, by itself or by a staff member as the hub page says; it says so.
func TestG9HubAccountRevoked(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	h := s.App.Hub
	id := h.HubID()
	account := approveAccount(t, s)
	beat := func() string {
		var b map[string]any
		s.Central.JSON("GET", "/api/dataStore/facility-hub-status/"+id, nil, &b)
		return b["time"].(string) + " owner " + b["hubTokenOwner"].(string)
	}
	initial := beat()
	t.Logf("the hub runs on its own account; heartbeat %s", initial)

	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	p := s.M.Primary()
	saveOne := func(name string) string {
		s.Offline()
		pr := s.M.NewPerson(p, name)
		nurse.Complete(p, pr)
		if st, b := s.save(nurse, "register "+name, pr.Payload); st != 200 {
			t.Fatalf("offline save: %d %s", st, b)
		}
		s.Online()
		if err := s.App.Worker.Cycle(context.Background()); err != nil {
			t.Logf("sync: %v", err)
		}
		return pr.TE
	}

	// 1. Its token deleted on central (by someone tidying tokens).
	var toks struct {
		APIToken []struct{ ID string } `json:"apiToken"`
	}
	as := tk.NewCentral(s.M.CentralURL, account.Username, h.Runtime().HubPassword)
	if err := as.JSON("GET", "/api/apiToken?fields=id&paging=false", nil, &toks); err != nil {
		t.Fatal(err)
	}
	for _, tok := range toks.APIToken {
		as.Do("DELETE", "/api/apiToken/"+tok.ID, nil)
	}
	s.ExpireTokenCache()
	te1 := saveOne("Zqhubtoka")
	cycle := func() {
		if err := s.App.Worker.Cycle(context.Background()); err != nil {
			t.Logf("sync: %v", strings.ReplaceAll(err.Error(), "\n", "; "))
		}
	}
	cycle()
	cycle()
	stuck := beat() == initial
	t.Logf("token deleted: owner %q; heartbeat %s; the nurse's save on central: %v", h.Runtime().TokenOwner(), beat(),
		s.CentralObject("trackedEntities", te1) != nil)

	// 2. The account disabled.
	before := beat()
	s.SetUserDisabled(account, true)
	s.ExpireTokenCache()
	te2 := saveOne("Zqhubtokb")
	for range 2 {
		if err := s.App.Worker.Cycle(context.Background()); err != nil {
			t.Logf("sync: %v", err)
		}
	}
	after := beat()
	onCentral := s.CentralObject("trackedEntities", te2) != nil
	adminPage := s.Web(tk.RoleAdmin)
	adminPage.Login()
	st, page := adminPage.Do("GET", "/hub/ui", nil)
	says := []string{}
	for _, w := range []string{"own account", "housekeeping", "take over", "Take over", "heartbeat", "disabled"} {
		if strings.Contains(string(page), w) {
			says = append(says, w)
		}
	}
	t.Logf("account disabled: heartbeat before %s, after %s; the nurse's save on central: %v; the hub page (%d) mentions %v",
		before, after, onCentral, st, says)
	if !onCentral {
		t.Errorf("staff saves stop when the hub's account is disabled")
	}
	if after != before {
		t.Logf("the heartbeat still goes")
	}

	// 3. A staff member takes the housekeeping over on the hub page.
	resp, err := adminPage.PostForm("/hub/ui/takeover", url.Values{"password": {s.M.Password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	msg, _ := url.QueryUnescape(resp.Header.Get("Location"))
	cycle()
	t.Logf("taken over: %s; owner %q; heartbeat %s", msg, h.Runtime().TokenOwner(), beat())
	if h.Runtime().TokenOwner() == id || beat() == after {
		t.Errorf("after the take-over the heartbeat does not go (owner %q)", h.Runtime().TokenOwner())
	}
	var reg map[string]any
	st, b, _ := s.Central.Do("GET", "/api/dataStore/facility-hub-registry/"+id, nil)
	json.Unmarshal(b, &reg)
	t.Logf("registry entry (%d): status %v, accountRequested %v, hubTokenOwner %v", st, reg["status"], reg["accountRequested"], reg["hubTokenOwner"])
	s.expect(nil, "on central as saved")
	if stuck {
		// Found 28 October: the hub's token was replaced only near its expiry, so a token
		// deleted on central left every housekeeping call at 401, though the hub's own
		// account still worked. Fixed 29 October: a 401 to the hub's token makes a new one
		// at once with the account; the hub page says when that fails.
		t.Errorf("the hub's housekeeping stays down after its token was deleted, though its account works")
	}
}

// approveAccount gives the scene's hub an account of its own, as the Facility Hub app
// does (hubaccount.Approve, as the superuser), and waits for the hub to take it up. The
// account, its role, group and move key are deleted when the test ends.
func approveAccount(t *testing.T, s *scene) tk.User {
	t.Helper()
	h := s.App.Hub
	id := h.HubID()
	group, role := tk.UID(), tk.UID()
	for _, p := range []struct {
		path string
		body map[string]any
	}{
		{"/api/userRoles", map[string]any{"id": role, "name": "Facility hub account " + role, "authorities": []string{}}},
		{"/api/userGroups", map[string]any{"id": group, "name": "Facility hub accounts " + group}},
	} {
		if st, b, err := s.Central.Do("POST", p.path, p.body); err != nil || st >= 300 {
			t.Fatalf("%s: %d %v %.300s", p.path, st, err, b)
		}
	}
	t.Cleanup(func() {
		var users struct{ Users []struct{ ID string } }
		s.Central.JSON("GET", "/api/users?fields=id&filter=username:ieq:"+id, nil, &users)
		for _, u := range users.Users {
			s.Central.Do("DELETE", "/api/users/"+u.ID, nil)
		}
		s.Central.Do("DELETE", "/api/dataStore/facility-hub-keys/"+id, nil)
		s.Central.Do("DELETE", "/api/userGroups/"+group, nil)
		s.Central.Do("DELETE", "/api/userRoles/"+role, nil)
	})
	done, err := hubaccount.Approve(context.Background(), hubaccount.Options{Central: s.M.CentralURL,
		Username: s.M.AdminUser, Password: s.M.AdminPassword, Group: group, Role: role},
		[]hubaccount.Request{{HubID: id, Code: h.AccountCode()}})
	if err != nil || len(done) != 1 {
		t.Fatalf("approve: %v %v", done, err)
	}
	s.Sync()
	if owner := h.Runtime().TokenOwner(); owner != id {
		t.Fatalf("the hub did not take up its account: %q", owner)
	}
	var users struct {
		Users []struct{ ID, Username string }
	}
	if err := s.Central.JSON("GET", "/api/users?fields=id,username&filter=username:ieq:"+id, nil, &users); err != nil || len(users.Users) != 1 {
		t.Fatalf("the hub's user on central: %v %v", users, err)
	}
	account := tk.User{UID: users.Users[0].ID, Username: users.Users[0].Username}
	return account
}

// G6: reset a hub that holds a change central refused (an edit of a person deleted on
// central) and a save not yet pushed, on a real central. Must hold, as designed
// (TestResetKeepsRefusedChanges): the outbox is pushed before the reset; a plain reset
// is refused while it would lose the refused change, with the reason; RESET AND
// DISCARD resets, and the registry records who retired the hub.
func TestG6ResetWithRefusedChanges(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	h := s.App.Hub
	id := h.HubID()
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	p := s.M.Primary()
	a := s.register(nurse, p, "Zqreseta")
	s.Sync()
	s.Offline()
	te := map[string]any{}
	for k, v := range a.Payload["trackedEntities"].([]any)[0].(map[string]any) {
		if k != "enrollments" {
			te[k] = v
		}
	}
	if st, b := s.save(nurse, "edit of a person about to be deleted on central", map[string]any{"trackedEntities": []any{te}}); st != 200 {
		t.Fatalf("offline edit: %d %s", st, b)
	}
	s.CentralDelete("trackedEntities", a.TE)
	web := s.Web(tk.RoleNurse)
	web.Login()
	s.Online()
	reset := func(confirm string) string {
		resp, err := web.PostForm("/hub/ui/reset", url.Values{"confirm": {confirm}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc, _ := url.QueryUnescape(resp.Header.Get("Location"))
		return loc
	}
	// A save queued just before: the reset pushes it first.
	s.Offline()
	b := s.offlineSave(nurse, "Zqresetb")
	s.Online()
	s.reconnect(1)
	// The person's enrollment went with the person, deleted on central by the test.
	s.expect(map[string]string{b.TE: tk.OnCentral, a.Enrollment: tk.Lost}, tk.OnCentral, tk.RefusedKept)
	t.Logf("refused: %d objects", h.Store.CountObjects("rejected"))
	var entry map[string]any
	s.Central.JSON("GET", "/api/dataStore/facility-hub-registry/"+id, nil, &entry)
	msg := reset("RESET")
	t.Logf("RESET: %s; mode %v, hub %s", msg, h.Mode(), h.HubID())
	if h.Mode() != "active" || h.HubID() != id {
		t.Fatalf("a plain reset discarded a refused change")
	}
	msg = reset("RESET AND DISCARD")
	t.Logf("RESET AND DISCARD: %s; mode %v", msg, h.Mode())
	if h.Mode() != "setup" {
		t.Fatalf("RESET AND DISCARD did not reset the hub")
	}
	var reg map[string]any
	s.Central.JSON("GET", "/api/dataStore/facility-hub-registry/"+id, nil, &reg)
	t.Logf("registry entry: status %v, retiredBy %v, retiredByUser %v", reg["status"], reg["retiredBy"], reg["retiredByUser"])
	if reg["status"] != "retired" || reg["retiredByUser"] != web.User {
		t.Errorf("the registry does not record the reset: %v", reg)
	}
	// What the Facility Hub app shows of a hub (facility name, set-up, version, its
	// tokens for "Revoke all tokens") must survive the reset (N23).
	var dropped []string
	for k := range entry {
		if _, ok := reg[k]; !ok {
			dropped = append(dropped, k)
		}
	}
	slices.Sort(dropped)
	t.Logf("registry fields before the reset: %d; dropped by it: %v", len(entry), dropped)
	if len(dropped) > 0 {
		finding(t, "N23: a reset rewrites the hub's registry entry without the fields the Facility Hub app shows")
		t.Errorf("the reset dropped registry fields the app reads: %v", dropped)
	}
}

// G7: a replacement hub is set up for the facility while the old one still runs,
// offline, with saves queued and staff still saving on it. Must hold: the old hub
// retires (serves nothing offline, pushes what it holds); nothing is pushed twice; the
// new hub holds every registration the old one confirmed.
func TestG7ReplacementWhileOldRuns(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	old := r.p.Status()["hubId"]
	r.outage(20 * time.Second)

	// The replacement, set up by the facility's administrator.
	e2 := tk.Start(t)
	e2.SetupOnly(tk.RoleAdmin)
	e2.Web(tk.RoleNurse).Login()
	e2.Sync()
	t.Logf("replacement %s set up; the old hub %s offline with %v objects queued", e2.App.Hub.HubID(), old, r.p.Status()["pending"])

	back := time.Now()
	r.e.Net.SetDown(false)
	s := r.p.WaitFor("retired", 3*time.Minute, func(s map[string]any) bool { return s["retired"] == true })
	t.Logf("the old hub retired %s after its uplink returned: %v", time.Since(back).Round(time.Second), s["message"])
	time.Sleep(15 * time.Second) // staff go on saving on the old hub, online
	stop()
	r.finish(5 * time.Minute)

	// The old hub offline answers nothing from its store.
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	c := r.e.Web(tk.RoleNurse)
	st, lb := login(c, r.e.M.Password)
	q := url.Values{"program": {r.prog.ID}, "orgUnits": {r.e.M.Facility.ID}, "fields": {"trackedEntity"}, "pageSize": {"5"}}
	rst, _, rb, _ := c.Try("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil)
	pr := r.e.M.NewPerson(r.prog, r.tag+"retired")
	sst, _, sb, _ := c.Try("POST", tk.SyncImport, pr.Payload)
	t.Logf("the retired hub offline: login %d %.80s; read %d %.120s; save %d %.120s", st, lb, rst, rb, sst, sb)
	if rst == 200 || accepted(sst, sb) {
		t.Errorf("the retired hub still serves offline: read %d, save %d", rst, sst)
	}
	r.e.Net.SetDown(false)

	// The new hub holds everything the old one confirmed.
	e2.Sync()
	e2.Sync()
	r2 := newRun(t, e2)
	e2.Offline()
	n := e2.Web(tk.RoleNurse)
	n.Login()
	held := r2.held(n)
	var missing []string
	confirmed := 0
	for _, x := range r.ledger.all() {
		if x.Confirmed && !x.Update {
			confirmed++
			if !held["person "+x.TE] {
				missing = append(missing, x.TE)
			}
		}
	}
	t.Logf("the new hub offline holds %d people; of %d registrations the old one confirmed, %d missing %v", len(held), confirmed,
		len(missing), first(missing))
	if len(missing) > 0 {
		t.Errorf("the new hub misses %d registrations the old hub confirmed", len(missing))
	}
}

// tsFields reads the scalar fields of a TypeScript object type in the Facility Hub
// app: name to "string", "number" or "boolean" (others are left out).
func tsFields(t *testing.T, file, typ string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "apps", "facility-hub", "src", filepath.FromSlash(file)))
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(b), "export type "+typ+" = {")
	if !ok {
		t.Fatalf("no type %s in %s", typ, file)
	}
	out := map[string]string{}
	depth := 0
	for _, ln := range strings.Split(body, "\n") {
		tr := strings.TrimSpace(ln)
		if depth == 0 {
			if m := tsField.FindStringSubmatch(tr); m != nil {
				out[m[1]] = m[2]
			}
		}
		depth += strings.Count(tr, "{") - strings.Count(tr, "}")
		if depth < 0 {
			break
		}
	}
	return out
}

var tsField = regexp.MustCompile(`^(\w+)\??: (string|number|boolean)(?: \| null)?$`)

// typeMismatches lists the fields of a dataStore entry whose JSON type is not the one
// the app declares.
func typeMismatches(entry map[string]any, want map[string]string) []string {
	var out []string
	for k, typ := range want {
		v, ok := entry[k]
		if !ok || v == nil {
			continue
		}
		got := "?"
		switch v.(type) {
		case string:
			got = "string"
		case float64:
			got = "number"
		case bool:
			got = "boolean"
		}
		if got != typ {
			out = append(out, fmt.Sprintf("%s: %s, the app reads %s", k, got, typ))
		}
	}
	slices.Sort(out)
	return out
}

// N6: a staged rollout: two hubs of a country on different releases at once, the
// facility's on the last release and the sibling's on this tree, both saving while
// offline and online. Must hold: both push to central, nothing lost or twice; both
// write the dataStore entries the Facility Hub app reads (heartbeat, registry) in the
// shapes its types declare.
func TestN6TwoReleasesAtOnce(t *testing.T) {
	ch := newChannel(t)
	old := ch.build(fromRelease(), "0.1.0")
	r := releaseStart(t, old)
	stop := r.write(300 * time.Millisecond)
	defer stop()

	// The sibling's hub, this tree, set up by an administrator there.
	sib := r.e.M.Sibling.ID
	e2 := tk.Start(t)
	admin, nurse := e2.NewUserLike(tk.RoleAdmin), e2.NewUserLike(tk.RoleNurse)
	e2.SetUserOrgUnits(admin, sib)
	e2.SetUserOrgUnits(nurse, sib)
	e2.SetupOnlyAs(admin.Username, e2.M.Password)
	dropEntries(t, e2.Central, e2.App.Hub.HubID())
	e2.Sync()
	if f := e2.App.Hub.Facility(); f != sib {
		t.Fatalf("the second hub serves %s, not the sibling %s", f, sib)
	}
	n := e2.AndroidUser(nurse.Username)
	if st, b := n.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("%s online: %d %s", nurse.Username, st, b)
	}
	atSibling := func(name string) string {
		pr := e2.M.NewPerson(r.prog, name)
		te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
		te["orgUnit"] = sib
		for _, en := range te["enrollments"].([]any) {
			en.(map[string]any)["orgUnit"] = sib
		}
		st, b := n.Do("POST", tk.SyncImport, pr.Payload)
		t.Logf("%s at the sibling (online %v): %d %.200s", name, !e2.Net.Down(), st, b)
		if st != 200 {
			t.Fatalf("a save on the sibling's hub failed")
		}
		return pr.TE
	}
	var sibs []string
	sibs = append(sibs, atSibling("Zqsibon"))
	r.outage(15 * time.Second)
	e2.Offline()
	sibs = append(sibs, atSibling("Zqsiboffa"), atSibling("Zqsiboffb"))
	r.e.Net.SetDown(false)
	e2.Online()
	e2.Sync()
	e2.Sync()
	time.Sleep(10 * time.Second)
	stop()
	r.finish(5 * time.Minute)
	for _, te := range sibs {
		if r.central("trackedEntities", te, "trackedEntity") == nil {
			t.Errorf("I1: %s saved on the sibling's hub is not on central", te)
		}
	}

	// What the Facility Hub app reads of both.
	beat := tsFields(t, "hubs/flags.ts", "Heartbeat")
	reg := tsFields(t, "hubs/registry.ts", "Registry")
	for _, id := range []string{r.p.Status()["hubId"].(string), e2.App.Hub.HubID()} {
		for _, x := range []struct {
			ns   string
			want map[string]string
		}{{"facility-hub-status", beat}, {"facility-hub-registry", reg}} {
			var entry map[string]any
			if err := r.e.Central.JSON("GET", "/api/dataStore/"+x.ns+"/"+id, nil, &entry); err != nil {
				t.Errorf("%s/%s: %v", x.ns, id, err)
				continue
			}
			var missing []string
			for k := range x.want {
				if _, ok := entry[k]; !ok {
					missing = append(missing, k)
				}
			}
			slices.Sort(missing)
			t.Logf("%s/%s (version %v): %d fields; the app's fields not sent: %v", x.ns, id, entry["version"], len(entry), missing)
			if m := typeMismatches(entry, x.want); len(m) > 0 {
				t.Errorf("%s/%s: %v", x.ns, id, m)
			}
		}
	}
}

// siblingHub sets a second hub up, in this process, for the facility's sibling, by a
// fresh administrator there, and returns it with a fresh nurse there (Android, known
// to the hub).
func siblingHub(t *testing.T, m *tk.Manifest) (*tk.Env, *tk.Client) {
	t.Helper()
	sib := m.Sibling.ID
	e2 := tk.Start(t)
	admin, nurse := e2.NewUserLike(tk.RoleAdmin), e2.NewUserLike(tk.RoleNurse)
	e2.SetUserOrgUnits(admin, sib)
	e2.SetUserOrgUnits(nurse, sib)
	e2.SetupOnlyAs(admin.Username, e2.M.Password)
	dropEntries(t, e2.Central, e2.App.Hub.HubID())
	e2.Sync()
	if f := e2.App.Hub.Facility(); f != sib {
		t.Fatalf("the second hub serves %s, not the sibling %s", f, sib)
	}
	n := e2.AndroidUser(nurse.Username)
	if st, b := n.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("%s online: %d %s", nurse.Username, st, b)
	}
	return e2, n
}

// G8: two hubs at sibling facilities, a person moving between them (TESTING.md phase
// 6), in the protected program: registered at the facility, transferred to the
// sibling through the facility's hub online; then both hubs offline, the sibling's
// nurse adds an event and the facility's nurse edits the old one; the hubs reconnect.
// Must hold: neither hub shows more than central would, online or offline; the
// sibling's event reaches central once; the facility's edit as central decides,
// refused and kept if refused; the owner at the end is the one central holds.
func TestG8TwoHubsPersonMoves(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	var p tk.Program
	for _, x := range s.M.Programs {
		if x.AccessLevel == "PROTECTED" {
			p = x
		}
	}
	if p.ID == "" {
		t.Skip("no protected program")
	}
	stage := ""
	for _, st := range p.Stages {
		if st != p.RestrictedStage && len(p.EventValues[st]) > 0 {
			stage = st
			break
		}
	}
	sib := s.M.Sibling.ID
	nurseA := s.connect(s.M.Users[tk.RoleNurse].Username)
	e2, nurseB := siblingHub(t, s.M)

	pr := s.M.NewPerson(p, "Zqtwohub", stage)
	nurseA.Complete(p, pr)
	if st, b := s.save(nurseA, "register at the facility", pr.Payload); st != 200 {
		t.Fatalf("registering: %d %s", st, b)
	}
	st, b := nurseA.Do("PUT", "/api/tracker/ownership/transfer?"+url.Values{"trackedEntity": {pr.TE}, "program": {p.ID},
		s.App.Hub.API().TransferOrgUnit(): {sib}}.Encode(), nil)
	t.Logf("transferred to the sibling through the facility's hub: %d %.200s; owner on central %s", st, b, s.CentralOwner(pr.TE, p.ID))
	s.Sync()
	e2.Sync()

	read := func(who string, c *tk.Client) int {
		st, _ := c.Do("GET", "/api/tracker/trackedEntities/"+pr.TE+"?program="+p.ID+"&fields=trackedEntity", nil)
		return st
	}
	t.Logf("online: the facility's nurse reads the person %d, the sibling's %d", read("A", nurseA), read("B", nurseB))
	s.Offline()
	e2.Offline()
	t.Logf("offline: the facility's hub answers %d, the sibling's %d", read("A", nurseA), read("B", nurseB))

	// Both offline: the sibling adds an event, the facility edits the old one.
	values := map[string]string{}
	for de, v := range p.EventValues[stage] {
		values[de] = v
	}
	// A follow-up in another stage (the first is not repeatable in every profile).
	next := stage
	for _, st := range p.Stages {
		if st != stage && st != p.RestrictedStage {
			next = st
			break
		}
	}
	newEv := map[string]any{"events": []any{map[string]any{"event": tk.UID(), "enrollment": pr.Enrollment, "program": p.ID,
		"programStage": next, "orgUnit": sib, "occurredAt": time.Now().UTC().Format("2006-01-02"), "status": "ACTIVE",
		"dataValues": []any{}}}}
	stB, bB := nurseB.Do("POST", tk.SyncImport, newEv)
	t.Logf("the sibling's nurse adds an event offline: %d %.300s", stB, bB)
	if stB == 200 && accepted(stB, bB) {
		s.l.Confirmed(nurseB.User, newEv)
	}
	edit := s.event(p, stage, pr.Enrollment, pr.Events[0], values)
	stA, bA := s.save(nurseA, "the facility's nurse edits the old event offline", edit)
	_ = stA
	_ = bA

	// The facility's hub reconnects first, then the sibling's.
	s.reconnect(2)
	e2.Online()
	e2.Sync()
	e2.Sync()
	t.Logf("owner on central at the end: %s (sibling %s)", s.CentralOwner(pr.TE, p.ID), sib)
	s.expect(nil, tk.OnCentral, tk.RefusedKept)

	// Neither hub shows more than central would, online and offline.
	for _, h := range []struct {
		name string
		e    *tk.Env
		c    *tk.Client
		ou   string
	}{{"the facility's hub", s.Env, nurseA, s.M.Facility.ID}, {"the sibling's hub", e2, nurseB, sib}} {
		path := "/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnits": {h.ou}, "fields": {"trackedEntity"},
			"paging": {"false"}}.Encode()
		for _, down := range []bool{false, true} {
			if down {
				h.e.Offline()
			} else {
				h.e.Online()
			}
			if l := s.leaks(h.c, path, "trackedEntities", "trackedEntity"); len(l) > 0 {
				t.Errorf("I3: %s (offline %v) lists %d people central does not show %s: %v", h.name, down, len(l), h.c.User, first(l))
			}
			hst, _ := h.c.Do("GET", "/api/tracker/trackedEntities/"+pr.TE+"?program="+p.ID+"&fields=trackedEntity", nil)
			cst, _, _ := s.Central.As(h.c.User, s.M.Password).Do("GET", "/api/tracker/trackedEntities/"+pr.TE+"?program="+p.ID+"&fields=trackedEntity", nil)
			t.Logf("%s (offline %v): the person %d, central %d", h.name, down, hst, cst)
			if hst == 200 && cst != 200 {
				t.Errorf("I3: %s (offline %v) serves the moved person where central answers %d", h.name, down, cst)
			}
		}
		h.e.Online()
	}
}

// G7, aggregate: a hub replaced by another for its facility must not take new saves
// offline, as it refuses tracker saves ("this hub was replaced"). Found by chance on
// 28 October, when another scenario replaced the hub of N16 mid-run.
func TestG7RetiredHubAggregate(t *testing.T) {
	s := begin(t)
	if !s.Live() || s.M.Aggregate == nil {
		t.Skip("needs a real DHIS2 with the aggregate data set")
	}
	web := s.Web(tk.RoleNurse)
	web.Login()
	e2 := tk.Start(t)
	e2.SetupOnly(tk.RoleAdmin)
	if err := s.App.Worker.Cycle(context.Background()); err != nil {
		t.Logf("sync: %v", err)
	}
	if !s.App.Hub.Retired() {
		t.Fatal("the old hub does not know it was replaced")
	}
	s.Offline()
	nurse := s.AndroidUser(s.M.Users[tk.RoleNurse].Username)
	pr := s.M.NewPerson(s.M.Primary(), "Zqretagg")
	nurse.Complete(s.M.Primary(), pr)
	tst, tb := nurse.Do("POST", tk.SyncImport, pr.Payload)
	ast, ab := s.aggValue(web, s.M.Aggregate.Number, month(0), "5")
	t.Logf("the replaced hub offline: a registration %d %.150s; an aggregate value %d %s", tst, tb, ast, ab)
	if ast/100 == 2 {
		finding(t, "G7: a replaced hub keeps taking aggregate values offline")
		t.Errorf("the replaced hub takes an aggregate value offline (%d), while it refuses tracker saves (%d)", ast, tst)
	}
	s.Online()
	s.Sync()
	s.DeleteCentralDataValue(s.M.Aggregate.DataSet, month(0), s.M.Aggregate.Number, s.M.Aggregate.COC)
}

// dropEntries deletes a hub's registry entry and heartbeat when the test ends, before
// the users it made: central keeps an entry's last writer, the hub's token's owner,
// and refuses to delete a user still named there.
func dropEntries(t *testing.T, c *tk.Central, hubID string) {
	t.Cleanup(func() {
		for _, ns := range []string{"facility-hub-registry", "facility-hub-status"} {
			c.Do("DELETE", "/api/dataStore/"+ns+"/"+hubID, nil)
		}
	})
}
