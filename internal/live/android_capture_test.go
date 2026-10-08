package live

import (
	"fmt"
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

	refused := map[string]int{}
	for _, r := range e.Requests() {
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
