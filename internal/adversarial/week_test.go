package adversarial

import (
	"bytes"
	"net/url"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// During an outage every Android incremental sync receives every person whose
// records were written on the hub since the outage began, whatever its updatedAfter:
// the hub counts objects written on it as updated at any time (Index.updatedMatch,
// changedTEs), because a device's watermark may come from central's clock. Measured in
// TestWeekOffline (internal/perf; docs/PERFORMANCE.md, "A week offline at HC IV
// size"): the people a sync receives grow through the week, up to the device's limit
// per program, and the sync's time with them. Proposed: count an object written on the
// hub as updated after updatedAfter when its hub write time is at most a day older
// (the floor central's times get, updatedFloor), so a sync gets the last day's work,
// not the whole outage's.
func TestWeekAndroidSyncResendsTheOutage(t *testing.T) {
	finding(t, "during an outage each Android incremental sync gets every person written on the hub since the outage began")
	e := tk.Start(t)
	e.Setup()
	a := e.Android(tk.RoleNurse)
	a.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	e.Offline()
	p := e.M.Primary()
	register := func(name string) {
		pr := e.M.NewPerson(p, name)
		if st, b := a.Do("POST", tk.SyncImport, pr.Payload); st != 200 {
			t.Fatalf("offline registration: %d %.300s", st, b)
		}
	}
	for range 20 {
		register("Zqwk" + tk.UID()[:5])
	}
	time.Sleep(1100 * time.Millisecond)
	watermark := time.Now().UTC().Format("2006-01-02T15:04:05.000")
	time.Sleep(1100 * time.Millisecond)
	register("Zqwklast")
	q := url.Values{"program": {p.ID}, "orgUnit": {e.M.Facility.ID}, "orgUnitMode": {"DESCENDANTS"},
		"updatedAfter": {watermark}, "pageSize": {"50"}, "page": {"1"}, "fields": {"trackedEntity"}}
	st, b := a.Do("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil)
	if st != 200 {
		t.Fatalf("incremental sync: %d %.300s", st, b)
	}
	if n := bytes.Count(b, []byte(`"trackedEntity":`)); n != 1 {
		t.Errorf("an incremental sync after the 21st offline registration got %d people, want the 1 registered since its watermark", n)
	}
}
