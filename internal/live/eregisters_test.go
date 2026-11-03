package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestERegisters drives the eRegisters app (github.com/HISP-Uganda/eregisters)
// through the hub with Playwright (clients/eregisters.py), one device at a time: the
// second nurse pulls online; the first pulls online, then with the uplink cut
// registers a client and pushes to the hub; the second opens the app offline, pulls
// from the hub and finds the client. After reconnection the client is on central,
// created by the first nurse. It needs an instance set up by
// clients/eregisters_setup.py (its manifest in HUB_MANIFEST) and HUB_CLIENTS=1.
func TestERegisters(t *testing.T) {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real eRegisters app")
	}
	e := tk.Start(t)
	m := e.M
	if !e.Live() || m.Profile != "eregisters" {
		t.Skip("needs an eRegisters instance (clients/eregisters_setup.py)")
	}
	e.Setup()
	nurse, nurse2 := e.Web(tk.RoleNurse), e.Web(tk.RoleNurse2)
	nurse.Login() // both have logged in through the hub online, as staff must
	nurse2.Login()
	e.Sync()

	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch r.URL.Path {
		case "/offline":
			err = e.SetUplink(false)
		case "/online":
			err = e.SetUplink(true)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
	defer control.Close()

	out := t.TempDir()
	if p := os.Getenv("HUB_REPORT"); p != "" {
		out = filepath.Join(filepath.Dir(p), "eregisters")
		os.MkdirAll(out, 0o755)
	}
	surname := "Zq" + strings.ToLower(tk.UID()[:7])
	cmd := exec.Command("python3", "-I", "../../clients/eregisters.py")
	cmd.Env = append(os.Environ(), "HUB_URL="+e.Srv.URL, "CONTROL_URL="+control.URL, "USER_A="+nurse.User,
		"USER_B="+nurse2.User, "HUB_PASSWORD="+m.Password, "SURNAME="+surname, "OUT="+out)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("eregisters.py: %v\n%s", err, b)
	}
	os.WriteFile(filepath.Join(out, "result.json"), b, 0o644)
	var res struct {
		Steps   map[string]any `json:"steps"`
		Tracker []struct {
			User, Phase, Method, Path string
			Status, Count             int
			HasClient                 bool
		} `json:"tracker"`
		Failed []struct {
			User, Phase, Path string
			Status            int
		} `json:"failed"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	for _, step := range []string{"pull online B", "pull online A", "push offline A", "pull offline B"} {
		if st, _ := res.Steps[step].(float64); st != 200 {
			t.Errorf("%s: %v, want 200", step, res.Steps[step])
		}
	}
	servedOffline := false
	for _, r := range res.Tracker {
		t.Logf("%s %s: %s %d, %d people, the client among them: %v (%.100s)", r.User, r.Phase, r.Method, r.Status,
			r.Count, r.HasClient, r.Path)
		servedOffline = servedOffline || (r.User == nurse2.User && r.Phase == "offline" && r.HasClient)
	}
	for _, f := range res.Failed {
		if f.Phase == "offline" {
			t.Logf("offline, the hub could not answer %s: %d %s", f.User, f.Status, f.Path)
		}
	}
	for _, x := range res.Errors {
		t.Logf("page error: %s", x)
	}

	e.Sync()
	p := m.Primary()
	q := url.Values{"program": {p.ID}, "orgUnits": {m.Facility.ID}, "filter": {p.SearchAttr + ":eq:" + surname},
		"fields": {"trackedEntity,createdBy"}}
	var found struct {
		TrackedEntities []map[string]any `json:"trackedEntities"`
	}
	// As the nurse: central refuses a tracker search to a user without org units (E1006).
	if err := e.Central.As(nurse.User, m.Password).JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &found); err != nil {
		t.Fatal(err)
	}
	if len(found.TrackedEntities) != 1 {
		t.Fatalf("on central after reconnection: %d clients named %s, want 1", len(found.TrackedEntities), surname)
	}
	if got := username(found.TrackedEntities[0], "createdBy"); got != nurse.User {
		t.Errorf("on central, created by %q, want %q", got, nurse.User)
	}
	t.Logf("screenshots and result in %s", out)

	if found, _ := res.Steps["B finds the client offline"].(bool); !found {
		// eRegisters drops a pulled record whose author lacks first name or surname
		// (FINDINGS.md, "Found by adversarial testing" 9).
		t.Errorf("the second nurse does not find the client offline (the hub served it: %v)", servedOffline)
	}
}

// TestERegistersUpdate (adversarial N24): eRegisters updated on central while the hub
// runs. Two devices use the build central serves (HUB_EREGISTERS_OLD) through the hub
// online; with the hub offline, the build in HUB_EREGISTERS_ZIP is installed on
// central; the first device registers a client offline and pushes it to the hub; back
// online the second device loads the app (the new build, once its service worker
// updates) and finds the client; offline again it registers a second client, which
// the first device pulls from the hub. Must hold, as N19: each device works offline
// on whichever build it has; every pull and push is answered; both clients reach
// central once, created by their authors. The old build is put back at the end.
func TestERegistersUpdate(t *testing.T) {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real eRegisters app")
	}
	newZip, oldZip := os.Getenv("HUB_EREGISTERS_ZIP"), os.Getenv("HUB_EREGISTERS_OLD")
	if newZip == "" || oldZip == "" {
		t.Skip("HUB_EREGISTERS_ZIP (the update) and HUB_EREGISTERS_OLD (the build central serves) run it")
	}
	e := tk.Start(t)
	m := e.M
	if !e.Live() || m.Profile != "eregisters" {
		t.Skip("needs an eRegisters instance (clients/eregisters_setup.py)")
	}
	t.Cleanup(func() {
		if err := uploadApp(e, oldZip); err != nil {
			t.Errorf("putting the old eRegisters back on central: %v", err)
		}
	})
	e.Setup()
	nurse, nurse2 := e.Web(tk.RoleNurse), e.Web(tk.RoleNurse2)
	nurse.Login()
	nurse2.Login()
	e.Sync()

	steps := map[string]func() error{
		"offline": func() error { return e.SetUplink(false) },
		"online":  func() error { return e.SetUplink(true) },
		"install": func() error { return uploadApp(e, newZip) },
		"sync":    func() error { return e.App.Worker.Cycle(t.Context()) },
	}
	var mu sync.Mutex
	var done []string
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		name := strings.TrimPrefix(r.URL.Path, "/")
		f := steps[name]
		if f == nil {
			http.NotFound(w, r)
			return
		}
		start := time.Now()
		err := f()
		done = append(done, fmt.Sprintf("%s %s (%v)", name, time.Since(start).Round(time.Millisecond), err))
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
	defer control.Close()

	out := t.TempDir()
	if p := os.Getenv("HUB_REPORT"); p != "" {
		out = filepath.Join(filepath.Dir(p), "eregisters-update")
		os.MkdirAll(out, 0o755)
	}
	surname, surname2 := "Zq"+strings.ToLower(tk.UID()[:7]), "Zq"+strings.ToLower(tk.UID()[:7])
	cmd := exec.Command("python3", "-I", "../../clients/eregisters.py")
	cmd.Env = append(os.Environ(), "SCENARIO=update", "HUB_URL="+e.Srv.URL, "CONTROL_URL="+control.URL, "USER_A="+nurse.User,
		"USER_B="+nurse2.User, "HUB_PASSWORD="+m.Password, "SURNAME="+surname, "SURNAME2="+surname2, "OUT="+out,
		"ZIP_OLD="+oldZip, "ZIP_NEW="+newZip)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("eregisters.py: %v\n%s", err, b)
	}
	os.WriteFile(filepath.Join(out, "result.json"), b, 0o644)
	var res struct {
		Steps   map[string]any    `json:"steps"`
		Builds  map[string]string `json:"builds"`
		Error   string            `json:"error"`
		Tracker []struct {
			User, Phase, Method, Path string
			Status, Count             int
			HasClient                 bool
		} `json:"tracker"`
		Failed []struct {
			User, Phase, Path string
			Status            int
		} `json:"failed"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	t.Logf("builds %v; control steps %v", res.Builds, done)
	order := []string{"B online, pull", "A online, pull", "A offline after the install, registers", "A offline, push",
		"B online after the install, pull", "B online finds A's client", "B offline, registers", "B offline, push",
		"A offline, pull", "A offline finds B's client", "A online at the end, pull", "A online finds both"}
	for _, s := range order {
		t.Logf("%-42s %v (build %v)", s, res.Steps[s], res.Steps[s+": build"])
		switch v := res.Steps[s].(type) {
		case float64:
			if v != 200 {
				t.Errorf("%s: %v, want 200", s, v)
			}
		case bool:
			if !v {
				t.Errorf("%s: no", s)
			}
		case string:
			if v != "done" {
				t.Errorf("%s: %s", s, v)
			}
		default:
			t.Errorf("%s: not run (%v)", s, v)
		}
	}
	if res.Error != "" {
		t.Errorf("the driver stopped: %s", res.Error)
	}
	for _, r := range res.Tracker {
		t.Logf("%s %s: %s %d, %d people (%.100s)", r.User, r.Phase, r.Method, r.Status, r.Count, r.Path)
	}
	for _, f := range res.Failed {
		t.Logf("%s, the hub answered %s %d for %s", f.Phase, f.User, f.Status, f.Path)
	}
	for _, x := range res.Errors {
		t.Logf("page error: %s", x)
	}

	e.Sync()
	p := m.Primary()
	for _, c := range []struct{ surname, author string }{{surname, nurse.User}, {surname2, nurse2.User}} {
		q := url.Values{"program": {p.ID}, "orgUnits": {m.Facility.ID}, "filter": {p.SearchAttr + ":eq:" + c.surname},
			"fields": {"trackedEntity,createdBy"}}
		var found struct {
			TrackedEntities []map[string]any `json:"trackedEntities"`
		}
		if err := e.Central.As(c.author, m.Password).JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &found); err != nil {
			t.Fatal(err)
		}
		t.Logf("on central: %d clients named %s", len(found.TrackedEntities), c.surname)
		if len(found.TrackedEntities) != 1 {
			t.Errorf("on central: %d clients named %s, want 1", len(found.TrackedEntities), c.surname)
		} else if got := username(found.TrackedEntities[0], "createdBy"); got != c.author {
			t.Errorf("%s on central created by %q, want %q", c.surname, got, c.author)
		}
	}
	t.Logf("screenshots and result in %s", out)
}
