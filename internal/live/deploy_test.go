package live

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/hubaccount"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// dataStoreValue reads a dataStore entry on central (nil if missing).
func dataStoreValue(t *testing.T, e *tk.Env, ns, key string) map[string]any {
	t.Helper()
	if e.Fake != nil {
		return e.Fake.DataStoreValue(ns, key)
	}
	st, b, err := e.Central.Do("GET", "/api/dataStore/"+ns+"/"+key, nil)
	if err != nil || st == 404 {
		return nil
	}
	var v map[string]any
	json.Unmarshal(b, &v)
	return v
}

// putDataStore writes a dataStore entry on central, and live removes it afterwards.
func putDataStore(t *testing.T, e *tk.Env, ns, key, value string) {
	t.Helper()
	if e.Fake != nil {
		if e.Fake.DataStore[ns] == nil {
			e.Fake.DataStore[ns] = map[string]json.RawMessage{}
		}
		e.Fake.DataStore[ns][key] = json.RawMessage(value)
		return
	}
	path := "/api/dataStore/" + ns + "/" + key
	st, b, err := e.Central.Do("PUT", path, []byte(value))
	if err == nil && st == 404 {
		st, b, err = e.Central.Do("POST", path, []byte(value))
	}
	if err != nil || st >= 300 {
		t.Fatalf("writing %s: %d %v %.300s", path, st, err, b)
	}
	t.Cleanup(func() { e.Central.Do("DELETE", path, nil) })
}

