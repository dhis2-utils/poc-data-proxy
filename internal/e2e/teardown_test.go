package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dhis2/facility-hub/internal/app"
)

// Windows will not delete an open file, so a test's temporary folder can be removed
// only once its hub has closed the store and the request log: each test's hub is
// stopped when the test ends (the Windows CI run, 2026-10-20).
func TestHubClosedWhenTheTestEnds(t *testing.T) {
	var a *app.App
	var dir string
	t.Run("a test", func(t *testing.T) {
		e := newEnv(t)
		e.setup()
		a, dir = e.app, e.dir
	})
	if err := a.Hub.Store.DB().Ping(); err == nil {
		t.Error("the store is still open after the test")
	}
	if _, err := os.Stat(filepath.Join(dir, "requests.log")); err == nil {
		t.Error("the test's folder was not removed")
	}
	if a.Hub.ReqLog.Opened() {
		t.Error("the request log is still open after the test")
	}
}
