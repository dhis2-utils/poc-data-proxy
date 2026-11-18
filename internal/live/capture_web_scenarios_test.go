package live

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Capture web against the hub in the adversarial scenarios of wave 2
// (docs/ADVERSARIAL-TESTING.md: I1c, I2c, I10c/N18, N19), driven by
// clients/capture_web_scenarios.py. Like TestCaptureWeb they need HUB_CLIENTS=1 and a
// live central; screenshots go next to HUB_REPORT, or to the test's temporary folder.

// scenarioRun is what clients/capture_web_scenarios.py printed.
type scenarioRun struct {
	Login     int                       `json:"login"`
	Steps     map[string]map[string]any `json:"steps"`
	Failed    []map[string]any          `json:"failed"`
	Errors    []map[string]any          `json:"errors"`
	Requests  []map[string]any          `json:"requests"`
	Exception string                    `json:"exception"`
}

// startCapture starts a hub for a Capture web scenario: set up, the nurse logged in
// once online, and synced.
func startCapture(t *testing.T) *tk.Env {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real Capture web app")
	}
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST)")
	}
	e.Setup()
	e.Web(tk.RoleNurse).Login()
	e.Sync()
	return e
}

// reportDir is where a scenario's screenshots and result go.
func reportDir(t *testing.T, name string) string {
	if p := os.Getenv("HUB_REPORT"); p != "" {
		out := filepath.Join(filepath.Dir(p), name)
		os.MkdirAll(out, 0o755)
		return out
	}
	return t.TempDir()
}

// runScenario runs one action of the script. Each control step the script asks for
// (GET <control>/<step>) runs in the test process, one at a time; offline and online
// are always there.
func runScenario(t *testing.T, e *tk.Env, action, out string, steps map[string]func() error, env ...string) scenarioRun {
	t.Helper()
	var mu sync.Mutex
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		name := strings.TrimPrefix(r.URL.Path, "/")
		var err error
		switch f := steps[name]; {
		case f != nil:
			err = f()
		case name == "offline":
			err = e.SetUplink(false)
		case name == "online":
			err = e.SetUplink(true)
		default:
			err = fmt.Errorf("no step %q", name)
		}
		t.Logf("step %s: %v", name, err)
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
	defer control.Close()
	var progs []string
	for _, p := range e.M.Programs {
		progs = append(progs, p.ID)
	}
	cmd := exec.Command("python3", "-I", "../../clients/capture_web_scenarios.py")
	cmd.Env = append(append(os.Environ(), "HUB_URL="+e.Srv.URL, "CONTROL_URL="+control.URL, "ACTION="+action,
		"OUT="+out, "PASSWORD="+e.M.Password, "ORG_UNIT="+e.M.Facility.ID, "PROGRAMS="+strings.Join(progs, ",")), env...)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("capture_web_scenarios.py %s: %v\n%s", action, err, b)
	}
	os.WriteFile(filepath.Join(out, "result.json"), b, 0o644)
	var res scenarioRun
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if res.Exception != "" {
		t.Fatalf("Capture: %s (screenshots in %s)", res.Exception, out)
	}
	for _, pe := range res.Errors {
		t.Logf("page error (%v): %v", pe["phase"], pe["error"])
	}
	return res
}

// eventsWithWeight lists the events on central, under an enrollment, holding a value.
func eventsWithWeight(t *testing.T, e *tk.Env, enr, value string) []string {
	t.Helper()
	var evs struct {
		Events []struct {
			Event      string
			CreatedBy  struct{ Username string } `json:"createdBy"`
			DataValues []struct{ Value string }  `json:"dataValues"`
		} `json:"events"`
	}
	if err := e.Central.JSON("GET", enrollmentEventsPath(t, e, enr), nil, &evs); err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, ev := range evs.Events {
		for _, dv := range ev.DataValues {
			if dv.Value == value {
				found = append(found, ev.Event+" by "+ev.CreatedBy.Username)
			}
		}
	}
	return found
}

