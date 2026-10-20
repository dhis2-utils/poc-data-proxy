package e2e

import (
	"encoding/json"
	"testing"
	"time"
)

// refusedGroups is the heartbeat's refusals by kind, programme and error code ("refused").
func refusedGroups(e *env) []map[string]any {
	var g []map[string]any
	b, _ := json.Marshal(e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["refused"])
	json.Unmarshal(b, &g)
	return g
}

func retry(e *env, id string, g map[string]any) {
	r := map[string]any{"id": id, "kind": g["kind"], "program": g["program"], "code": g["code"],
		"at": time.Now().UTC().Format(time.RFC3339), "by": "hub_admin"}
	b, _ := json.Marshal(map[string]any{"retries": []any{r}})
	if e.central.DataStore["facility-hub-admin"] == nil {
		e.central.DataStore["facility-hub-admin"] = map[string]json.RawMessage{}
	}
	e.central.DataStore["facility-hub-admin"]["retries"] = b
}

// After central is fixed, a hub manager resends a whole group of refusals from the
// Facility Hub app: each hub sends its refused saves of that kind, programme and code
// again, as their authors, once.
func TestRetryResendsRefusedGroup(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	e.central.Seed("CentralTe79", "CentralEn79", "Other", "NAT-7979", nil)
	a.json("POST", postSync, newPatient("DupTe000079", "DupEn000079", "Dup", "NAT-7979", nil), 200)
	e.online()
	e.sync()
	groups := refusedGroups(e)
	if len(groups) == 0 {
		t.Fatal("no refusal reported")
	}
	var dup map[string]any
	for _, g := range groups {
		if g["code"] == "E1064" {
			dup = g
		}
	}
	if dup == nil {
		t.Fatalf("no E1064 group in %v", groups)
	}
	// A retry for another code changes nothing.
	retry(e, "r-other", map[string]any{"kind": dup["kind"], "program": dup["program"], "code": "E9999"})
	e.sync()
	if e.central.Object("DupTe000079") != nil {
		t.Fatal("a retry for another code resent the refusal")
	}
	// Central is fixed: the other person's national ID was a typo.
	e.central.Seed("CentralTe79", "CentralEn79", "Other", "NAT-0000", nil)
	retry(e, "r-dup", dup)
	e.sync()
	if e.central.Object("DupTe000079") == nil {
		t.Fatal("the retried person did not reach central")
	}
	if g := refusedGroups(e); len(g) != 0 {
		t.Fatalf("refusals left after the retry: %v", g)
	}
	if n := e.app.Hub.Store.CountIssues(); n != 0 {
		t.Errorf("%d issues left on the hub", n)
	}
}

// A retry is applied once: if central still refuses, the save stays refused until the
// next retry or a fix at the facility, and is not resent every cycle.
func TestRetryAppliedOnce(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	e.central.Seed("CentralTe80", "CentralEn80", "Other", "NAT-8080", nil)
	a.json("POST", postSync, newPatient("DupTe000080", "DupEn000080", "Dup", "NAT-8080", nil), 200)
	e.online()
	e.sync()
	var dup map[string]any
	for _, g := range refusedGroups(e) {
		if g["code"] == "E1064" {
			dup = g
		}
	}
	if dup == nil {
		t.Fatal("no E1064 refusal")
	}
	retry(e, "r-1", dup)
	before := e.central.RequestCount("POST /api/tracker")
	e.sync() // resent, refused again: central was not fixed
	e.sync()
	e.sync()
	if n := e.central.RequestCount("POST /api/tracker") - before; n != 1 {
		t.Fatalf("the refused save was sent %d times after one retry", n)
	}
	if len(refusedGroups(e)) == 0 {
		t.Fatal("the save refused again is not reported")
	}
}
