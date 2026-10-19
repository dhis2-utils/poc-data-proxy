package e2e

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/stick"
)

// Windows (usb_dir: auto): the hub keeps its copy on a removable drive a staff member
// chose, finds it again by its folder, and reports it missing rather than use another.
func TestStickAutoAsksThenKeepsCopy(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	plugged := []stick.Drive{{Root: d1, Label: "STAFF", Serial: 1}, {Root: d2, Label: "KINGSTON", Serial: 2}}
	old := stick.Removable
	stick.Removable = func() []stick.Drive { return plugged }
	t.Cleanup(func() { stick.Removable = old })

	e := &env{t: t, central: fc.New(), dir: t.TempDir()}
	e.cSrv = httptest.NewServer(e.central)
	t.Cleanup(e.cSrv.Close)
	e.cfgHook = func(c *config.Config) { c.USBDir, c.USBRequireMount = "auto", true }
	e.start()
	e.setup()
	a := e.web("hub_admin")
	a.login()
	e.app.Stick.Tick(context.Background())
	st := e.app.Stick.Status()
	if st.Present || len(st.Ask) != 2 {
		t.Fatalf("status before a choice: %+v", st)
	}
	for _, d := range []string{d1, d2} {
		if entries, _ := os.ReadDir(d); len(entries) != 0 {
			t.Fatalf("wrote to %s before anyone chose it", d)
		}
	}
	if _, page := a.do("GET", "/hub/ui", nil); !strings.Contains(string(page), "Keep the hub's copy on") {
		t.Fatal("the admin page does not ask which drive")
	}
	resp, err := a.postForm("/hub/ui/usb", url.Values{"root": {d2}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	e.app.Stick.Tick(context.Background())
	id := e.app.Hub.HubID()
	if _, err := os.Stat(filepath.Join(d2, "facility-hub", id, "keys.db")); err != nil {
		t.Fatalf("no copy on the chosen drive: %v (status %+v)", err, e.app.Stick.Status())
	}
	plugged = plugged[:1] // the chosen stick is pulled out
	e.app.Stick.Tick(context.Background())
	if st := e.app.Stick.Status(); st.Present || !st.Missing {
		t.Fatalf("status with the stick gone: %+v", st)
	}
	if entries, _ := os.ReadDir(d1); len(entries) != 0 {
		t.Fatal("the hub moved its copy to another drive")
	}
}
