package central

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Review M6: the error text names the status and code, never central's body.
func TestHTTPErrorOmitsBody(t *testing.T) {
	e := &HTTPError{Status: 409, Body: []byte(`{"errorCode":"E1064","message":"value 'Amina' already exists"}`)}
	if got := e.Error(); got != "central returned 409 (E1064)" {
		t.Fatal(got)
	}
	e = &HTTPError{Status: 400, Body: []byte(`Bad request: Amina`)}
	if got := e.Error(); strings.Contains(got, "Amina") {
		t.Fatal(got)
	}
}

// Review 2 F9: a captive portal does not make the hub think central is reachable.
func TestProbeTellsCentralFromPortal(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dhis-web-login/", http.StatusFound) // 2.42, anonymous
	}))
	defer central.Close()
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://portal.example/login", http.StatusFound)
	}))
	defer portal.Close()
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>Welcome to hotel Wi-Fi</html>"))
	}))
	defer html.Close()
	for url, want := range map[string]bool{central.URL: true, portal.URL: false, html.URL: false} {
		m := NewMonitor(time.Second, time.Second)
		if got := m.Probe(context.Background(), url); got != want {
			t.Errorf("%s: got %v, want %v", url, got, want)
		}
	}
}
