package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func startFixture(t *testing.T, httpsPort string) http.Handler {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte(testCA), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Start{DataDir: dir, HTTPSPort: httpsPort,
		Allowed: func(h string) bool {
			return strings.HasPrefix(h, "dhis2.local") || strings.HasPrefix(h, "192.168.1.20")
		}}
}

func get(h http.Handler, host, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://"+host+path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Every plain-HTTP path answers the start page, which moves on to the same name and
// path over HTTPS, shows the steps and the authority's fingerprint.
func TestStartPageMovesToHTTPS(t *testing.T) {
	for _, c := range []struct{ port, host, path, want string }{
		{"443", "dhis2.local", "/dhis-web-capture/index.html?x=1", "https://dhis2.local/dhis-web-capture/index.html?x=1"},
		{"8443", "dhis2.local:80", "/", "https://dhis2.local:8443/"},
		{"443", "192.168.1.20", "/api/me", "https://192.168.1.20/api/me"},
	} {
		w := get(startFixture(t, c.port), c.host, c.path)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, c.want) {
			t.Errorf("%s%s: %d, no %s in the page", c.host, c.path, w.Code, c.want)
		}
		for _, s := range []string{"/hub/ca.crt", testFingerprint, "Certificate Trust Settings", "Install from device storage"} {
			if !strings.Contains(body, s) {
				t.Errorf("page lacks %q", s)
			}
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Error("start page may be cached")
		}
	}
}

func TestStartServesAuthority(t *testing.T) {
	w := get(startFixture(t, "443"), "dhis2.local", "/hub/ca.crt")
	if w.Code != 200 || w.Body.String() != testCA || w.Header().Get("Content-Type") != "application/x-x509-ca-cert" {
		t.Fatalf("%d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), ".crt") {
		t.Error("no file name: Android needs the file saved to install it")
	}
}

// Other names are refused, as on HTTPS (DNS rebinding).
func TestStartRefusesOtherHosts(t *testing.T) {
	if w := get(startFixture(t, "443"), "evil.example", "/"); w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("%d", w.Code)
	}
}

const testCA = `-----BEGIN CERTIFICATE-----
MIIBeTCCAR+gAwIBAgIUJ6m1o5S6p1qvEjo1rG2Y0f9g0a8wCgYIKoZIzj0EAwIw
EjEQMA4GA1UEAwwHdGVzdCBjYTAeFw0yNjEwMTkwMDAwMDBaFw0zNjEwMTkwMDAw
MDBaMBIxEDAOBgNVBAMMB3Rlc3QgY2EwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNC
AAQ=
-----END CERTIFICATE-----
`

const testFingerprint = "57:EE:9F:08:6F:8D:59:53:63:46:D9:B1:7E:6D:6C:F2:10:06:00:E9:E4:39:47:25:A3:83:1A:E6:95:54:63:3A"