// enrollmentEventsPath reads an enrollment's events from central with the enrollment's
// program: 2.43.2.1 answers a bare HTTP 400 to events?enrollments= without one (2.42
// answers it; seen 17 November 2026).
func enrollmentEventsPath(t *testing.T, e *tk.Env, enr string) string {
	t.Helper()
	var en struct{ Program string }
	if err := e.Central.JSON("GET", "/api/tracker/enrollments/"+enr+"?fields=program", nil, &en); err != nil {
		t.Fatal(err)
	}
	return "/api/tracker/events?paging=false&program=" + en.Program + "&enrollments=" + enr + "&fields=event,createdBy,dataValues[value]"
}

func str(v any) string { s, _ := v.(string); return s }

func num(v any) int { f, _ := v.(float64); return int(f) }

// TestCaptureWebRefusals (I1c): what Capture web shows for each way the hub refuses a
// save offline, with a screenshot of each (I8): "upgrading" (local writes frozen as
// during an upgrade) and a refusal by the hub's own validation (central made the stage
// non-repeatable after Capture cached its metadata: E1039). After each, the dashboard
// is opened again: nothing may appear saved that is not on central after the sync.
// First, a note on the enrollment, which until 17 November 2026 needed a connection:
// taken offline, shown when the dashboard is opened again, on central once, by the
// nurse, after the sync.
func TestCaptureWebRefusals(t *testing.T) {
	e := startCapture(t)
	m := e.M
	p := m.Primary()
	stage := p.Stages[0]
	enr := enrollmentAt(t, e, p, m.Facility.ID)
	if enr == "" {
		t.Fatal("no active enrollment at the facility")
	}
	var was struct {
		Repeatable  bool
		DisplayName string `json:"displayName"`
	}
	if err := e.Central.JSON("GET", "/api/programStages/"+stage+"?fields=repeatable,displayName", nil, &was); err != nil || !was.Repeatable {
		t.Fatalf("stage %s must be repeatable to start: %+v %v", stage, was, err)
	}
	setRepeatable := func(v bool) error {
		st, b, err := e.Central.Do("PATCH", "/api/programStages/"+stage,
			[]any{map[string]any{"op": "replace", "path": "/repeatable", "value": v}})
		if err == nil && st/100 != 2 {
			err = fmt.Errorf("%d %.300s", st, b)
		}
		return err
	}
	t.Cleanup(func() {
		if err := setRepeatable(true); err != nil {
			t.Errorf("stage %s not made repeatable again: %v", stage, err)
		}
	})

	stamp := time.Now().UnixNano()
	note := fmt.Sprintf("I1c note %d", stamp)
	wUp, wRef := fmt.Sprint(900+stamp%50), fmt.Sprint(950+stamp%50)
	out := reportDir(t, "capture-web-refusals")
	nurse := e.M.Users[tk.RoleNurse].Username
	res := runScenario(t, e, "refusals", out, map[string]func() error{
		"freeze": func() error { e.App.Hub.FreezeLocalWrites(); return nil },
		"thaw":   func() error { e.App.Hub.ThawLocalWrites(); return nil },
		// Central changes the stage while the hub is online; the hub syncs and goes
		// offline again. The browser does not load anything meanwhile.
		"refuse": func() error {
			if err := e.SetUplink(true); err != nil {
				return err
			}
			if err := setRepeatable(false); err != nil {
				return err
			}
			if err := e.App.Worker.Cycle(t.Context()); err != nil {
				return err
			}
			return e.SetUplink(false)
		},
	}, "USER="+nurse, "ENROLLMENT="+enr, "STAGE_NAME="+was.DisplayName, "NOTE="+note, "WEIGHT_UPGRADING="+wUp, "WEIGHT_REFUSED="+wRef)
	if res.Login != 200 {
		t.Fatalf("login: %d", res.Login)
	}

	e.Online()
	e.Sync()
	// What reached central, against what Capture showed after opening the page again.
	var notes struct {
		Notes []struct {
			Value     string
			CreatedBy struct{ Username string } `json:"createdBy"`
		} `json:"notes"`
	}
	e.Central.JSON("GET", "/api/tracker/enrollments/"+enr+"?fields=notes[value,createdBy]", nil, &notes)
	var noteBy []string
	for _, n := range notes.Notes {
		if n.Value == note {
			noteBy = append(noteBy, n.CreatedBy.Username)
		}
	}
	s := res.Steps["note-offline"]
	t.Logf("note offline: the hub answered %d %.200s; Capture's alert: %v; on central after the sync, by: %v",
		num(s["status"]), str(s["answer"]), s["alerts"], noteBy)
	if num(s["status"])/100 != 2 {
		t.Errorf("a note offline: the hub answered %d %.300s", num(s["status"]), str(s["answer"]))
	}
	if alerts, _ := s["alerts"].([]any); len(alerts) > 0 {
		t.Errorf("a note offline: Capture showed an alert: %v", alerts)
	}
	if !strings.Contains(str(s["reopened"]), note) {
		t.Errorf("a note offline: not shown when the dashboard is opened again offline")
	}
	if len(noteBy) != 1 || noteBy[0] != nurse {
		t.Errorf("a note offline: on central %d times, by %v; want once, by %s", len(noteBy), noteBy, nurse)
	}
	for _, c := range []struct {
		step, value, want string
		onCentral         int
	}{
		{"upgrading", wUp, "upgrading", len(eventsWithWeight(t, e, enr, wUp))},
		{"refused", wRef, "E1039", len(eventsWithWeight(t, e, enr, wRef))},
	} {
		s := res.Steps[c.step]
		status, answer := num(s["status"]), str(s["answer"])
		// Shown as saved after opening the page again: the note's text, or one more
		// event in the stage than before the save (the stage's table shows no values).
		shown := num(s["events-reopened"]) > num(s["events-before"])
		t.Logf("%s: events in the stage before the save %d, opened again %d", c.step, num(s["events-before"]), num(s["events-reopened"]))
		t.Logf("%s: the hub answered %d %.200s", c.step, status, answer)
		t.Logf("%s: opened again, Capture shows %q: %v; on central after the sync: %d", c.step, c.value, shown, c.onCentral)
		alerts, _ := s["alerts"].([]any)
		t.Logf("%s: Capture's alert: %v", c.step, alerts)
		if len(alerts) == 0 {
			t.Errorf("I8: %s: Capture showed no alert after the hub refused the save", c.step)
		}
		if !strings.Contains(answer, c.want) {
			t.Errorf("%s: the hub's answer has no %q: %d %.300s", c.step, c.want, status, answer)
		}
		if c.onCentral > 1 {
			t.Errorf("I2: %s: %q is on central %d times", c.step, c.value, c.onCentral)
		}
		if shown && c.onCentral == 0 {
			t.Errorf("I1/I8: %s: Capture shows %q as saved after opening the page again; central does not have it", c.step, c.value)
		}
	}
	t.Logf("requests the hub answered with an error: %d; screenshots in %s", len(res.Failed), out)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestCaptureWebUsers (I2c): users in turn on one browser, through the hub. The nurse
// works online, then offline; then the restricted user (a stage hidden) and the
// outsider (captures at the sibling) each log in on the same browser offline first,
// then online, then offline again, logging out between, as staff sharing a computer
// do. Nothing may be shown to a user that the same user does not see alone in a fresh
// browser online, where the hub passes everything to central (I3). Marked people are
// made at the facility for it: one per program, the maternal one with an event in the
// stage the restricted user may not see.
func TestCaptureWebUsers(t *testing.T) {
	e := startCapture(t)
	m := e.M
	stamp := fmt.Sprint(time.Now().UnixNano() % 1e8)
	var enrollments, names []string
	for _, p := range m.Programs[:3] { // maternal (OPEN), HIV (PROTECTED), TB (CLOSED)
		name := "I2c" + strings.ToUpper(p.ID[6:9]) + stamp
		stages := p.Stages[:1]
		if p.RestrictedStage != "" {
			stages = append(stages, p.RestrictedStage)
		}
		pr := m.NewPerson(p, name, stages...)
		e.CentralComplete(p, pr)
		e.CentralImport(pr.Payload)
		t.Cleanup(func() { e.CentralDelete("trackedEntities", pr.TE) })
		enrollments = append(enrollments, p.ID+":"+pr.Enrollment+":"+name)
		names = append(names, name)
	}
	users := []string{m.Users[tk.RoleNurse].Username}
	for _, role := range []string{tk.RoleRestricted, tk.RoleOutsider} {
		// Each has used the hub before, from another device: the hub knows them offline.
		c := e.Web(role)
		st, b := c.Do("POST", "/api/auth/login", map[string]string{"username": c.User, "password": m.Password})
		t.Logf("%s logs in at the hub online: %d %.200s", c.User, st, b)
		users = append(users, c.User)
	}
	e.Sync()

	out := reportDir(t, "capture-web-users")
	res := runScenario(t, e, "users", out, nil, "USERS="+strings.Join(users, ","), "ENROLLMENTS="+strings.Join(enrollments, ","))

	type view struct {
		rows   map[string]bool // working list rows, all programs
		names  map[string]bool // marked people shown, in lists or dashboards
		stages map[string]int  // events per stage, all dashboards ("enrollment/stage")
	}
	read := func(tag string) view {
		s := res.Steps[tag]
		v := view{rows: map[string]bool{}, names: map[string]bool{}, stages: map[string]int{}}
		for k, x := range s {
			switch {
			case strings.HasPrefix(k, "rows:"):
				rows, _ := x.([]any)
				for _, r := range rows {
					v.rows[str(r)] = true
					for _, n := range names {
						if strings.Contains(str(r), n) {
							v.names[n] = true
						}
					}
				}
			case strings.HasPrefix(k, "dashboard:"):
				d, _ := x.(map[string]any)
				enr := strings.TrimPrefix(k, "dashboard:")
				if d["name"] == true {
					for _, en := range enrollments {
						if strings.Contains(en, enr) {
							v.names[en[strings.LastIndex(en, ":")+1:]] = true
						}
					}
				}
				stages, _ := d["stages"].(map[string]any)
				for st, n := range stages {
					v.stages[enr+"/"+st] = num(n)
				}
			}
		}
		t.Logf("%s (login %v): %d rows, people %v, stages %v, IndexedDB %v", tag, s["login"], len(v.rows),
			slices.Sorted(maps.Keys(v.names)), v.stages, s["indexeddb"])
		return v
	}
	first := read(users[0] + "-online")
	if len(first.names) != len(names) {
		t.Errorf("the nurse does not see every marked person online: %v", first.names)
	}
	read(users[0] + "-offline")
	for _, u := range users[1:] {
		truth := read(u + "-fresh")
		for _, tag := range []string{"-offline-first", "-online", "-offline"} {
			v := read(u + tag)
			for n := range v.names {
				if !truth.names[n] {
					t.Errorf("I3: %s%s: shows %s, whom %s alone online does not see", u, tag, n, u)
				}
			}
			for k, n := range v.stages {
				if n > truth.stages[k] {
					t.Errorf("I3: %s%s: dashboard shows %d events in %s; %s alone online sees %d", u, tag, n, k, u, truth.stages[k])
				}
			}
			for r := range v.rows {
				if !truth.rows[r] {
					t.Logf("%s%s: a row %s alone online does not have (offline the hub lists this facility only): %s", u, tag, u, r)
				}
			}
		}
	}
	t.Logf("screenshots in %s", out)
}

// captureZip is the newer Capture web to install on central (I10c, N18, N19):
// HUB_CAPTURE_ZIP, a release zip from the App Hub, or the same build from
// github.com/dhis2/capture-app/releases. It returns the zip and its version.
func captureZip(t *testing.T) (string, string) {
	t.Helper()
	p := os.Getenv("HUB_CAPTURE_ZIP")
	if p == "" {
		t.Skip("set HUB_CAPTURE_ZIP to a Capture release zip (App Hub, or github.com/dhis2/capture-app/releases)")
	}
	z, err := zip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	f, err := z.Open("manifest.webapp")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var man struct{ Version string }
	if err := json.NewDecoder(f).Decode(&man); err != nil || man.Version == "" {
		t.Fatalf("%s: no version in manifest.webapp: %v", p, err)
	}
	return p, man.Version
}

// captureVersion is the Capture version central serves.
func captureVersion(e *tk.Env) (string, error) {
	var apps []struct{ Key, Version string }
	if err := e.Central.JSON("GET", "/api/apps?fields=key,version", nil, &apps); err != nil {
		return "", err
	}
	for _, a := range apps {
		if a.Key == "capture" {
			return a.Version, nil
		}
	}
	return "", fmt.Errorf("no Capture app on central")
}

// keepCapture puts central's Capture back when the test ends: removing an installed
// Capture brings back the one bundled with DHIS2. It returns the version before.
func keepCapture(t *testing.T, e *tk.Env) string {
	t.Helper()
	before, err := captureVersion(e)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if v, _ := captureVersion(e); v == before {
			return
		}
		if st, b, err := e.Central.Do("DELETE", "/api/apps/capture", nil); err != nil || st/100 != 2 {
			t.Errorf("removing the installed Capture: %d %v %.300s", st, err, b)
		}
		if v, err := captureVersion(e); v != before {
			t.Errorf("central's Capture is %s after the test, was %s (%v)", v, before, err)
		} else {
			t.Logf("central's Capture back at %s", v)
		}
	})
	return before
}

