package app

import "testing"

// Paths that are not clean are refused before routing (review R-16).
func TestCleanPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/api/me": true, "/dhis-web-capture/": true, "/apps/capture": true, "/api/42/me.json": true,
		"//h/api/x.json": false, "/a/../api/me": false, "/api/./me": false, "/api//me": false, "/api/me//": false, "": false,
	} {
		if got := cleanPath(p); got != want {
			t.Errorf("cleanPath(%q) = %v, want %v", p, got, want)
		}
	}
}
