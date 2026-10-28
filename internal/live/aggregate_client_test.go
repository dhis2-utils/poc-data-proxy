package live

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// dataEntry drives the Aggregate Data Entry app through the hub with Playwright
// (clients/aggregate_data_entry.py) in one shared browser, as the nurse (user A) and
// the clerk (user B).
type dataEntry struct {
	e            *tk.Env
	ag           *tk.Aggregate
	nurse, clerk *tk.Client
	pe, base     string
	control      *httptest.Server
	updateApp    func() error // GET /update-app
}

// newDataEntry starts a hub for the app, or skips. fixedAddr serves the hub on a
// fixed port, so it can restart (GET /reboot) under the same address: the browser's
// storage belongs to the address.
func newDataEntry(t *testing.T, fixedAddr bool) *dataEntry {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real Aggregate Data Entry app")
	}
	e := tk.Start(t)
	if !e.Live() || e.M.Aggregate == nil || e.M.Aggregate.NumberLabel == "" {
		t.Skip("needs a live instance whose manifest names the form's rows")
	}
	if fixedAddr {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		l.Close()
		e.ServeOn(addr)
	}
	e.Setup()
	d := &dataEntry{e: e, ag: e.M.Aggregate, nurse: e.Web(tk.RoleNurse), clerk: e.Web(tk.RoleClerk)}
	d.nurse.Login()
	d.clerk.Login()
	e.Sync()
	d.pe = aggPeriod(e, d.ag)
	d.control = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch r.URL.Path {
		case "/offline":
			err = e.SetUplink(false)
		case "/online":
			err = e.SetUplink(true)
		case "/sync":
			err = e.App.Worker.Cycle(r.Context())
		case "/reboot":
			e.Reboot()
		case "/update-app":
			err = d.updateApp()
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
	t.Cleanup(d.control.Close)
	d.base = t.TempDir()
	if p := os.Getenv("HUB_REPORT"); p != "" {
		d.base = filepath.Join(filepath.Dir(p), "aggregate-data-entry")
	}
	return d
}

// openForm starts from an open, incomplete form: earlier runs may have completed it.
func (d *dataEntry) openForm(t *testing.T, ds string) {
	t.Helper()
	if err := d.e.Central.JSON("POST", "/api/dataEntry/dataSetCompletion", map[string]any{"dataSet": ds,
		"period": d.pe, "orgUnit": d.e.M.Facility.ID, "completed": false}, nil); err != nil {
		t.Fatal(err)
	}
	d.e.Sync() // the hub learns changes made on central at its next sync
}

// run runs one scenario of the driver; values are VALUE_A, VALUE_B, ...
func (d *dataEntry) run(t *testing.T, scenario, ds, name string, values ...string) (aggClientResult, string) {
	t.Helper()
	out := filepath.Join(d.base, name)
	os.RemoveAll(out)
	os.MkdirAll(out, 0o755)
	cmd := exec.Command("python3", "-I", "../../clients/aggregate_data_entry.py")
	cmd.Env = append(os.Environ(), "SCENARIO="+scenario, "HUB_URL="+d.e.Srv.URL, "CONTROL_URL="+d.control.URL,
		"USER_A="+d.nurse.User, "USER_B="+d.clerk.User, "HUB_PASSWORD="+d.e.M.Password, "DATASET="+ds,
		"ORGUNIT="+d.e.M.Facility.ID, "PERIOD="+d.pe, "ROW_A="+d.ag.NumberLabel, "ROW_B="+d.ag.IntegerLabel,
		"COLUMN="+fmt.Sprint(d.ag.Column), "OUT="+out, "PROFILE="+filepath.Join(t.TempDir(), "profile"))
	for i, v := range values {
		cmd.Env = append(cmd.Env, fmt.Sprintf("VALUE_%c=%s", 'A'+i, v))
	}
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("aggregate_data_entry.py: %v\n%s", err, b)
	}
	os.WriteFile(filepath.Join(out, "result.json"), b, 0o644)
	var res aggClientResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	for _, k := range sortedKeys(res.Steps) {
		t.Logf("%s: %s: %v", name, k, res.Steps[k])
	}
	if res.Error != "" {
		t.Fatalf("%s driver: %s (screenshots in %s)", name, res.Error, out)
	}
	for _, w := range res.Writes {
		t.Logf("%s write: %s %s %s %s → %d %s", name, w.User, w.Phase, w.Method, w.Path, w.Status, w.Body)
		if w.Phase != "online" && w.Phase != "reconnected" && w.Status == 503 {
			t.Errorf("offline write answered 503: the app queues it in the browser (%s %s)", w.Method, w.Path)
		}
	}
	for _, f := range res.Failed {
		t.Logf("%s failed read: %s %s %d %s", name, f.User, f.Phase, f.Status, f.Path)
	}
	for _, x := range res.Errors {
		t.Logf("%s page error: %s", name, x)
	}
	return res, out
}

