package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

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
