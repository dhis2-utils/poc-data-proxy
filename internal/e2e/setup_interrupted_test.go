package e2e

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// R-09: a set-up that fails after the hub created its data file, before any key slot
// is written, left a file encrypted with a key held only in memory. After a restart
// every set-up failed with "file is not a database". It must work again.
func TestSetupAfterInterruptedSetup(t *testing.T) {
	e := newEnv(t)
	var fail atomic.Bool
	fail.Store(true)
	e.cSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() && r.URL.Path == "/api/programs" && strings.HasPrefix(r.Header.Get("Authorization"), "ApiToken ") {
			http.Error(w, "boom", 500) // the hub's catalog read during set-up
			return
		}
		e.central.ServeHTTP(w, r)
	})
	c := e.web("hub_admin")
	resp, err := c.postForm("/hub/setup", url.Values{"step": {"login"}, "central_url": {e.cSrv.URL},
		"username": {"hub_admin"}, "password": {fc.Password}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !e.app.Hub.Store.Unlocked() {
		t.Fatal("set-up failed before it created the data file; the test does not reach the case")
	}
	fail.Store(false)
	e.stop()
	e.start()
	e.setup()
}
