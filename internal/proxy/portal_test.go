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
