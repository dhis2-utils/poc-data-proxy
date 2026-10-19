package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/hubaccount"
	"github.com/dhis2/facility-hub/internal/upgrade"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// appZip is the Facility Hub app as built (cd apps/facility-hub && pnpm build).
func appZip(t *testing.T) string {
	t.Helper()
	zips, _ := filepath.Glob("../../apps/facility-hub/build/bundle/facility-hub-*.zip")
	if len(zips) == 0 {
		t.Skip("build the Facility Hub app first: cd apps/facility-hub && pnpm install && pnpm build")
	}
	return zips[len(zips)-1]
}

// installApp installs the app on central, as the national team does in App Management.
func installApp(t *testing.T, e *tk.Env, zip string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("file", filepath.Base(zip))
	f, err := os.Open(zip)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(part, f)
	f.Close()
	w.Close()
	req, _ := http.NewRequest("POST", strings.TrimRight(e.M.CentralURL, "/")+"/api/apps", &body)
	req.SetBasicAuth(e.M.AdminUser, e.M.AdminPassword)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("installing the app: %d", resp.StatusCode)
	}
}

type appResult struct {
	Login     int      `json:"login"`
	Hubs      []string `json:"hubs"`
	Approved  bool     `json:"approved"`
	Dialog    string   `json:"dialog"`
	Code      string   `json:"code"`
	Exception string   `json:"exception"`
	Errors    []string `json:"errors"`
	Failed    []any    `json:"failed"`
}

// runApp drives the installed app with clients/facility_hub_app.py, as user.
func runApp(t *testing.T, e *tk.Env, user, password, action string, env ...string) appResult {
	t.Helper()
	out := t.TempDir()
	if p := os.Getenv("HUB_REPORT"); p != "" {
		out = filepath.Join(filepath.Dir(p), "facility-hub-app")
		os.MkdirAll(out, 0o755)
	}
	cmd := exec.Command("python3", "-I", "../../clients/facility_hub_app.py")
	cmd.Env = append(append(os.Environ(), "CENTRAL_URL="+e.M.CentralURL, "APP_USER="+user, "APP_PASSWORD="+password,
		"ACTION="+action, "OUT="+out), env...)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("facility_hub_app.py %s: %v\n%s", action, err, b)
	}
	var r appResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	// 409: the app creating an admin entry that exists, before taking it over.
	failed := slices.DeleteFunc(slices.Clone(r.Failed), func(f any) bool {
		m, _ := f.(map[string]any)
		return m["status"] == float64(409) && strings.HasPrefix(fmt.Sprint(m["url"]), "/api/dataStore/facility-hub-admin/")
	})
	if r.Login != 200 || r.Exception != "" || len(r.Errors) > 0 || len(failed) > 0 {
		t.Errorf("%s as %s: login %d, %s, page errors %v, failed requests %v (screenshot in %s)",
			action, user, r.Login, r.Exception, r.Errors, failed, out)
	}
	return r
}

