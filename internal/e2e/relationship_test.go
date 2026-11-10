package e2e

import (
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// A relationship between two persons, entered offline: the hub checks its type against
// the types central lists (E4006 otherwise), keeps it, serves it, and pushes it.
func TestOfflineRelationship(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync() // the user's access list, with central's relationship types
	e.offline()
	a.json("POST", postSync, newPatient("RelTe000001", "RelEn000001", "Index", "NAT-8101", nil), 200)
	a.json("POST", postSync, newPatient("RelTe000002", "RelEn000002", "Contact", "NAT-8102", nil), 200)
	te := func(uid string) map[string]any {
		return map[string]any{"trackedEntity": map[string]any{"trackedEntity": uid}}
	}
	rel := map[string]any{"relationships": []any{map[string]any{"relationship": "RelRel00001", "relationshipType": fc.RelType,
		"from": te("RelTe000001"), "to": te("RelTe000002")}}}
	if rep := a.json("POST", postSync, rel, 200); rep["status"] != "OK" {
		t.Fatalf("offline relationship: %v", rep)
	}
	got := a.json("GET", "/api/tracker/relationships?trackedEntity=RelTe000001", nil, 200)
	if ids := uids(got["relationships"], "relationship"); !contains(ids, "RelRel00001") {
		t.Fatalf("relationships offline: %v", got)
	}
	e.online()
	e.sync()
	r := e.central.Object("RelRel00001")
	if r == nil || r["relationshipType"] != fc.RelType {
		t.Fatalf("relationship on central: %v", r)
	}
}
