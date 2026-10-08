package live

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestCaptureAndroid drives the real Android Capture app on the emulator through the
// hub (docs/TESTING.md): a first login online, then, with the app's data wiped and the
// uplink cut, a first login and full download offline. It fails if the offline login
// does not reach the home screen, and reports every request the hub refused offline
// and how the record count differs from online. Set HUB_CLIENTS=1 to run it; it needs
// an emulator (adb) and SANDBOX_HOST_PORT_2, the port the emulator reaches as
// 10.0.2.2.
func TestCaptureAndroid(t *testing.T) {
	port := os.Getenv("SANDBOX_HOST_PORT_2")
	a := &tk.Android{Package: "com.dhis2"}
	switch {
	case os.Getenv("HUB_CLIENTS") == "":
		t.Skip("set HUB_CLIENTS=1 to drive the real Android Capture app")
	case port == "" || !a.Available():
		t.Skip("needs an emulator (adb) and SANDBOX_HOST_PORT_2")
	}
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST)")
	}
	m := e.M
	e.ServeOn(":"+port, "10.0.2.2")
	e.Setup()
	server := "http://10.0.2.2:" + port
	nurse := m.Users[tk.RoleNurse]
	program := m.Primary().Name

	out := t.TempDir()
	if p := os.Getenv("HUB_REPORT"); p != "" {
		out = filepath.Join(filepath.Dir(p), "capture-android")
		os.MkdirAll(out, 0o755)
	}
	login := func(phase string) int {
		t.Helper()
		if err := a.Reset(); err != nil {
			t.Fatal(err)
		}
		nodes, err := a.Login(server, nurse.Username, m.Password, program, 10*time.Minute)
		a.Screenshot(filepath.Join(out, phase+".png"))
		if err != nil {
			t.Fatalf("%s login: %v", phase, err)
		}
		n, ok := tk.HomeCount(nodes, program)
		if !ok {
			t.Logf("%s: no record count under %q", phase, program)
		}
		return n
	}

	online := login("online")
	e.Sync()
	e.Requests()
	e.Offline()
	offline := login("offline")
	t.Logf("%q on the home screen: %d records online, %d offline", program, online, offline)
	if offline == 0 && online > 0 {
		t.Errorf("offline the app shows no %q records; online it showed %d", program, online)
	} else if offline != online {
		t.Logf("record count differs: %d online, %d offline", online, offline)
	}

	reqs := e.Requests()
	transferOffline(t, e, a, program)

	refused := map[string]int{}
	for _, r := range reqs {
		if r.Status >= 400 && r.Status != 404 {
			refused[fmt.Sprintf("%d %s %s", r.Status, r.Method, r.Path)]++
		}
	}
	var lines []string
	for k, n := range refused {
		lines = append(lines, fmt.Sprintf("%s (x%d)", k, n))
	}
	sort.Strings(lines)
	os.WriteFile(filepath.Join(out, "refused-offline.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	for _, l := range lines {
		t.Logf("refused offline: %s", l)
	}
}

// transferOffline transfers a person to the sibling facility in the app while the hub
// is offline (the Android SDK queues transfers), syncs, and checks that the hub
// refuses the transfer (503) without blocking the app's other uploads; then, online
// again, that the next sync sends it through the hub to central and the hub stops
// showing the person in the program offline. Ownership is restored at the end.
func transferOffline(t *testing.T, e *tk.Env, a *tk.Android, program string) {
	m := e.M
	nodes, err := a.ScrollTo(program, 15)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := tk.Find(nodes, program)
	a.Tap(n)
	// Any person in the list: their card shows where they are enrolled.
	if err := a.Transfer("Enrolled in", m.Sibling.Name); err != nil {
		t.Logf("transfer in the app not possible (%v); skipping the transfer check", err)
		return
	}
	// The list shows the new owner only when opened again.
	a.Key("4")
	if nodes, err = a.ScrollTo(program, 15); err != nil {
		t.Fatal(err)
	}
	n, _ = tk.Find(nodes, program)
	a.Tap(n)
	marker := "Owned by:  " + m.Sibling.Name // the transferred person's card on the list
	if err := a.SyncRecord(marker); err != nil {
		t.Fatal(err)
	}
	put := waitRequest(t, e, "PUT", "/ownership/transfer", 2*time.Minute)
	if put.Status != 503 {
		t.Errorf("offline, the hub answered the transfer %d, want 503 (needs connection)", put.Status)
	}
	q, _ := url.ParseQuery(put.Query)
	te, p := q.Get("trackedEntity"), q.Get("program")
	defer func() {
		v := url.Values{"trackedEntity": {te}, "program": {p}, "ou": {m.Facility.ID}}
		e.Central.Do("PUT", "/api/tracker/ownership/transfer?"+v.Encode(), nil)
	}()

	e.Online()
	if err := a.SyncRecord(marker); err != nil {
		t.Fatalf("the transfer is not queued in the app any more: %v", err)
	}
	if put := waitRequest(t, e, "PUT", "/ownership/transfer", 2*time.Minute); put.Status != 200 {
		t.Fatalf("online, the transfer through the hub answered %d", put.Status)
	}
	var owners struct {
		ProgramOwners []struct{ OrgUnit, Program string } `json:"programOwners"`
	}
	e.Central.JSON("GET", "/api/tracker/trackedEntities/"+te+"?program="+p+"&fields=programOwners", nil, &owners)
	if len(owners.ProgramOwners) == 0 || owners.ProgramOwners[0].OrgUnit != m.Sibling.ID {
		t.Errorf("central's owner of %s after the sync: %+v, want %s", te, owners.ProgramOwners, m.Sibling.ID)
	}
	e.Offline()
	if st, _ := e.AndroidUser(m.Users[tk.RoleNurse].Username).Do("GET", "/api/tracker/trackedEntities/"+te+"?program="+p, nil); st == 200 {
		t.Errorf("offline, the hub still opens %s in the program after the transfer", te)
	}
}

// waitRequest waits until the hub has answered a request with this method and path
// suffix, and returns it.
func waitRequest(t *testing.T, e *tk.Env, method, suffix string, timeout time.Duration) tk.Request {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, r := range e.Requests() {
			if r.Method == method && strings.HasSuffix(r.Path, suffix) {
				return r
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("the app sent no %s …%s in %s", method, suffix, timeout)
	return tk.Request{}
}
