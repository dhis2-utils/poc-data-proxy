package e2e

import (
	"strings"
	"testing"
)

// A browser without an app's file gets the hub's copy when central says it is current
// (a 304), not a fresh download through the uplink; an updated file is downloaded, and
// the hub's copy follows it. docs/PERFORMANCE.md, "A slow uplink".
func TestAppFileFromHubCopyWhenCurrent(t *testing.T) {
	e := newEnv(t)
	const path = "/dhis-web-capture/assets/index-AbCdEf12.js"
	e.central.SetAppFile(path, strings.Repeat("bundle v1;", 1000))
	e.setup()
	nurse, clerk := e.web("nurse_a"), e.web("clerk_c")
	nurse.login()
	clerk.login()
	if st, b := nurse.do("GET", path, nil); st != 200 || !strings.HasPrefix(string(b), "bundle v1;") {
		t.Fatalf("first download: %d %.40s", st, b)
	}
	if n := e.central.NotModifiedCount(); n != 0 {
		t.Fatalf("%d 304s before the hub held a copy", n)
	}
	// The clerk's browser has nothing: the hub asks central about its copy.
	if st, b := clerk.do("GET", path, nil); st != 200 || !strings.HasPrefix(string(b), "bundle v1;") || len(b) != 10000 {
		t.Fatalf("second browser: %d %d bytes", st, len(b))
	}
	if n := e.central.NotModifiedCount(); n != 1 {
		t.Fatalf("central gave %d 304s, want 1: the hub did not revalidate its copy", n)
	}
	// An app update changes the file: central answers in full and the hub keeps the new copy.
	e.central.SetAppFile(path, strings.Repeat("bundle v2;", 1000))
	if st, b := clerk.do("GET", path, nil); st != 200 || !strings.HasPrefix(string(b), "bundle v2;") {
		t.Fatalf("after the update: %d %.40s", st, b)
	}
	if st, b := nurse.do("GET", path, nil); st != 200 || !strings.HasPrefix(string(b), "bundle v2;") {
		t.Fatalf("after the update, from the new copy: %d %.40s", st, b)
	}
	if n := e.central.NotModifiedCount(); n != 2 {
		t.Fatalf("central gave %d 304s, want 2", n)
	}
	// A browser asking conditionally itself is passed through, as before.
	if st, _ := nurse.getWith(path, map[string]string{"If-None-Match": "\"stale\""}); st != 200 {
		t.Fatalf("a browser's own condition: %d", st)
	}
}