// onCentral checks a cell on central: want "" means no value.
func (d *dataEntry) onCentral(t *testing.T, name, ds, de, want string) {
	t.Helper()
	val, by, ok := d.e.CentralDataValue(ds, d.pe, de, d.ag.COC, d.ag.DefaultAOC)
	switch {
	case want == "" && ok:
		t.Errorf("%s: %s on central: %q by %q, want none", name, de, val, by)
	case want != "" && (val != want || by != d.nurse.User):
		t.Errorf("%s: %s on central: %q by %q, want %s by %s", name, de, val, by, want, d.nurse.User)
	}
}

func (d *dataEntry) completedOnCentral(t *testing.T, ds string) bool {
	t.Helper()
	var regs struct {
		Registrations []struct {
			Completed bool `json:"completed"`
		} `json:"completeDataSetRegistrations"`
	}
	q := "dataSet=" + ds + "&orgUnit=" + d.e.M.Facility.ID + "&period=" + d.pe
	if err := d.e.Central.JSON("GET", "/api/completeDataSetRegistrations?"+q, nil, &regs); err != nil {
		t.Fatal(err)
	}
	return len(regs.Registrations) > 0 && regs.Registrations[0].Completed
}

func value() string { return fmt.Sprint(100 + rand.N(800)) }

// TestAggregateDataEntryApp drives the Aggregate Data Entry app through the hub with
// Playwright (clients/aggregate_data_entry.py), in one shared browser: the nurse
// enters a value online, then offline (browser closed and opened again) another, and
// marks the form complete; the clerk opens the same browser offline, and again once
// back online. Then the same on a data set with mandatory validation, where the app
// itself refuses to complete without validating. Both values must reach central as the nurse's (the spike found the
// browser's own queue sending them as the clerk's). It needs a live instance whose
// manifest names the form's rows, and HUB_CLIENTS=1. With HUB_DATAENTRY_ZIP, it runs
// against that release of the app, installed on central for the test (N19, N20).
func TestAggregateDataEntryApp(t *testing.T) {
	d := newDataEntry(t, false)
	ag := d.ag
	if zip := os.Getenv("HUB_DATAENTRY_ZIP"); zip != "" {
		_, v := installDataEntry(t, d.e, zip)
		t.Logf("the Data Entry app on central: %s", v)
	}
	run := func(ds, name string) (aggClientResult, string) {
		t.Helper()
		d.openForm(t, ds)
		va, vb := value(), value()
		res, out := d.run(t, "complete", ds, name, va, vb)
		if got := res.Steps["B offline: row A"]; got != va {
			t.Errorf("%s: the clerk offline sees row A %v, want the nurse's online value %s", name, got, va)
		}
		if got := res.Steps["B offline: row B"]; got != vb {
			t.Errorf("%s: the clerk offline sees row B %v, want the nurse's offline value %s", name, got, vb)
		}
		d.onCentral(t, name, ds, ag.Number, va)
		d.onCentral(t, name, ds, ag.Integer, vb)
		return res, out
	}
	res, _ := run(ag.DataSet, "plain")
	if st, _ := res.Steps["complete offline: status"].(float64); st != 200 {
		t.Errorf("completing offline: %v, want 200 (queued)", res.Steps["complete offline: status"])
	}
	if ag.ValidDataSet != "" {
		// Mandatory validation: the app runs validation before completing; the hub
		// cannot offline, so the app refuses to complete ("Couldn't validate the form,
		// please try again later") and sends nothing. The hub's own refusal (409) is
		// the guard for other clients (TestAggregateCompletionOffline).
		res, out := run(ag.ValidDataSet, "validated")
		if st, ok := res.Steps["complete offline: status"].(string); !ok || !strings.HasPrefix(st, "no request") {
			t.Errorf("completing offline with mandatory validation: %v, want no completion request", res.Steps["complete offline: status"])
		}
		if shown := fmt.Sprint(res.Steps["complete offline: shown"]); !strings.Contains(shown, "Couldn't validate the form") {
			t.Errorf("the app showed %s (screenshot %s)", shown, filepath.Join(out, "2-complete-offline.png"))
		}
	}
	t.Logf("screenshots and results in %s", d.base)
}

