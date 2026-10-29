package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestOfflineScreens measures what staff can open offline in the real Capture and
// Data Entry apps (clients/offline_screens.py), each in a fresh browser: A opens every
// screen online; B only logs in online; C opens Capture's front page online once (the
// set-up routine before 2026-10-29). Offline, each opens every screen. The hub loads
// the apps' requests for B and C at their login (proxy/replay.go), so both must open
// whatever A can offline without "needs connection". The result, with the hub's
// misses by endpoint per user, is in offline-screens/result.json next to HUB_REPORT.
// Set HUB_CLIENTS=1 to run it (it needs a browser).
func TestOfflineScreens(t *testing.T) {
	if os.Getenv("HUB_CLIENTS") == "" {
		t.Skip("set HUB_CLIENTS=1 to drive the real apps")
	}
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST)")
	}
	m := e.M
	e.Setup()
	e.Sync()
	out := t.TempDir()
	if p := os.Getenv("HUB_REPORT"); p != "" {
		out = filepath.Join(filepath.Dir(p), "offline-screens")
		os.MkdirAll(out, 0o755)
	}
	userA := m.Users[tk.RoleNurse].Username
	userB, userC := os.Getenv("SCREENS_USER_B"), os.Getenv("SCREENS_USER_C")
	if userB == "" {
		userB = "syn_nurse_twin"
	}
	if userC == "" {
		userC = m.Users[tk.RoleRestricted].Username
	}
	var progs, events []string
	for _, p := range m.Programs {
		progs = append(progs, p.ID)
	}
	for _, p := range m.EventPrograms {
		if len(p.AOCs) == 0 { // a URL cannot choose the attribute option
			events = append(events, p.ID)
		}
	}
	var sets []string
	if ag := m.Aggregate; ag != nil {
		pe := time.Now().AddDate(0, -1, 0).Format("200601")
		for _, ds := range []string{ag.DataSet, ag.ValidDataSet} { // not the donor set: as above
			if ds != "" {
				sets = append(sets, ds+":"+pe)
			}
		}
	}
	enr := enrollmentAt(t, e, m.Primary(), m.Facility.ID)
	run := func(user, mode, tag string) map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute) // a hung browser
		defer cancel()
		cmd := exec.CommandContext(ctx, "python3", "-I", "../../clients/offline_screens.py")
		cmd.Env = append(os.Environ(), "HUB_URL="+e.Srv.URL, "USER="+user, "PASSWORD="+m.Password,
			"ORG_UNIT="+m.Facility.ID, "OUT="+out, "TAG="+tag, "MODE="+mode, "PROGRAMS="+strings.Join(progs, ","),
			"EVENT_PROGRAMS="+strings.Join(events, ","), "ENROLLMENT="+m.Primary().ID+":"+enr,
			"DATASETS="+strings.Join(sets, ","))
		cmd.Stderr = os.Stderr
		// Playwright's driver and browser go with it.
		cmd.Cancel = func() error {
			exec.Command("pkill", "-TERM", "-P", fmt.Sprint(cmd.Process.Pid)).Run()
			return cmd.Process.Kill()
		}
		if err := cmd.Run(); err != nil {
			t.Logf("offline_screens.py %s: %v (its result so far is used)", tag, err)
		}
		var r map[string]any
		b, err := os.ReadFile(filepath.Join(out, tag+".json"))
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		return r
	}
	misses := func() map[string]int {
		top, _ := e.App.Hub.ReqLog.OfflineMisses(10000)
		out := map[string]int{}
		for _, x := range top {
			out[x["request"].(string)] = x["count"].(int)
		}
		return out
	}
	cacheRows := func() map[string]string {
		rows, err := e.App.Hub.Store.DB().Query(`SELECT u.username, count(*), sum(c.replayed), sum(length(c.body_enc))
 FROM http_cache c JOIN users u ON u.id=c.user_id GROUP BY u.username`)
		if err != nil {
			t.Logf("cache rows: %v", err)
			return nil
		}
		defer rows.Close()
		res := map[string]string{}
		for rows.Next() {
			var u string
			var n, rep, bytes int
			rows.Scan(&u, &n, &rep, &bytes)
			res[u] = fmt.Sprintf("%d entries, %d loaded at login, %d kB", n, rep, bytes/1024)
		}
		return res
	}

	res := map[string]any{"users": map[string]string{"A": userA, "B": userB, "C": userC}}
	res["A online"] = run(userA, "screens", "A-online")
	start := time.Now()
	res["B online"] = run(userB, "login", "B-online")
	res["C online"] = run(userC, "front", "C-online")
	e.App.Proxy.WaitReplays()
	res["replays (s, after B's login)"] = time.Since(start).Seconds()
	res["cache"] = cacheRows()
	res["loaded for B"] = loaded(t, e, userB)
	e.Sync() // their first pull, which their login starts

	if err := e.SetUplink(false); err != nil {
		t.Fatal(err)
	}
	missesBy := map[string]map[string]int{}
	for _, u := range []struct{ key, user string }{{"A", userA}, {"B", userB}, {"C", userC}} {
		before := misses()
		res[u.key+" offline"] = run(u.user, "screens", u.key+"-offline")
		d := map[string]int{}
		for k, n := range misses() {
			if n > before[k] {
				d[k] = n - before[k]
			}
		}
		missesBy[u.key] = d
	}
	if err := e.SetUplink(true); err != nil {
		t.Fatal(err)
	}
	res["hub misses offline"] = missesBy
	b, _ := json.MarshalIndent(res, "", "  ")
	os.WriteFile(filepath.Join(out, "result.json"), b, 0o644)

	// A screen A opens offline, B and C open too, and the hub answers every read.
	screens := func(key string) map[string]any {
		s, _ := res[key+" offline"].(map[string]any)["screens"].(map[string]any)
		return s
	}
	for name, sa := range screens("A") {
		shownA, _ := sa.(map[string]any)["shown"].(float64)
		t.Logf("%-28s A %3.0f  B %3.0f  C %3.0f", name, shownA, shown(screens("B"), name), shown(screens("C"), name))
		for _, k := range []string{"B", "C"} {
			if shownA > 0 && shown(screens(k), name) <= 0 {
				t.Errorf("%s cannot open %s offline (A can)", k, name)
			}
		}
	}
	for k, d := range missesBy {
		total := 0
		for _, n := range d {
			total += n
		}
		t.Logf("%s: %d misses offline: %v", k, total, d)
		if k != "A" && total > 0 {
			t.Errorf("%s: %d reads the hub could not answer offline", k, total)
		}
	}
}

func shown(screens map[string]any, name string) float64 {
	s, _ := screens[name].(map[string]any)
	n, _ := s["shown"].(float64)
	return n
}

// loaded lists, by endpoint, what the hub loaded for user at their login.
func loaded(t *testing.T, e *tk.Env, user string) map[string]int {
	rows, err := e.App.Hub.Store.DB().Query(`SELECT c.url FROM http_cache c JOIN users u ON u.id=c.user_id
 WHERE u.username=? AND c.replayed=1`, user)
	if err != nil {
		t.Logf("loaded: %v", err)
		return nil
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		p, q, _ := strings.Cut(s, "?")
		k := core.Template(p)
		if v, err := url.ParseQuery(q); err == nil && q != "" {
			var names []string
			for n := range v {
				names = append(names, n)
			}
			sort.Strings(names)
			k += " ?" + strings.Join(names, ",")
		}
		out[k]++
	}
	return out
}
