package e2e

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// Logging out through the hub while online: the app's requests still in flight are
// cancelled as the browser leaves the page. A request its client gave up on is not
// central being unreachable: the hub stays online and passes the login app through
// (in the field: "needs connection: /dhis-web-login/ was not cached while online").
func TestLogoutOnlineReachesLoginApp(t *testing.T) {
	e := newEnv(t)
	e.setup()
	c := e.web("nurse_a")
	c.login()

	// A page load the browser abandons while central is still answering.
	e.central.StallResponse = map[string]int{"GET /apps/capture": 1}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/apps/capture", nil)
	req.Header.Set("Accept", "text/html")
	if resp, err := c.http.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("the abandoned request was answered: %d", resp.StatusCode)
	}
	time.Sleep(100 * time.Millisecond) // the hub notices its client went away

	next := "/dhis-web-commons-security/logout.action"
	for range 3 {
		st, loc := pageLoad(t, c, next)
		if st != http.StatusFound {
			if next != "/dhis-web-login/" || st != 200 {
				t.Fatalf("GET %s: %d, want the login app from central", next, st)
			}
			break
		}
		u, err := url.Parse(loc)
		if err != nil {
			t.Fatal(err)
		}
		next = u.Path
	}
	if next != "/dhis-web-login/" {
		t.Fatalf("logout ended at %s, not the login app", next)
	}
	if !e.app.Hub.Online() {
		t.Fatal("the hub went offline: a cancelled request was taken for central unreachable")
	}
}

// pageLoad is a browser's page load: status and Location.
func pageLoad(t *testing.T, c *client, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", c.e.srv.URL+path, nil)
	req.Header.Set("Accept", "text/html")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}