// TestCaptureWebUpdate (I10c, N18): Capture web updated on central, in App Management,
// while the hub is offline. The browser used Capture through the hub online and
// offline; offline, the cached Capture must keep working; online, the new one loads;
// offline again, the new one works as far as the hub cached it, and what it cannot
// answer is "needs connection", never another version's or user's answer.
func TestCaptureWebUpdate(t *testing.T) {
	zipPath, newVersion := captureZip(t)
	e := startCapture(t)
	old := keepCapture(t, e)
	if old == newVersion {
		t.Fatalf("central already serves Capture %s", old)
	}
	enr := enrollmentAt(t, e, e.M.Primary(), e.M.Facility.ID)
	out := reportDir(t, "capture-web-update")
	res := runScenario(t, e, "update", out, map[string]func() error{
		"install": func() error { return uploadApp(e, zipPath) },
	}, "USER="+e.M.Users[tk.RoleNurse].Username, "ENROLLMENT="+enr)
	if v, _ := captureVersion(e); v != newVersion {
		t.Fatalf("central serves Capture %s after the install, want %s", v, newVersion)
	}
	want := map[string]string{"online-before": old, "offline-before": old, "offline-installed": old,
		"online-new": newVersion, "offline-new": newVersion}
	rows := num(res.Steps["online-before"]["rows"])
	for _, step := range []string{"online-before", "offline-before", "offline-installed", "online-new", "offline-new"} {
		s := res.Steps[step]
		t.Logf("%s: Capture %v in %v, %v rows, dashboard shown %v", step, s["version"], s["frame"], s["rows"], s["dashboard-ok"])
		if str(s["version"]) != want[step] {
			t.Errorf("%s: Capture %v, want %s", step, s["version"], want[step])
		}
		if num(s["rows"]) == 0 || num(s["rows"]) != rows {
			t.Errorf("%s: the working list shows %v rows, %d before", step, s["rows"], rows)
		}
		if s["dashboard-ok"] != true {
			t.Errorf("%s: the enrollment dashboard did not open: %.300s", step, oneLine(str(s["dashboard"])))
		}
	}
	gaps := map[string]int{}
	for _, f := range res.Failed {
		phase := str(f["phase"])
		if !strings.HasPrefix(phase, "offline") && phase != "install" {
			t.Errorf("online, the hub answered %v for %v", f["status"], f["path"])
			continue
		}
		if num(f["status"]) != 503 {
			t.Errorf("offline (%s), the hub answered %v for %v; want 503, needs connection", phase, f["status"], f["path"])
		}
		gaps[phase+" "+str(f["path"])]++
	}
	for _, k := range slices.Sorted(maps.Keys(gaps)) {
		t.Logf("offline, the hub could not answer: %s (x%d)", k, gaps[k])
	}
	t.Logf("screenshots in %s", out)
}

// TestCaptureWebNewest (N19): the checks of TestCaptureWeb (working lists online and
// offline, the dashboard, an offline save reaching central once) with the Capture in
// HUB_CAPTURE_ZIP installed on central, the newest from the App Hub.
func TestCaptureWebNewest(t *testing.T) {
	zipPath, version := captureZip(t)
	e := startCapture(t)
	if old := keepCapture(t, e); old != version {
		installApp(t, e, zipPath)
	}
	if v, _ := captureVersion(e); v != version {
		t.Fatalf("central serves Capture %s, want %s", v, version)
	}
	t.Logf("Capture %s on central", version)
	checkCaptureWeb(t, e, reportDir(t, "capture-web-newest"))
}