// tokenValid reports whether central accepts a personal access token.
func tokenValid(t *testing.T, e *tk.Env, key string) bool {
	t.Helper()
	if e.Fake != nil {
		return e.Fake.TokenValid(key)
	}
	req, _ := http.NewRequest("GET", strings.TrimRight(e.M.CentralURL, "/")+"/api/me?fields=id", nil)
	req.Header.Set("Authorization", "ApiToken "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode == 200
}

// Anyone assigned to the facility sets the hub up and opens its admin page; there is
// no setup user group (docs/DEPLOYMENT.md). Someone assigned to another facility
// cannot open it.
func TestStaffSetUpAndAdminister(t *testing.T) {
	e := tk.Start(t)
	e.SetupAs(tk.RoleNurse)
	if e.App.Hub.Facility() != e.M.Facility.ID {
		t.Fatalf("facility %q, want %q", e.App.Hub.Facility(), e.M.Facility.ID)
	}
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	if st, b := nurse.Do("GET", "/hub/ui", nil); st != 200 {
		t.Errorf("the nurse cannot open the hub's admin page: %d %.300s", st, b)
	}
	outsider := e.Web(tk.RoleOutsider)
	outsider.Login()
	if st, _ := outsider.Do("GET", "/hub/ui", nil); st == 200 {
		t.Error("a user of another facility opened the hub's admin page")
	}
}

// A replacement is allowed at any time and recorded: the old hub's entry says which
// hub replaced it, who did it and when; the new one says which it replaced.
func TestReplacementRecorded(t *testing.T) {
	e := tk.Start(t)
	oldID := "OldHub" + tk.UID()[:5]
	putDataStore(t, e, "facility-hub-registry", oldID,
		`{"hubId":"`+oldID+`","facility":"`+e.M.Facility.ID+`","status":"active"}`)
	e.SetupAs(tk.RoleNurse)
	old := dataStoreValue(t, e, "facility-hub-registry", oldID)
	nurse := e.M.Users[tk.RoleNurse].Username
	if old["status"] != "retired" || old["retiredBy"] != e.App.Hub.HubID() || old["retiredByUser"] != nurse || old["retiredAt"] == nil {
		t.Errorf("old entry: %v", old)
	}
	cur := dataStoreValue(t, e, "facility-hub-registry", e.App.Hub.HubID())
	if !strings.Contains(fmt.Sprint(cur["replaces"]), oldID) {
		t.Errorf("new entry: %v", cur)
	}
}

// A staff member assigned to the facility takes the hub's housekeeping account over
// (its token for the heartbeat, issue reports and the like) when the person who set
// it up leaves: the hub swaps to a token of theirs and deletes the old one. The
// heartbeat says whose token it is.
func TestHubTokenTakeOver(t *testing.T) {
	e := tk.Start(t)
	e.Setup()
	old := e.App.Hub.Runtime().HubToken
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	take := func(password string) {
		t.Helper()
		resp, err := nurse.PostForm("/hub/ui/takeover", url.Values{"password": {password}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	take("wrong")
	if rt := e.App.Hub.Runtime(); rt.HubToken != old {
		t.Fatal("a wrong password took the hub over")
	}
	take(e.M.Password)
	rt := e.App.Hub.Runtime()
	name := e.M.Users[tk.RoleNurse].Username
	if rt.HubToken == old || rt.HubTokenOwner != name || !tokenValid(t, e, rt.HubToken) {
		t.Fatalf("not taken over: owner %q, new token valid %v", rt.HubTokenOwner, tokenValid(t, e, rt.HubToken))
	}
	if tokenValid(t, e, old) {
		t.Error("the old token still works")
	}
	e.Sync()
	hb := dataStoreValue(t, e, "facility-hub-status", e.App.Hub.HubID())
	if hb["hubTokenOwner"] != name || hb["hubTokenExpires"] == nil {
		t.Errorf("heartbeat: owner %v, expires %v", hb["hubTokenOwner"], hb["hubTokenExpires"])
	}
}

// Review fix: a take-over by someone whose account cannot write the hub's records on
// central (the facility-hub-* namespaces protected as advised) is refused before the
// old token goes, so the hub never ends up silent.
func TestTakeOverNeedsWriteAccess(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	admin := e.M.Users[tk.RoleAdmin].Username
	e.Fake.HubWriters = map[string]bool{admin: true}
	old := e.App.Hub.Runtime().HubToken
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	resp, err := nurse.PostForm("/hub/ui/takeover", url.Values{"password": {e.M.Password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rt := e.App.Hub.Runtime(); rt.HubToken != old || rt.TokenOwner() != admin || !e.Fake.TokenValid(old) {
		t.Fatalf("taken over without write access: owner %q, old token valid %v", rt.TokenOwner(), e.Fake.TokenValid(old))
	}
	nurseName := e.M.Users[tk.RoleNurse].Username
	if n := e.Fake.TokensOf(nurseName); n > 1 { // the nurse's own login token only
		t.Errorf("the refused take-over left %d tokens of the nurse", n)
	}
}

// The heartbeat keeps a daily history for 90 days next to the latest status: syncs,
// outages and offline minutes (split at midnight), the peak of pending changes and the
// lowest free disk, so central sees how a hub lives, not only how it is now.
func TestHeartbeatHistory(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	h := e.App.Hub
	today := time.Now().UTC().Truncate(24 * time.Hour)
	h.NoteOffline(today.Add(-30*time.Minute), today.Add(90*time.Minute)) // 23:30 to 01:30
	e.Offline()
	e.Online()
	e.Sync()
	var hb struct {
		Version string `json:"version"`
		History []struct {
			Date           string `json:"date"`
			Syncs          int    `json:"syncs"`
			Outages        int    `json:"outages"`
			OfflineMinutes int    `json:"offlineMinutes"`
			LongestOffline int    `json:"longestOfflineMinutes"`
			DiskFreeMin    uint64 `json:"diskFreeMinBytes"`
		} `json:"history"`
	}
	b, _ := json.Marshal(e.Fake.DataStoreValue("facility-hub-status", h.HubID()))
	json.Unmarshal(b, &hb)
	days := map[string]int{}
	for i, d := range hb.History {
		days[d.Date] = i
	}
	y, ok1 := days[today.Add(-time.Hour).Format(time.DateOnly)]
	d, ok2 := days[today.Format(time.DateOnly)]
	if hb.Version == "" || !ok1 || !ok2 {
		t.Fatalf("heartbeat: %s", b)
	}
	if hb.History[y].OfflineMinutes != 30 || hb.History[d].OfflineMinutes < 90 || hb.History[d].LongestOffline < 120 {
		t.Errorf("offline minutes not split at midnight: %+v", hb.History)
	}
	if hb.History[d].Outages < 2 || hb.History[d].Syncs < 1 || hb.History[d].DiskFreeMin == 0 {
		t.Errorf("today: %+v", hb.History[d])
	}
}

// Review gap: a staff member moved to another facility loses the hub's admin page
// at the next access refresh, though their hub session goes on.
func TestAdminPageLostWhenMovedAway(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	if st, _ := nurse.Do("GET", "/hub/ui", nil); st != 200 {
		t.Fatalf("admin page before the move: %d", st)
	}
	e.Fake.Users[e.M.Users[tk.RoleNurse].Username].OrgUnits = []string{fc.OtherFacility}
	e.Sync() // refreshes access
	if st, _ := nurse.Do("GET", "/hub/ui", nil); st != 403 {
		t.Errorf("admin page after moving away: %d, want 403", st)
	}
}

// Review gap: the admin page's actions refuse posts from another site, as a browser
// on a malicious page would send them with the staff member's cookie.
func TestAdminActionsRefuseCrossOrigin(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	old := e.App.Hub.Runtime().HubToken
	for _, path := range []string{"/hub/ui/reset", "/hub/ui/takeover", "/hub/ui/sync"} {
		req, _ := http.NewRequest("POST", e.Srv.URL+path, strings.NewReader("confirm=RESET+AND+DISCARD&password="+url.QueryEscape(e.M.Password)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://evil.example")
		res, err := nurse.HTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Errorf("%s from another origin: %d, want 403", path, res.StatusCode)
		}
	}
	if e.App.Hub.Mode() != "active" || e.App.Hub.Runtime().HubToken != old {
		t.Fatal("a cross-origin post reset the hub or took it over")
	}
}

// Review gap: a take-over while the hub token is being renewed ends with the token of
// the one who took over, and the heartbeat's owner is that token's owner (one lock
// serialises the two; without it the renewal overwrote the take-over).
func TestTakeOverDuringRenewal(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	h := e.App.Hub
	admin, nurse := e.M.Users[tk.RoleAdmin].Username, e.M.Users[tk.RoleNurse].Username
	if err := h.UpdateRuntime(func(rt *core.Runtime) {
		rt.HubTokenExp = time.Now().Add(time.Hour).UTC().Format(time.RFC3339) // due for renewal
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	e.Fake.OnTokenCreate = func(user string) {
		if user != admin {
			return
		}
		e.Fake.OnTokenCreate = nil
		go func() { done <- h.TakeOverHubToken(context.Background(), nurse, e.M.Password) }()
		time.Sleep(300 * time.Millisecond) // the take-over runs now, if nothing holds it back
	}
	if err := h.RenewHubToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rt := h.Runtime()
	if owner := e.Fake.TokenOwner(rt.HubToken); owner != nurse || rt.TokenOwner() != nurse {
		t.Fatalf("the hub uses a token of %q, says it is %q's", owner, rt.TokenOwner())
	}
}

// Review minor: take-over attempts are throttled like logins, so a session left open
// on a shared PC cannot be used to guess its user's central password.
func TestTakeOverThrottled(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	old := e.App.Hub.Runtime().HubToken
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	for i := 0; i < 6; i++ {
		resp, err := nurse.PostForm("/hub/ui/takeover", url.Values{"password": {"guess" + strconv.Itoa(i)}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	resp, err := nurse.PostForm("/hub/ui/takeover", url.Values{"password": {e.M.Password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if e.App.Hub.Runtime().HubToken != old {
		t.Fatal("taken over right after six wrong passwords")
	}
	e.Web(tk.RoleNurse).Login() // the take-over's throttle does not lock the user's login
}

// The hub's own account against central: an approval (as the superuser, what the
// Facility Hub app does) creates it scoped to the facility, and keeps the hub's move
// key for administrators only, even from a staff member who created its entry first;
// the hub takes the account up and replaces the password it was handed.
func TestHubAccountLive(t *testing.T) {
	e := tk.Start(t)
	e.Setup()
	h := e.App.Hub
	group, role := "HubGrpAccts", "HubRoleAcct"
	if e.Live() {
		role = tk.UID()
		group = tk.UID()
		for _, p := range []struct {
			path string
			body map[string]any
		}{
			{"/api/userRoles", map[string]any{"id": role, "name": "Facility hub account " + role, "authorities": []string{}}},
			{"/api/userGroups", map[string]any{"id": group, "name": "Facility hub accounts " + group}},
		} {
			if st, b, err := e.Central.Do("POST", p.path, p.body); err != nil || st >= 300 {
				t.Fatalf("%s: %d %v %.300s", p.path, st, err, b)
			}
		}
	}
	keyPath := "/api/dataStore/facility-hub-keys/" + h.HubID()
	nurse := tk.NewCentral(e.M.CentralURL, e.M.Users[tk.RoleNurse].Username, e.M.Password)
	if e.Fake != nil {
		nurse = tk.NewCentral(e.M.CentralURL, "nurse_a", fc.Password)
	}
	// A staff member creates the move key's entry first: approval must shut them out.
	squatted, _, _ := nurse.Do("POST", keyPath, map[string]string{"moveKey": "c3F1YXR0ZWQ="})
	done, err := hubaccount.Approve(context.Background(), hubaccount.Options{Central: e.M.CentralURL,
		Username: e.M.AdminUser, Password: e.M.AdminPassword, Group: group, Role: role},
		[]hubaccount.Request{{HubID: h.HubID(), Code: h.AccountCode()}})
	if err != nil || !slices.Contains(done, h.HubID()) {
		t.Fatalf("approve: %v %v", done, err)
	}
	// What the box carries, to check the hub replaces it.
	st, b, err := e.Central.Do("GET", "/api/dataStore/facility-hub-registry/"+h.HubID(), nil)
	var reg struct {
		Account struct {
			Box string `json:"box"`
		} `json:"account"`
	}
	if err != nil || st != 200 || json.Unmarshal(b, &reg) != nil {
		t.Fatalf("reading the registry entry: %d %v", st, err)
	}
	box, _ := base64.StdEncoding.DecodeString(reg.Account.Box)
	plain, err := hubaccount.Open(h.Runtime().BoxKey, box, h.HubID())
	var handed hubaccount.Credentials
	if err != nil || json.Unmarshal(plain, &handed) != nil {
		t.Fatalf("the box does not open: %v", err)
	}
	if err := h.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	rt := h.Runtime()
	if rt.TokenOwner() != h.HubID() {
		t.Fatalf("the hub did not take up its account: %q", rt.TokenOwner())
	}
	if h.Store.Meta(core.MetaAccountRotate) != "" || rt.HubPassword == handed.Password || !bytes.Equal(rt.MoveKey, handed.MoveKey) {
		t.Fatalf("the handed-over credentials were not replaced (%q)", h.Store.Meta(core.MetaAccountRotate))
	}
	if st, _, _ := tk.NewCentral(e.M.CentralURL, h.HubID(), handed.Password).Do("GET", "/api/me?fields=id", nil); st == 200 {
		t.Error("the handed-over password still works")
	}
	if st, _, _ := tk.NewCentral(e.M.CentralURL, h.HubID(), rt.HubPassword).Do("GET", "/api/me?fields=id", nil); st != 200 && e.Live() {
		t.Errorf("the hub's new password does not work: %d", st)
	}
	var ous []string
	if e.Fake != nil {
		ous = e.Fake.Users[h.HubID()].OrgUnits
	} else {
		st, b, err := e.Central.Do("GET", "/api/users?filter=username:eq:"+h.HubID()+"&fields=organisationUnits[id]", nil)
		if err != nil || st != 200 {
			t.Fatalf("reading the account: %d %v", st, err)
		}
		var us struct {
			Users []struct {
				OrganisationUnits []struct{ ID string } `json:"organisationUnits"`
			} `json:"users"`
		}
		json.Unmarshal(b, &us)
		for _, u := range us.Users {
			for _, o := range u.OrganisationUnits {
				ous = append(ous, o.ID)
			}
		}
	}
	if !slices.Equal(ous, []string{e.M.Facility.ID}) {
		t.Fatalf("the account is not scoped to the facility: %v", ous)
	}
	// The move key: administrators only, not the hub's own token, not staff.
	st, b, err = e.Central.Do("GET", keyPath, nil)
	var mk struct {
		MoveKey []byte `json:"moveKey"`
	}
	if err != nil || st != 200 || json.Unmarshal(b, &mk) != nil || !bytes.Equal(mk.MoveKey, rt.MoveKey) {
		t.Fatalf("the move key on central: %d %v", st, err)
	}
	req, _ := http.NewRequest("GET", strings.TrimRight(e.M.CentralURL, "/")+keyPath, nil)
	req.Header.Set("Authorization", "ApiToken "+rt.HubToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == 200 {
		t.Error("the hub's own token reads its move key")
	}
	if st, _, _ := nurse.Do("GET", keyPath, nil); st == 200 {
		t.Errorf("a staff member reads the hub's move key (they created its entry first: %d)", squatted)
	}
}
