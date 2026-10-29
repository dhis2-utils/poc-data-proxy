package e2e

import (
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// The facility moves to another district on central (A12). The next access refresh
// (hourly) reads its new path, and offline the old district's officer is no longer
// taken as the facility's staff; once it moves back, they are again.
func TestFacilityMovesDistrict(t *testing.T) {
	e := newEnv(t)
	e.app.Hub.Cfg.AccessRefreshInterval = 0 // a refresh in every cycle
	e.central.MoveFacility("/root/" + fc.District + "/" + fc.Facility)
	e.setup()
	officer := e.web("district_e")
	officer.login()
	e.sync()
	listing := "/api/tracker/trackedEntities?orgUnits=" + fc.Facility + "&program=" + fc.Program
	e.offline()
	// The fake has no org unit tree, so central lists the officer nobody; what matters
	// here is that the hub takes them as facility staff (200) or not (403).
	if st, b := officer.do("GET", listing, nil); st != 200 {
		t.Fatalf("offline before the move, the district's officer: %d %.200s, want 200", st, b)
	}
	e.online()

	e.central.MoveFacility("/root/OtherDist01/" + fc.Facility)
	e.sync()
	if got, want := e.app.Hub.FacilityPath(), "/root/OtherDist01/"+fc.Facility; got != want {
		t.Errorf("the hub's facility path after the move: %q, want %q", got, want)
	}
	e.offline()
	if st, b := officer.do("GET", listing, nil); st != 403 {
		t.Errorf("offline after the move, the old district's officer: %d %.200s, want 403", st, b)
	}
	e.online()

	// Back under the district: the officer sees the facility again.
	e.central.MoveFacility("/root/" + fc.District + "/" + fc.Facility)
	e.sync()
	e.offline()
	if st, b := officer.do("GET", listing, nil); st != 200 {
		t.Errorf("offline after the move back, the district's officer: %d %.200s, want 200", st, b)
	}
}