// TestDataEntryAppDrafts (adversarial I6c) leaves a form incomplete offline, as a
// draft, across a restart of the hub, with a validation rule on central: the nurse
// runs validation online (no alerts), then offline enters a value that breaks the
// rule and runs validation again; the hub restarts; she changes the value and clears
// another, leaving the form incomplete; the clerk sees the draft offline; after
// reconnection central has the last values as the nurse's and the form incomplete.
// Validation offline needs a connection (the hub replayed the online answer before).
func TestDataEntryAppDrafts(t *testing.T) {
	d := newDataEntry(t, true)
	e, ag := d.e, d.ag
	// The rule: number ≤ integer. Removed at the end: the instance is shared.
	rule := tk.UID()
	if err := e.Central.JSON("POST", "/api/validationRules", map[string]any{"id": rule, "name": "Hub test number le integer " + rule,
		"importance": "HIGH", "operator": "less_than_or_equal_to", "periodType": "Monthly",
		"leftSide":  map[string]any{"expression": "#{" + ag.Number + "}", "missingValueStrategy": "SKIP_IF_ANY_VALUE_MISSING"},
		"rightSide": map[string]any{"expression": "#{" + ag.Integer + "}", "missingValueStrategy": "SKIP_IF_ANY_VALUE_MISSING"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Central.Do("DELETE", "/api/validationRules/"+rule, nil) })
	d.openForm(t, ag.DataSet)
	low := 100 + rand.N(100)
	breaks, last := fmt.Sprint(low+500), fmt.Sprint(low+600) // both above the integer
	// Central flags the values the nurse will enter offline.
	e.SetCentralDataValue(ag.DataSet, d.pe, ag.Integer, ag.COC, fmt.Sprint(low+400))
	e.SetCentralDataValue(ag.DataSet, d.pe, ag.Number, ag.COC, breaks)
	var check struct {
		ValidationRuleViolations []any `json:"validationRuleViolations"`
	}
	vpath := "/api/validation/dataSet/" + ag.DataSet + "?pe=" + d.pe + "&ou=" + e.M.Facility.ID
	if err := e.Central.JSON("GET", vpath, nil, &check); err != nil || len(check.ValidationRuleViolations) != 1 {
		t.Fatalf("central's validation of %s ≤ %d: %+v %v", breaks, low+400, check, err)
	}
	e.SetCentralDataValue(ag.DataSet, d.pe, ag.Number, ag.COC, fmt.Sprint(low))
	e.Sync()
	res, out := d.run(t, "drafts", ag.DataSet, "drafts", breaks, "", last)
	s := res.Steps

	shownOnline := fmt.Sprint(s["validate online: shown"])
	if !strings.Contains(shownOnline, "No validation alerts") {
		t.Errorf("validation online: %s, want no alerts (screenshot %s)", shownOnline, filepath.Join(out, "1-validated-online.png"))
	}
	for _, c := range []struct{ step, want string }{
		{"A after restart: row A", breaks}, {"A after restart: row B", fmt.Sprint(low + 400)},
		{"B offline: row A", last}, {"B offline: row B", ""}, {"B offline: completion", "Mark complete"},
		{"A reconnected: row A", last}, {"A reconnected: row B", ""}, {"A reconnected: completion", "Mark complete"},
	} {
		if got := fmt.Sprint(s[c.step]); got != c.want {
			t.Errorf("%s: %q, want %q", c.step, got, c.want)
		}
	}
	d.onCentral(t, "drafts", ag.DataSet, ag.Number, last)
	d.onCentral(t, "drafts", ag.DataSet, ag.Integer, "")
	if d.completedOnCentral(t, ag.DataSet) {
		t.Error("the draft is completed on central")
	}
	pending, issues := e.App.Hub.Store.CountOutbox(store.OutboxPending), e.App.Hub.Store.CountIssues()
	t.Logf("after the sync: outbox %d, refused %d", pending, issues)
	if pending != 0 || issues != 0 {
		t.Errorf("after the sync: outbox %d, refused %d", pending, issues)
	}
	t.Logf("screenshots and results in %s", out)

	// Offline, validation needs central: the hub does not replay the online answer,
	// made before the values changed (it showed "No validation alerts" here). The
	// app tries four times, then says so.
	shown := fmt.Sprint(s["validate offline: shown"])
	if st, _ := s["validate offline: status"].(float64); st != 503 || !strings.Contains(shown, "Validation could not be run") {
		t.Errorf("validation offline after an edit that breaks the rule: %v %s (screenshot %s)",
			s["validate offline: status"], shown, filepath.Join(out, "2-validated-offline.png"))
	}
}

// TestDataEntryAppUpdated (adversarial N20) updates the Data Entry app on central
// while the hub is offline (HUB_DATAENTRY_ZIP: the newest release, as on the App
// Hub): the browser keeps the cached build offline and saves; online it loads the
// new build, which then works offline too, for a second user as well. Every value
// reaches central as the nurse's. The app's earlier version is put back at the end.
func TestDataEntryAppUpdated(t *testing.T) {
	zip := os.Getenv("HUB_DATAENTRY_ZIP")
	if zip == "" {
		t.Skip("set HUB_DATAENTRY_ZIP to a release of the Data Entry app newer than central's")
	}
	d := newDataEntry(t, false)
	e, ag := d.e, d.ag
	was := bundledDataEntry(t, e)
	oldBuild := centralDataEntryBuild(t, e)
	newBuild, newVersion := zipBuild(t, zip)
	if oldBuild == newBuild {
		t.Fatalf("central already runs %s", newBuild)
	}
	d.updateApp = func() error { return uploadApp(e, zip) }
	t.Cleanup(func() { restoreDataEntry(t, e, was) })
	d.openForm(t, ag.DataSet)
	va, vb, vc, vd := value(), value(), value(), value()
	res, out := d.run(t, "update", ag.DataSet, "update", va, vb, vc, vd)
	s := res.Steps
	t.Logf("builds: central's %s, the new %s (%s)", oldBuild, newBuild, newVersion)
	for _, c := range []struct{ step, want string }{
		{"online: build", oldBuild}, {"offline after the update: build", oldBuild},
		{"online, reloaded: build", newBuild}, {"offline, new build: build", newBuild}, {"B offline, new build: build", newBuild},
		{"online, reloaded: row B", vb}, {"offline, new build: row A", vc}, {"B offline: row A", vc}, {"B offline: row B", vd},
	} {
		if got := fmt.Sprint(s[c.step]); got != c.want {
			t.Errorf("%s: %q, want %q", c.step, got, c.want)
		}
	}
	d.onCentral(t, "update", ag.DataSet, ag.Number, vc)
	d.onCentral(t, "update", ag.DataSet, ag.Integer, vd)
	t.Logf("screenshots and results in %s", out)
}

var mainBuild = regexp.MustCompile(`main-[A-Za-z0-9_-]+\.js`)

// centralDataEntryBuild is the build (main-*.js) central serves for the Data Entry app.
func centralDataEntryBuild(t *testing.T, e *tk.Env) string {
	t.Helper()
	// The global shell's frame asks with redirect=false; without it central redirects to the shell.
	st, b, err := e.Central.Do("GET", "/dhis-web-aggregate-data-entry/index.html?redirect=false", nil)
	if err != nil || st != 200 {
		t.Fatalf("the Data Entry app on central: %d %v", st, err)
	}
	return mainBuild.FindString(string(b))
}

// zipBuild reads a release's build (main-*.js) and version.
func zipBuild(t *testing.T, path string) (build, version string) {
	t.Helper()
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		r.Close()
		switch f.Name {
		case "index.html":
			build = mainBuild.FindString(string(b))
		case "manifest.webapp":
			var m struct{ Version string }
			json.Unmarshal(b, &m)
			version = m.Version
		}
	}
	return build, version
}