// TestFacilityHubApp drives the Facility Hub app (apps/facility-hub) on central: a hub
// manager of the hub's district (not a superuser) approves a waiting hub with the code
// from the hub's page, and the hub takes its account up; the app's move code for an address is the hub's own; a district user
// sees the hub, a user of another district does not (docs/FACILITY-HUB-APP.md).
func TestFacilityHubApp(t *testing.T) {
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live instance to install the app on")
	}
	zip := appZip(t)
	e.Setup()
	h := e.App.Hub
	installApp(t, e, zip)

	// The hub accounts group and role, as the national team sets them once in the app.
	role, group := tk.UID(), tk.UID()
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
	// The hub managers, set up once by a superuser: a group, and a role with the
	// authorities to create users and add them to the (read-only) hub accounts group. The
	// manager is not a superuser, and is assigned to the hub's district only.
	mgrGroup, mgrRole, manager := tk.UID(), tk.UID(), "fhmgr"+strings.ToLower(tk.UID())
	for _, p := range []struct {
		path string
		body map[string]any
	}{
		{"/api/userGroups", map[string]any{"id": mgrGroup, "name": "Hub managers " + mgrGroup}},
		{"/api/userRoles", map[string]any{"id": mgrRole, "name": "Hub manager " + mgrRole, "authorities": []string{
			"F_USER_ADD", "F_USER_VIEW", "F_USER_GROUPS_READ_ONLY_ADD_MEMBERS", "M_facilityhub"}}},
		{"/api/users", map[string]any{"id": tk.UID(), "username": manager, "password": e.M.Password,
			"firstName": "Hub", "surname": "manager", "organisationUnits": []map[string]string{{"id": e.M.District.ID}},
			"userRoles": []map[string]string{{"id": mgrRole}}, "userGroups": []map[string]string{{"id": mgrGroup}}}},
		{"/api/sharing?type=userGroup&id=" + group, map[string]any{"object": map[string]any{"publicAccess": "--------",
			"userGroupAccesses": []map[string]string{{"id": mgrGroup, "access": "r-------"}}}}},
	} {
		if st, b, err := e.Central.Do("POST", p.path, p.body); err != nil || st >= 300 {
			t.Fatalf("%s: %d %v %.300s", p.path, st, err, b)
		}
	}
	// The settings, as the app writes them: created, shared as public read only, then written.
	e.Central.Do("DELETE", "/api/dataStore/facility-hub-admin/accounts", nil)
	e.Central.Do("POST", "/api/dataStore/facility-hub-admin/accounts", map[string]string{})
	var meta struct {
		ID string `json:"id"`
	}
	e.Central.JSON("GET", "/api/dataStore/facility-hub-admin/accounts/metaData", nil, &meta)
	e.Central.Do("POST", "/api/sharing?type=dataStore&id="+meta.ID, map[string]any{"object": map[string]any{
		"publicAccess": "r-------", "userAccesses": []any{}, "userGroupAccesses": []any{}}})
	if st, b, err := e.Central.Do("PUT", "/api/dataStore/facility-hub-admin/accounts",
		map[string]string{"group": group, "role": role, "managers": mgrGroup}); err != nil || st >= 300 {
		t.Fatalf("accounts settings: %d %v %.300s", st, err, b)
	}

	r := runApp(t, e, manager, e.M.Password, "approve", "HUB_ID="+h.HubID(), "CODE="+h.AccountCode())
	if !r.Approved {
		t.Fatalf("not approved in the app: %s", r.Dialog)
	}
	if err := h.RefreshCatalog(t.Context()); err != nil {
		t.Fatal(err)
	}
	rt := h.Runtime()
	if rt.TokenOwner() != h.HubID() || rt.MoveKey == nil {
		t.Fatalf("the hub did not take up the account the app handed over: owner %q", rt.TokenOwner())
	}
	// The move key the app kept is shared with the hub managers only.
	var keyMeta struct {
		Sharing struct {
			Public     string         `json:"public"`
			Users      map[string]any `json:"users"`
			UserGroups map[string]any `json:"userGroups"`
		} `json:"sharing"`
	}
	if err := e.Central.JSON("GET", "/api/dataStore/facility-hub-keys/"+h.HubID()+"/metaData", nil, &keyMeta); err != nil {
		t.Fatal(err)
	}
	if _, ok := keyMeta.Sharing.UserGroups[mgrGroup]; keyMeta.Sharing.Public != "--------" ||
		len(keyMeta.Sharing.Users) > 0 || len(keyMeta.Sharing.UserGroups) != 1 || !ok {
		t.Errorf("the move key is shared: %+v", keyMeta.Sharing)
	}

	const address = "https://new.example.org/dhis"
	r = runApp(t, e, manager, e.M.Password, "movecode", "HUB_ID="+h.HubID(), "ADDRESS="+address)
	if want := hubaccount.MoveCode(rt.MoveKey, address); r.Code != want {
		t.Errorf("the app's move code %q, the hub's %q", r.Code, want)
	}

	// A planned move, written by the app: readable by all, writable by administrators
	// only (an earlier entry by someone else is replaced), withdrawn by emptying it.
	nurse := e.M.Users[tk.RoleNurse]
	e.Central.Do("DELETE", "/api/dataStore/facility-hub-admin/central-move", nil)
	tk.NewCentral(e.M.CentralURL, nurse.Username, e.M.Password).Do("POST",
		"/api/dataStore/facility-hub-admin/central-move", map[string]string{"url": "https://evil.example.org"})
	runApp(t, e, manager, e.M.Password, "announce", "ADDRESS="+address)
	var move struct {
		URL string `json:"url"`
	}
	e.Central.JSON("GET", "/api/dataStore/facility-hub-admin/central-move", nil, &move)
	if move.URL != address {
		t.Errorf("planned move %q", move.URL)
	}
	if st, _, _ := tk.NewCentral(e.M.CentralURL, nurse.Username, e.M.Password).Do("PUT",
		"/api/dataStore/facility-hub-admin/central-move", map[string]string{"url": "https://evil.example.org"}); st < 300 {
		t.Errorf("a staff member who created the entry first still writes the planned move (%d)", st)
	}
	runApp(t, e, manager, e.M.Password, "withdraw")
	var withdrawn map[string]any
	if err := e.Central.JSON("GET", "/api/dataStore/facility-hub-admin/central-move", nil, &withdrawn); err != nil || len(withdrawn) != 0 {
		t.Errorf("planned move not withdrawn: %v %v", withdrawn, err)
	}

	// The hub version, chosen and staged by the app: readable by all (hubs read it),
	// writable by the hub managers.
	e.Central.Do("DELETE", "/api/dataStore/facility-hub-admin/release", nil)
	runApp(t, e, manager, e.M.Password, "stage", "HUB_ID="+h.HubID(), "VERSION=9.9.9")
	var choice upgrade.Choice
	if err := e.Central.JSON("GET", "/api/dataStore/facility-hub-admin/release", nil, &choice); err != nil ||
		choice.Version != "9.9.9" || len(choice.PilotHubs) != 1 || choice.PilotHubs[0] != h.HubID() || choice.Percent != 0 {
		t.Errorf("release choice %+v %v", choice, err)
	}
	var relMeta struct {
		Sharing struct {
			Public     string         `json:"public"`
			UserGroups map[string]any `json:"userGroups"`
		} `json:"sharing"`
	}
	e.Central.JSON("GET", "/api/dataStore/facility-hub-admin/release/metaData", nil, &relMeta)
	if _, ok := relMeta.Sharing.UserGroups[mgrGroup]; relMeta.Sharing.Public != "r-------" || !ok {
		t.Errorf("release choice sharing %+v", relMeta.Sharing)
	}

	// Users who may open the app: one in the hub's district, one in another.
	appRole := tk.UID()
	if st, b, err := e.Central.Do("POST", "/api/userRoles", map[string]any{"id": appRole,
		"name": "Facility Hub app " + appRole, "authorities": []string{"M_facilityhub"}}); err != nil || st >= 300 {
		t.Fatalf("app role: %d %v %.300s", st, err, b)
	}
	var district struct {
		Level int `json:"level"`
	}
	e.Central.JSON("GET", "/api/organisationUnits/"+e.M.District.ID+"?fields=level", nil, &district)
	var others struct {
		OrganisationUnits []struct{ ID string } `json:"organisationUnits"`
	}
	e.Central.JSON("GET", fmt.Sprintf("/api/organisationUnits?filter=level:eq:%d&filter=id:ne:%s&fields=id&pageSize=1",
		district.Level, e.M.District.ID), nil, &others)
	if len(others.OrganisationUnits) == 0 {
		t.Fatal("no other district")
	}
	for _, c := range []struct {
		ou   string
		sees bool
	}{{e.M.District.ID, true}, {others.OrganisationUnits[0].ID, false}} {
		name := "fhapp" + strings.ToLower(tk.UID())
		if st, b, err := e.Central.Do("POST", "/api/users", map[string]any{"id": tk.UID(), "username": name,
			"password": e.M.Password, "firstName": "Facility Hub", "surname": "app test",
			"organisationUnits": []map[string]string{{"id": c.ou}}, "userRoles": []map[string]string{{"id": appRole}}}); err != nil || st >= 300 {
			t.Fatalf("user: %d %v %.300s", st, err, b)
		}
		r := runApp(t, e, name, e.M.Password, "list")
		if slices.Contains(r.Hubs, h.HubID()) != c.sees {
			t.Errorf("a user of %s sees the hub: %v, want %v (sees %v)", c.ou, !c.sees, c.sees, r.Hubs)
		}
	}
}
