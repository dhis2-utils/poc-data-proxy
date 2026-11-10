package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// A15, measured (docs/superpowers/plans/2026-10-30-open-findings.md, "Measure first"):
// cached metadata replayed offline after central changed it. First find out whether
// Capture web and Android reload metadata offline at all, as a scenario with the real
// apps; fix only if a client is shown a stale form.
//
// The scenario: the tablet and a browser load the primary program online through the
// hub; central renames the program; the hub's uplink is cut (the tablet still reaches
// the hub, the browser too); offline, both apps reload / sync. What is measured: which
// metadata requests each app makes offline and what the hub answers (its per-user
// cache), and which program name each app shows — the old one from before the change,
// or nothing.
//
// Set HUB_CLIENTS=1 (browser and adb device) and HUB_MANIFEST; SANDBOX_HOST_PORT_2
// gives the tablet's port.
func TestA15MetadataReloadOffline(t *testing.T) {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real apps")
	}
	tb := startTablet(t, "A15") // skips without a device or a live central
	e := tb.Env
	m := e.M
	p := m.Primary()
	out := t.TempDir()
	if r := os.Getenv("HUB_REPORT"); r != "" {
		out = filepath.Join(filepath.Dir(r), "a15-metadata")
		os.MkdirAll(out, 0o755)
	}

	// Online: the tablet logs in at the hub and syncs all metadata.
	tb.login(tb.hub)
	if err := tb.a.SyncAll(); err != nil {
		t.Fatalf("the tablet's online sync: %v", err)
	}
	time.Sleep(30 * time.Second) // let the metadata sync settle
	e.Requests()                 // forget the online requests

	// The control the web script calls between phases: rename the program on central,
	// then cut the hub's uplink.
	newName := p.Name + "-RenamedA15"
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch r.URL.Path {
		case "/midpoint":
			e.SetField("/api/programs/"+p.ID, "name", newName)
			err = e.SetUplink(false)
		case "/online":
			err = e.SetUplink(true)
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
	defer control.Close()

	// Online: the web app loads the program and a registration form through the hub.
	web := func() map[string]any {
		t.Helper()
		cmd := exec.Command("python3", "-I", "../../clients/a15_metadata.py")
		cmd.Env = append(os.Environ(), "HUB_URL="+e.Srv.URL, "CONTROL_URL="+control.URL,
			"HUB_USER="+m.Users[tk.RoleNurse].Username, "HUB_PASSWORD="+m.Password,
			"ORG_UNIT="+m.Facility.ID, "PROGRAM="+p.ID, "OLD_NAME="+p.Name, "NEW_NAME="+newName, "OUT="+out)
		cmd.Stderr = os.Stderr
		b, err := cmd.Output()
		if err != nil {
			t.Fatalf("a15_metadata.py: %v\n%s", err, b)
		}
		var res map[string]any
		if err := json.Unmarshal(b, &res); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		return res
	}
	// One run covers online then (after /midpoint) offline, so run it once.
	res := web()
	webReqs, _ := res["reqs"].(map[string]any)

	// Offline: the tablet still reaches the hub (centralDown cuts only central's
	// route); sync again and open the program.
	tb.centralDown.Store(true)
	tb.online()
	e.Requests() // forget everything before the offline phase
	if err := tb.a.SyncAll(); err != nil {
		t.Logf("the tablet's offline sync: %v", err)
	}
	time.Sleep(20 * time.Second)
	nodes, _ := tb.a.Screen()
	shown := tk.Texts(nodes)
	if err := tb.a.Screenshot(filepath.Join(out, "a15-tablet-offline.png")); err != nil {
		t.Logf("screenshot: %v", err)
	}
	// Open the program on the tablet to make it load its metadata.
	if _, err := tb.a.ScrollTo(p.Name, 10); err != nil {
		t.Logf("scroll to %s: %v", p.Name, err)
	}
	time.Sleep(5 * time.Second)
	if nodes, err := tb.a.Screen(); err == nil {
		shown += " || " + tk.Texts(nodes)
	}

	offlineReqs := e.Requests()
	metaReqs := map[string][]tk.Request{}
	for _, r := range offlineReqs {
		if !r.Online && isMetadataPath(r.Path) {
			metaReqs[r.Path] = append(metaReqs[r.Path], r)
		}
	}
	var metaPaths []string
	for p := range metaReqs {
		metaPaths = append(metaPaths, p)
	}
	t.Logf("offline metadata requests from the tablet (%d paths): %v", len(metaPaths), metaPaths)
	for p, rs := range metaReqs {
		for _, r := range rs {
			t.Logf("  %s %s -> %d", r.Method, p, r.Status)
		}
	}
	t.Logf("tablet offline shows old name %q: %v; new name %q: %v", p.Name, strings.Contains(shown, p.Name),
		newName, strings.Contains(shown, newName))
	t.Logf("web online: %v", res["online"])
	t.Logf("web offline: %v", res["offline"])
	if wr, ok := webReqs["offline"].([]any); ok {
		var meta []string
		for _, x := range wr {
			r := x.(map[string]any)
			path, _ := r["path"].(string)
			if isMetadataPath(path) {
				meta = append(meta, fmt.Sprintf("%v %s", r["status"], path))
			}
		}
		t.Logf("web offline metadata requests: %v", meta)
	}
	tb.centralDown.Store(false)
	tb.online()
}

// isMetadataPath reports whether path is a metadata collection the apps load.
func isMetadataPath(p string) bool {
	for _, pre := range []string{"/api/programs", "/api/programStages", "/api/trackedEntityAttributes",
		"/api/trackedEntityTypes", "/api/dataElements", "/api/optionSets", "/api/optionGroups",
		"/api/categoryCombos", "/api/categoryOptionCombos", "/api/categoryOptions", "/api/organisationUnits",
		"/api/programRules", "/api/programRuleVariables", "/api/programIndicators", "/api/relationshipTypes",
		"/api/dataStore/", "/api/systemSettings", "/api/userSettings", "/api/me"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}