type dataEntryApp struct {
	Version string `json:"version"`
	Bundled bool   `json:"bundled"` // central ships the app (also when a newer one is installed over it)
}

func centralDataEntry(e *tk.Env) (dataEntryApp, error) {
	var apps []struct {
		Key string `json:"key"`
		dataEntryApp
	}
	if err := e.Central.JSON("GET", "/api/apps?fields=key,version,bundled", nil, &apps); err != nil {
		return dataEntryApp{}, err
	}
	for _, a := range apps {
		if a.Key == "aggregate-data-entry" {
			return a.dataEntryApp, nil
		}
	}
	return dataEntryApp{}, fmt.Errorf("no Data Entry app on central")
}

// bundledDataEntry returns the version of the Data Entry app central runs, or skips
// unless central ships the app: the test puts it back by deleting what it installed
// over it, and central then serves the version it ships (2.43, DefaultAppManager.deleteApp).
// That should be the version central ran before; restoreDataEntry checks it.
func bundledDataEntry(t *testing.T, e *tk.Env) string {
	t.Helper()
	a, err := centralDataEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Bundled {
		t.Skipf("central does not ship the Data Entry app (%s): the test could not put it back", a.Version)
	}
	return a.Version
}

// installDataEntry installs a release of the Data Entry app on central for the test,
// and puts central's back at the end. It returns the version installed.
func installDataEntry(t *testing.T, e *tk.Env, zip string) (before, after string) {
	t.Helper()
	was := bundledDataEntry(t, e)
	t.Cleanup(func() { restoreDataEntry(t, e, was) })
	if err := uploadApp(e, zip); err != nil {
		t.Fatal(err)
	}
	now, err := centralDataEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	return was, now.Version
}

