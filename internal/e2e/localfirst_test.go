package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// Programs a country lists in facility-hub-config/localReads are read from the hub
// while online; saves still go to central and show at once; what the hub cannot
// answer, and every other program, goes to central (trackerapi/localfirst.go).
func TestLocalReadsWhileOnline(t *testing.T) {
	e := newEnv(t)
	e.central.Users["nurse_b"].SearchOrgUnits = []string{fc.District} // searches the district
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"localReads": json.RawMessage(`{"programs":["` + fc.Program + `","` + fc.EventProgram + `"]}`)}
	e.central.SeedProgramEvent("LocEvent001", fc.EventProgram, fc.EventStage, "2026-11-01T00:00:00.000", fc.DefaultAOC)
	e.setup()
	a, b := e.web("nurse_a"), e.web("nurse_b")
	a.login()
	b.login()
	e.sync()

	central := func(path string) int { return e.central.RequestCount("GET " + path) }
	reads := func() int { return e.central.RequestsUnder("GET /api/tracker") }
	local := func(what, path string) {
		t.Helper()
		before := reads()
		if st, body := a.do("GET", path, nil); st != 200 {
			t.Fatalf("%s: %d %s", what, st, body)
		}
		if reads() != before {
			t.Errorf("%s went to central", what)
		}
	}
	local("a working list", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnits="+fc.Facility+"&orgUnitMode=SELECTED")
	local("a search, the user's scope being the facility", "/api/tracker/trackedEntities?program="+fc.Program+
		"&orgUnitMode=ACCESSIBLE&filter="+fc.AttrFirstName+":like:seed")
	local("a person without program=", "/api/tracker/trackedEntities/SeedTe00001")
	local("an enrollment", "/api/tracker/enrollments/SeedEn00001")
	local("an event program's list", "/api/tracker/events?program="+fc.EventProgram+"&orgUnit="+fc.Facility)
	local("an event of an event program", "/api/tracker/events/LocEvent001")
	local("an event of a tracker program", "/api/tracker/events/SeedEv00001")

	// A user who searches the district: central answers.
	before := central("/api/tracker/trackedEntities")
	if st, _ := b.do("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnitMode=ACCESSIBLE&filter="+fc.AttrFirstName+":like:seed", nil); st != 200 {
		t.Fatalf("district search: %d", st)
	}
	if central("/api/tracker/trackedEntities") != before+1 {
		t.Error("a district-wide search was answered by the hub")
	}
	// Something the hub does not hold: central answers.
	before = central("/api/tracker/trackedEntities/NotHere0001")
	a.do("GET", "/api/tracker/trackedEntities/NotHere0001", nil)
	if central("/api/tracker/trackedEntities/NotHere0001") != before+1 {
		t.Error("an object the hub does not hold was not asked of central")
	}

	// A save goes to central, and the hub's next answer shows it.
	posts := e.central.RequestCount("POST /api/tracker")
	st, body := a.do("POST", "/api/tracker?async=false", map[string]any{"trackedEntities": []any{map[string]any{
		"trackedEntity": "LocFirst001", "trackedEntityType": fc.TET, "orgUnit": fc.Facility,
		"attributes": []any{map[string]any{"attribute": fc.AttrFirstName, "value": "Localfirst"}},
		"enrollments": []any{map[string]any{"enrollment": "LocFirstEn1", "program": fc.Program, "orgUnit": fc.Facility,
			"enrolledAt": "2026-11-01", "occurredAt": "2026-11-01", "status": "ACTIVE"}}}}})
	if st != 200 || e.central.RequestCount("POST /api/tracker") != posts+1 {
		t.Fatalf("save while online: %d %s", st, body)
	}
	local("the save, read back", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnits="+fc.Facility+"&filter="+fc.AttrFirstName+":eq:Localfirst")
	if _, body := a.do("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnits="+fc.Facility+"&filter="+fc.AttrFirstName+":eq:Localfirst", nil); !strings.Contains(string(body), "LocFirst001") {
		t.Fatalf("the save, read back from the hub: %s", body)
	}

	// Without the setting, everything passes through again (read with the catalog,
	// hourly, as retention is).
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{}
	if err := e.app.Hub.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	before = central("/api/tracker/trackedEntities")
	a.do("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnits="+fc.Facility, nil)
	if central("/api/tracker/trackedEntities") != before+1 {
		t.Error("with no programs listed, a working list was answered by the hub")
	}
}

// Capture web registers a person with an enrollment that has no UID; central assigns
// one and names it in its report. With local reads, the hub must hold that
// enrollment, or Capture opens the new person as "not enrolled in this program"
// (found by TestCentralWork on 2.43, 9 November).
func TestLocalReadsRegistrationWithoutEnrollmentUID(t *testing.T) {
	e := newEnv(t)
	e.central.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"localReads": json.RawMessage(`{"programs":["` + fc.Program + `"]}`)}
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()

	st, body := a.do("POST", "/api/tracker?async=false", map[string]any{"trackedEntities": []any{map[string]any{
		"trackedEntity": "LocNoEnr001", "trackedEntityType": fc.TET, "orgUnit": fc.Facility,
		"attributes": []any{map[string]any{"attribute": fc.AttrFirstName, "value": "Noenrollmentuid"}},
		"enrollments": []any{map[string]any{"program": fc.Program, "orgUnit": fc.Facility,
			"enrolledAt": "2026-11-09", "status": "ACTIVE"}}}}})
	if st != 200 {
		t.Fatalf("registration: %d %s", st, body)
	}
	reads := e.central.RequestsUnder("GET /api/tracker")
	// Capture's reads of the new person's dashboard.
	st, body = a.do("GET", "/api/tracker/trackedEntities/LocNoEnr001?fields=programOwners[orgUnit],enrollments[program,status]&program="+fc.Program, nil)
	if e.central.RequestsUnder("GET /api/tracker") != reads {
		t.Fatal("the read went to central; this test is about the hub's answer")
	}
	var got struct {
		Enrollments   []map[string]any `json:"enrollments"`
		ProgramOwners []map[string]any `json:"programOwners"`
	}
	json.Unmarshal(body, &got)
	if st != 200 || len(got.Enrollments) != 1 || len(got.ProgramOwners) != 1 {
		t.Fatalf("the hub's answer for the new person: %d %s, want one enrollment and one owner", st, body)
	}
}
