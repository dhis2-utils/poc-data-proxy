package live

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestReconnectBeforePush: when central is back but the outbox is not pushed yet, the
// hub keeps answering tracker requests itself. A person registered offline is still
// found, an event for them is queued behind the registration instead of failing on
// central, and after the push both are on central and requests pass through again.
func TestReconnectBeforePush(t *testing.T) {
	e := tk.Start(t)
	m := e.M
	p := m.Primary()
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()

	e.Offline()
	pr := m.NewPerson(p, "Zqrecon"+strings.ToLower(tk.UID()[:5]), p.Stages[0])
	nurse.Complete(p, pr)
	nurse.JSON("POST", tk.SyncImport, pr.Payload, 200)

	e.Online() // central is reachable; the outbox has not been pushed
	q := url.Values{"program": {p.ID}, "orgUnits": {m.Facility.ID}, "fields": {"trackedEntity"}, "trackedEntities": {pr.TE}}
	st, hdr, _ := nurse.DoHeader("GET", "/api/tracker/trackedEntities?"+q.Encode())
	if st != 200 || hdr.Get("X-Hub-Mode") != "offline" {
		t.Fatalf("before the push: %d, X-Hub-Mode %q; want the hub's own answer", st, hdr.Get("X-Hub-Mode"))
	}
	if got := uids(nurse.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, 200)["trackedEntities"], "trackedEntity"); !slices.Contains(got, pr.TE) {
		t.Fatalf("before the push, the person registered offline is not found: %v", got)
	}
	ev, payload := m.NewEvent(p, p.Stages[0], pr.Enrollment)
	nurse.JSON("POST", tk.SyncImport, payload, 200) // queued behind the registration

	e.Sync()
	if e.CentralObject("trackedEntities", pr.TE) == nil {
		t.Fatal("after the push the person is not on central")
	}
	if e.CentralObject("events", ev) == nil {
		t.Fatal("after the push the event is not on central")
	}
	if _, hdr, _ := nurse.DoHeader("GET", "/api/tracker/trackedEntities?"+q.Encode()); hdr.Get("X-Hub-Mode") != "" {
		t.Errorf("after the push the hub still answers itself (X-Hub-Mode %q)", hdr.Get("X-Hub-Mode"))
	}
}