// restoreDataEntry deletes a Data Entry app installed over the one central ships, so
// central serves its own again (version was).
func restoreDataEntry(t *testing.T, e *tk.Env, was string) {
	t.Helper()
	if a, err := centralDataEntry(e); err == nil && a.Version == was {
		return
	}
	if st, b, err := e.Central.Do("DELETE", "/api/apps/aggregate-data-entry", nil); err != nil || st/100 != 2 {
		t.Errorf("removing the installed Data Entry app: %d %v %.300s", st, err, b)
	}
	if a, err := centralDataEntry(e); err != nil || a.Version != was {
		t.Errorf("central's Data Entry app after removing the installed one: %+v %v, want %s", a, err, was)
	} else {
		t.Logf("central's Data Entry app back to %s", a.Version)
	}
}

type aggClientResult struct {
	Steps  map[string]any `json:"steps"`
	Writes []struct {
		User, Phase, Method, Path, Body string
		Status                          int
	} `json:"writes"`
	Failed []struct {
		User, Phase, Path string
		Status            int
	} `json:"failed"`
	Errors []string `json:"errors"`
	Error  string   `json:"error"`
}

func sortedKeys(m map[string]any) []string {
	keys := slices.Collect(maps.Keys(m))
	slices.Sort(keys)
	return keys
}
