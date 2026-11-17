package adversarial

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// During an outage an Android incremental sync gets the people written on the hub in
// the day before its watermark, not everyone written since the outage began. Before,
// the hub counted objects written on it as updated at any time (Index.updatedMatch,
// changedTEs), and in TestWeekOffline (internal/perf; docs/PERFORMANCE.md, "A week
// offline at HC IV size") the people a sync received grew through the week up to the
// device's limit per program, and the sync's time with them. An object written on the
// hub now counts as updated by its hub write time, with the day's margin central's
// times get (updatedFloor), for a hub clock that drifted from central's.
func TestWeekAndroidSyncGetsTheLastDay(t *testing.T) {
	e := tk.Start(t)
	e.Setup()
	a := e.Android(tk.RoleNurse)
	a.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	e.Offline()
	p := e.M.Primary()
	register := func(name string) string {
		pr := e.M.NewPerson(p, name)
		if st, b := a.Do("POST", tk.SyncImport, pr.Payload); st != 200 {
			t.Fatalf("offline registration: %d %.300s", st, b)
		}
		return pr.TE
	}
	var early []string
	for range 20 {
		early = append(early, register("Zqwk"+tk.UID()[:5]))
	}
	recent := register("Zqwkrecent")
	// The outage passes: the first 20 written two days ago, one three hours ago.
	h := e.App.Hub
	db := h.Store.DB()
	back := func(uid string, d time.Duration) {
		if _, err := db.Exec(`UPDATE objects SET updated_at = ? WHERE uid = ? OR te_uid = ?`, h.CentralTime(time.Now().Add(-d)), uid, uid); err != nil {
			t.Fatal(err)
		}
	}
	for _, uid := range early {
		back(uid, 48*time.Hour)
	}
	back(recent, 3*time.Hour)
	watermark := h.CentralTime(time.Now().Add(-time.Hour))
	last := register("Zqwklast")
	// Every page, as Android reads them: a facility with people updated on central in
	// the last day (Sierra Leone's) fills more than the first.
	var b []byte
	for page := 1; ; page++ {
		q := url.Values{"program": {p.ID}, "orgUnit": {e.M.Facility.ID}, "orgUnitMode": {"DESCENDANTS"},
			"updatedAfter": {watermark}, "pageSize": {"50"}, "page": {strconv.Itoa(page)}, "fields": {"trackedEntity"}}
		st, pb := a.Do("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil)
		if st != 200 {
			t.Fatalf("incremental sync, page %d: %d %.300s", page, st, pb)
		}
		b = append(b, pb...)
		var r struct {
			Pager struct{ NextPage string } `json:"pager"`
		}
		if json.Unmarshal(pb, &r); r.Pager.NextPage == "" || page >= 100 {
			break
		}
	}
	if !bytes.Contains(b, []byte(recent)) || !bytes.Contains(b, []byte(last)) {
		t.Errorf("an incremental sync did not get the 2 people written in the day before its watermark: %.500s", b)
	}
	resent := 0
	for _, uid := range early {
		if bytes.Contains(b, []byte(uid)) {
			resent++
		}
	}
	if resent > 0 {
		t.Errorf("an incremental sync got %d of the 20 people written two days before its watermark, want none", resent)
	}
}
