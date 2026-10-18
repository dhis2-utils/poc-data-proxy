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
	if r.Exception != "" || len(r.Errors) > 0 {
		t.Errorf("%s as %s: %s %v (screenshot in %s)", action, user, r.Exception, r.Errors, out)
	}
	return r
}

// TestFacilityHubApp drives the Facility Hub app (apps/facility-hub) on central: the
// superuser approves a waiting hub with the code from the hub's page, and the hub takes
// its account up; the app's move code for an address is the hub's own; a district user
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
	accounts := map[string]string{"group": group, "role": role}
	if st, _, _ := e.Central.Do("PUT", "/api/dataStore/facility-hub-admin/accounts", accounts); st == 404 {
		e.Central.Do("POST", "/api/dataStore/facility-hub-admin/accounts", accounts)
	}

	r := runApp(t, e, e.M.AdminUser, e.M.AdminPassword, "approve", "HUB_ID="+h.HubID(), "CODE="+h.AccountCode())
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

	const address = "https://new.example.org/dhis"
	r = runApp(t, e, e.M.AdminUser, e.M.AdminPassword, "movecode", "HUB_ID="+h.HubID(), "ADDRESS="+address)
	if want := hubaccount.MoveCode(rt.MoveKey, address); r.Code != want {
		t.Errorf("the app's move code %q, the hub's %q", r.Code, want)
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
