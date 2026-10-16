package live

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Anyone assigned to the facility sets the hub up and opens its admin page; there is
// no setup user group (docs/DEPLOYMENT.md). Someone assigned to another facility
// cannot open it.
func TestStaffSetUpAndAdminister(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
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
	e.NeedFake()
	e.Fake.DataStore["facility-hub-registry"] = map[string]json.RawMessage{
		"OldHub00001": json.RawMessage(`{"hubId":"OldHub00001","facility":"` + e.M.Facility.ID + `","status":"active"}`)}
	e.SetupAs(tk.RoleNurse)
	old := e.Fake.DataStoreValue("facility-hub-registry", "OldHub00001")
	nurse := e.M.Users[tk.RoleNurse].Username
	if old["status"] != "retired" || old["retiredBy"] != e.App.Hub.HubID() || old["retiredByUser"] != nurse || old["retiredAt"] == nil {
		t.Errorf("old entry: %v", old)
	}
	if cur := e.Fake.DataStoreValue("facility-hub-registry", e.App.Hub.HubID()); fmt.Sprint(cur["replaces"]) != "[OldHub00001]" {
		t.Errorf("new entry: %v", cur)
	}
}

// A staff member assigned to the facility takes the hub's housekeeping account over
// (its token for the heartbeat, issue reports and the like) when the person who set
// it up leaves: the hub swaps to a token of theirs and deletes the old one. The
// heartbeat says whose token it is.
func TestHubTokenTakeOver(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
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
	if rt.HubToken == old || rt.HubTokenOwner != name || !e.Fake.TokenValid(rt.HubToken) {
		t.Fatalf("not taken over: owner %q, new token valid %v", rt.HubTokenOwner, e.Fake.TokenValid(rt.HubToken))
	}
	if e.Fake.TokenValid(old) {
		t.Error("the old token still works")
	}
	e.Sync()
	hb := e.Fake.DataStoreValue("facility-hub-status", e.App.Hub.HubID())
	if hb["hubTokenOwner"] != name || hb["hubTokenExpires"] == nil {
		t.Errorf("heartbeat: owner %v, expires %v", hb["hubTokenOwner"], hb["hubTokenExpires"])
	}
}
