package e2e

import (
	"context"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// Session A, replayed (PENDING.md, item 3): a nurse edits an event offline, someone
// changes it on central meanwhile, and the hub's push replaces central's newer
// version. The push goes ahead as before; the hub counts the case in its heartbeat.
// An edit central did not change is read but not counted; a registration costs no
// read; the country can turn the count off. A hub checks one entry in ten
// (sync.OverwriteSample); this test checks every one.
func TestOverwriteCounted(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("OwTe0000001", "OwEn0000001", "Owen", "NAT-5001", map[string]string{"OwEv0000001": fc.Stage1, "OwEv0000002": fc.Stage1})
	e.setup()
	e.app.Worker.OverwriteSample = 1 // every entry
	a := e.web("nurse_a")
	a.login()
	e.sync()
	h := e.app.Hub

	e.offline()
	for _, ev := range []string{"OwEv0000001", "OwEv0000002"} {
		if rep := a.json("POST", postSync, event(ev, "OwEn0000001", fc.Stage1), 200); len(errorCodes(rep)) != 0 {
			t.Fatal(rep)
		}
	}
	if rep := a.json("POST", postSync, newPatient("OwTe0000002", "OwEn0000002", "Ola", "NAT-5002", nil), 200); len(errorCodes(rep)) != 0 {
		t.Fatal(rep)
	}
	time.Sleep(5 * time.Millisecond)
	e.central.Seed("OwTe0000001", "OwEn0000001", "Owen", "NAT-5001", map[string]string{"OwEv0000001": fc.Stage1}) // changed on central
	e.online()
	if err := e.app.Worker.Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	ow := h.Store.Overwrites()
	if ow.Count != 1 || len(ow.Recent) != 1 || ow.Recent[0].UID != "OwEv0000001" || ow.Recent[0].User != "nurse_a" {
		t.Errorf("overwrites: %+v", ow)
	}
	if ev := e.central.Object("OwEv0000001"); ev["occurredAt"] != "2026-10-07" {
		t.Errorf("the push did not go ahead: central has %v", ev["occurredAt"])
	}
	for path, want := range map[string]int{"GET /api/tracker/events/OwEv0000001": 1, "GET /api/tracker/events/OwEv0000002": 1,
		"GET /api/tracker/trackedEntities/OwTe0000002": 0} {
		if n := e.central.RequestCount(path); n != want {
			t.Errorf("%s: %d reads, want %d", path, n, want)
		}
	}

	// Turned off on central: no read.
	e.putSigned("facility-hub-config/overwrites", `{"off":true}`)
	if err := h.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.sync() // a fresh copy from central to edit
	before := e.central.RequestCount("GET /api/tracker/events/OwEv0000002")
	e.offline()
	a.json("POST", postSync, event("OwEv0000002", "OwEn0000001", fc.Stage1), 200)
	e.online()
	if err := e.app.Worker.Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.central.RequestCount("GET /api/tracker/events/OwEv0000002"); n != before {
		t.Errorf("read with the count off: %d reads, %d before", n, before)
	}
}
