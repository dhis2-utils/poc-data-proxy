package live

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestAggregateDataEntryApp drives the Aggregate Data Entry app through the hub with
// Playwright (clients/aggregate_data_entry.py), in one shared browser: the nurse
// enters a value online, then offline (browser closed and opened again) another, and
// marks the form complete; the clerk opens the same browser offline, and again once
// back online. Then the same on a data set with mandatory validation, where the app
// itself refuses to complete without validating. Both values must reach central as the nurse's (the spike found the
// browser's own queue sending them as the clerk's). It needs a live instance whose
// manifest names the form's rows, and HUB_CLIENTS=1.
func TestAggregateDataEntryApp(t *testing.T) {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real Aggregate Data Entry app")
	}
	e, ag, nurse, clerk := aggregateEnv(t)
	if !e.Live() || ag.NumberLabel == "" {
		t.Skip("needs a live instance whose manifest names the form's rows")
	}
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		switch r.URL.Path {
		case "/offline":
			err = e.SetUplink(false)
		case "/online":
			err = e.SetUplink(true)
		case "/sync":
			err = e.App.Worker.Cycle(r.Context())
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
		}
	}))
	defer control.Close()

	base := t.TempDir()
	if p := os.Getenv("HUB_REPORT"); p != "" {
		base = filepath.Join(filepath.Dir(p), "aggregate-data-entry")
		os.RemoveAll(base)
	}
	pe := tk.CurrentMonth(e)
	run := func(ds, name string) (aggClientResult, string, string, string) {
		t.Helper()
		out := filepath.Join(base, name)
		os.MkdirAll(out, 0o755)
		// Earlier runs may have completed the form: start from an open, incomplete one.
		if err := e.Central.JSON("POST", "/api/dataEntry/dataSetCompletion", map[string]any{"dataSet": ds,
			"period": pe, "orgUnit": e.M.Facility.ID, "completed": false}, nil); err != nil {
			t.Fatal(err)
		}
		e.Sync() // the hub learns changes made on central at its next sync
		va, vb := fmt.Sprint(100+rand.N(800)), fmt.Sprint(100+rand.N(800))
		cmd := exec.Command("python3", "-I", "../../clients/aggregate_data_entry.py")
		cmd.Env = append(os.Environ(), "HUB_URL="+e.Srv.URL, "CONTROL_URL="+control.URL, "USER_A="+nurse.User,
			"USER_B="+clerk.User, "HUB_PASSWORD="+e.M.Password, "DATASET="+ds, "ORGUNIT="+e.M.Facility.ID,
			"PERIOD="+pe, "ROW_A="+ag.NumberLabel, "ROW_B="+ag.IntegerLabel, "COLUMN="+fmt.Sprint(ag.Column),
			"VALUE_A="+va, "VALUE_B="+vb, "OUT="+out, "PROFILE="+filepath.Join(t.TempDir(), "profile"))
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
		t.Logf("%s steps: %v", name, res.Steps)
		if res.Error != "" {
			t.Fatalf("%s driver: %s (screenshots in %s)", name, res.Error, out)
		}
		for _, w := range res.Writes {
			t.Logf("%s write: %s %s %s %s → %d %s", name, w.User, w.Phase, w.Method, w.Path, w.Status, w.Body)
			if w.Phase == "offline" && w.Status == 503 {
				t.Errorf("offline write answered 503: the app queues it in the browser (%s %s)", w.Method, w.Path)
			}
		}
		if got := res.Steps["B offline: row A"]; got != va {
			t.Errorf("%s: the clerk offline sees row A %v, want the nurse's online value %s", name, got, va)
		}
		if got := res.Steps["B offline: row B"]; got != vb {
			t.Errorf("%s: the clerk offline sees row B %v, want the nurse's offline value %s", name, got, vb)
		}
		for _, c := range []struct{ de, want string }{{ag.Number, va}, {ag.Integer, vb}} {
			val, by, _ := e.CentralDataValue(ds, pe, c.de, ag.COC, ag.DefaultAOC)
			if val != c.want || by != nurse.User {
				t.Errorf("%s: %s on central: %q by %q, want %s by %s", name, c.de, val, by, c.want, nurse.User)
			}
		}
		return res, out, va, vb
	}
	res, _, _, _ := run(ag.DataSet, "plain")
	if st, _ := res.Steps["complete offline: status"].(float64); st != 200 {
		t.Errorf("completing offline: %v, want 200 (queued)", res.Steps["complete offline: status"])
	}
	if ag.ValidDataSet != "" {
		// Mandatory validation: the app runs validation before completing; the hub
		// cannot offline, so the app refuses to complete ("Couldn't validate the form,
		// please try again later") and sends nothing. The hub's own refusal (409) is
		// the guard for other clients (TestAggregateCompletionOffline).
		res, out, _, _ := run(ag.ValidDataSet, "validated")
		if st, ok := res.Steps["complete offline: status"].(string); !ok || !strings.HasPrefix(st, "no request") {
			t.Errorf("completing offline with mandatory validation: %v, want no completion request", res.Steps["complete offline: status"])
		}
		if shown := fmt.Sprint(res.Steps["complete offline: shown"]); !strings.Contains(shown, "Couldn't validate the form") {
			t.Errorf("the app showed %s (screenshot %s)", shown, filepath.Join(out, "2-complete-offline.png"))
		}
	}
	t.Logf("screenshots and results in %s", base)
}

type aggClientResult struct {
	Steps  map[string]any `json:"steps"`
	Writes []struct {
		User, Phase, Method, Path, Body string
		Status                          int
	} `json:"writes"`
	Errors []string `json:"errors"`
	Error  string   `json:"error"`
}
