package live

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestCaptureWeb drives the real Capture web app through the hub with Playwright
// (clients/capture_web.py): online to warm the hub, then offline. It fails if Capture
// does not render the facility's working list offline, and reports every request the
// hub could not answer offline. Set HUB_CLIENTS=1 to run it (it needs a browser).
func TestCaptureWeb(t *testing.T) {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real Capture web app")
	}
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST)")
	}
	m := e.M
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
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
		out = filepath.Join(filepath.Dir(p), "capture-web")
		os.MkdirAll(out, 0o755)
	}
	var progs []string
	for _, p := range m.Programs {
		progs = append(progs, p.ID)
	}
	cmd := exec.Command("python3", "-I", "../../clients/capture_web.py")
	cmd.Env = append(os.Environ(), "HUB_URL="+e.Srv.URL, "CONTROL_URL="+control.URL, "HUB_USER="+nurse.User,
		"HUB_PASSWORD="+m.Password, "ORG_UNIT="+m.Facility.ID, "PROGRAMS="+strings.Join(progs, ","),
		"ENROLLMENT="+enrollmentAt(t, e, m.Primary(), m.Facility.ID), "OUT="+out)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("capture_web.py: %v\n%s", err, b)
	}
	os.WriteFile(filepath.Join(out, "result.json"), b, 0o644)
	var res struct {
		Login   int            `json:"login"`
		Online  map[string]any `json:"online"`
		Offline map[string]any `json:"offline"`
		Failed  map[string][]struct {
			Status int
			Path   string
		} `json:"failed"`
		Errors map[string][]string `json:"errors"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if res.Login != 200 {
		t.Fatalf("login through the hub: %d", res.Login)
	}
	for _, p := range progs {
		on, _ := res.Online["rows:"+p].(float64)
		off, _ := res.Offline["rows:"+p].(float64)
		t.Logf("program %s: %v rows online, %v offline", p, on, off)
		if on > 0 && off == 0 {
			t.Errorf("program %s: Capture shows the working list online but not offline", p)
		}
	}
	gaps := map[string]int{}
	for _, f := range res.Failed["offline"] {
		gaps[f.Path]++
	}
	var paths []string
	for p := range gaps {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		t.Logf("offline, the hub could not answer: %s (x%d)", p, gaps[p])
	}
	for _, e := range res.Errors["offline"] {
		t.Errorf("Capture page error offline: %s", e)
	}
	t.Logf("screenshots and result in %s", out)
}
