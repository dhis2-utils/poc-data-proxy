package e2e

import (
	"net/url"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// An incremental pull starts 5 minutes before the newest update time the last pull
// saw, for records stamped before it and committed after (review R-34). A record
// listed again with the time the hub holds is not fetched again.
func TestIncrementalPullOverlaps(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("OverTe00001", "OverEn00001", "Overlap", "NAT-0101", nil)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	e.sync()
	u, _ := e.app.Hub.Store.UserByName("nurse_a")
	last, _ := e.app.Hub.Store.PullState(u.ID, fc.Program)
	at, err := time.Parse(tracker.DHIS2Layout, last)
	if err != nil {
		t.Fatalf("pull state %q: %v", last, err)
	}
	want := at.Add(-5 * time.Minute).Format(tracker.DHIS2Layout)
	fetched, lists := e.central.FetchCount("OverTe00001"), len(e.central.TEListQueries())
	e.sync()
	asked := false
	for _, raw := range e.central.TEListQueries()[lists:] {
		q, _ := url.ParseQuery(raw)
		if q.Get("program") != fc.Program || q.Get("trackedEntities") != "" {
			continue
		}
		asked = true
		if got := q.Get("updatedAfter"); got != want {
			t.Errorf("updatedAfter=%q, want %q (the last pull's %q less 5 minutes)", got, want, last)
		}
	}
	if !asked {
		t.Fatal("no incremental listing of the program")
	}
	if n := e.central.FetchCount("OverTe00001"); n != fetched {
		t.Errorf("a record listed again with the same time was fetched again: %d -> %d", fetched, n)
	}
	if l, _ := e.app.Hub.Store.PullState(u.ID, fc.Program); l != last {
		t.Errorf("pull state moved from %q to %q with nothing new", last, l)
	}
}
