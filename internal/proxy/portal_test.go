package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// notCentral stays narrow: central's own files, pages and redirects under /api/ are
// central's (adversarial D7).
func TestNotCentralIsNarrow(t *testing.T) {
	bases := []string{"https://central.example.org/dhis"}
	for _, c := range []struct {
		path, accept, ct, loc string
		status                int
		portal                bool
	}{
		{"/api/programs/x", "", "text/html", "", 200, true},
		{"/api/42/me", "application/json", "text/html;charset=UTF-8", "", 200, true},
		{"/api/dataValues", "", "", "http://hotspot.example/login", 302, true},
		{"/api/me", "application/json", "application/json", "", 200, false},
		{"/api/apps/menu/index.html", "application/json", "text/html", "", 200, false},
		{"/api/documents/x/data", "text/html,application/xhtml+xml,*/*;q=0.8", "text/html", "", 200, false},
		{"/api/reports/x/data.html", "application/json, text/plain, */*", "text/html", "", 200, false},
		{"/api/fileResources/x/data", "", "image/png", "", 200, false},
		{"/api/me", "", "", "https://central.example.org/dhis/dhis-web-login/", 302, false},
		{"/api/me", "", "", "/dhis-web-login/", 302, false},
		{"/dhis-web-capture/index.html", "", "text/html", "", 200, false},
		{"/apps/capture", "", "", "http://hotspot.example/login", 302, false},
	} {
		r := httptest.NewRequest("GET", c.path, nil)
		hdr := http.Header{}
		if c.accept != "" {
			hdr.Set("Accept", c.accept)
		}
		resp := &http.Response{StatusCode: c.status, Header: http.Header{}}
		if c.ct != "" {
			resp.Header.Set("Content-Type", c.ct)
		}
		if c.loc != "" {
			resp.Header.Set("Location", c.loc)
		}
		if got := notCentral(r, hdr, resp, bases) != ""; got != c.portal {
			t.Errorf("%s (Accept %q) answered %d %s %s: taken for a portal %v, want %v", c.path, c.accept, c.status, c.ct, c.loc, got, c.portal)
		}
	}
}

// Client X-Forwarded-* and Forwarded headers do not reach central (review R-17):
// central writes its base URL into app pages from them.
func TestForwardHeaderDropsForwarded(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-Port", "9999")
	h.Set("X-Forwarded-Host", "evil.example")
	h.Set("X-Forwarded-Proto", "http")
	h.Set("X-Forwarded-For", "10.0.0.1")
	h.Set("Forwarded", "host=evil.example")
	h["x-forwarded-prefix"] = []string{"/x"} // not canonical
	h.Set("Authorization", "Bearer x")
	h.Set("Accept", "application/json")
	h.Set("If-None-Match", `"x"`)
	got := forwardHeader(h)
	if len(got) != 2 || got.Get("Accept") != "application/json" || got.Get("If-None-Match") != `"x"` {
		t.Errorf("forwarded %v, want only Accept and If-None-Match", got)
	}
	if h.Get("X-Forwarded-Port") == "" {
		t.Error("the client's header was changed")
	}
}

// The apps menu is per user, not an app file anyone may be served (review R-16).
func TestAppsMenuIsNotShared(t *testing.T) {
	if isStatic("/api/apps/menu.json") || shared("/api/apps/menu.json") {
		t.Error("/api/apps/menu.json is classed as shared by all")
	}
	if !isStatic("/api/apps/capture/index.html") {
		t.Error("an installed app's file is no longer static")
	}
}
