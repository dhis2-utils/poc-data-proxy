package e2e

import (
	"context"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// M2 (PENDING.md, item 30): a person registered offline stays readable by her author
// after the push, when another user's pull stores central's copy first. Pulls run per
// access group, so the first to list the person may be another group's; it used to
// make the person central's, visible to that puller only, and the author was refused
// her own record offline until her own pull listed it. The pulled object is now shared
// by the hub's own rules, as an online save is (core.ShareByRules): its author, and
// whoever else may read it, keep seeing it.
func TestM2AuthorKeepsOwnRecordsAfterPush(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a, b := e.web("nurse_a"), e.web("nurse_b")
	a.login()
	b.login()
	e.sync()

	e.offline()
	path := "/api/tracker/trackedEntities/M2Te0000001?program=" + fc.Program + "&fields=trackedEntity"
	if rep := a.json("POST", postSync, newPatient("M2Te0000001", "M2En0000001", "Mariam", "NAT-3001", nil), 200); len(errorCodes(rep)) != 0 {
		t.Fatal(rep)
	}
	e.online()
	if err := e.app.Worker.Push(context.Background()); err != nil {
		t.Fatal(err)
	}
	// nurse_b's pull lists the person first: central's copy, stored for her.
	h := e.app.Hub
	nb, _ := h.Store.UserByName("nurse_b")
	te := h.Index.Get("M2Te0000001")
	if te == nil {
		t.Fatal("the person is not on the hub")
	}
	central := h.Index.Full(te)
	central["updatedAt"] = "2099-01-01T00:00:00.000" // central's own time, which the hub's copy lacks
	if _, err := h.UpsertCentral(nb.ID, tracker.M{"trackedEntities": []any{central}}, true); err != nil {
		t.Fatal(err)
	}
	e.offline()
	if st, body := a.do("GET", path, nil); st != 200 {
		t.Errorf("nurse_a offline, her own registration after the push and nurse_b's pull: %d %s", st, body)
	}
	if st, _ := b.do("GET", path, nil); st != 200 {
		t.Errorf("nurse_b offline: %d", st)
	}
}
