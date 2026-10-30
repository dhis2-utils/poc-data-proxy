package e2e

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// An online read of a person the hub already holds, unchanged on central, does not
// rewrite them: on the Pis the hub wrote about 250 MB a minute while online, storing
// every answer anew (docs/PERFORMANCE.md). A read after a change on central still
// stores the change.
func TestOnlineReadOfUnchangedPersonIsNotRewritten(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("LearnTe0091", "LearnEn0091", "Ama", "NAT-9191", nil)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	stored := func() []byte {
		var b []byte
		if err := e.app.Hub.Store.DB().QueryRow(`SELECT payload_enc FROM objects WHERE uid = ?`, "LearnTe0091").Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	read := func() {
		if st, b := a.do("GET", "/api/tracker/trackedEntities/LearnTe0091?program="+fc.Program+"&fields=*", nil); st != 200 {
			t.Fatalf("online read: %d %s", st, b)
		}
	}
	before := stored()
	read()
	if !bytes.Equal(before, stored()) {
		t.Error("an unchanged person was written again")
	}

	time.Sleep(1100 * time.Millisecond) // a later change, in central's whole seconds
	e.central.Seed("LearnTe0091", "LearnEn0091", "Changed", "NAT-9191", nil)
	read()
	if o := e.app.Hub.Index.Get("LearnTe0091"); o == nil || !strings.Contains(fmt.Sprint(e.app.Hub.Index.Full(o)), "Changed") {
		t.Errorf("the change on central was not stored: %v", o)
	}
}

// S2: a save passed on to central while a full pull lists its program is not in that
// listing (the listing was taken before the save). The full pull's
// RemoveVisibilityExcept must keep it visible to its author: it compares with the
// listing's start time, so what the hub wrote or passed on after the listing began
// stays visible.
func TestFullPullKeepsSaveMadeDuringItVisible(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	u, err := e.app.Hub.Store.UserByName("nurse_a")
	if err != nil || u == nil {
		t.Fatal("nurse_a not registered on the hub")
	}
	// A save passed on to central "during the listing": the hub stamps its copy with
	// the hub's time until the next pull gives central's.
	a.json("POST", postSync, newPatient("S2Te0000001", "S2En0000001", "S2", "NAT-S2001", nil), 200)
	// The seed objects as a listing taken before the save would have them: old times.
	if _, err := e.app.Hub.Store.DB().Exec(`UPDATE objects SET updated_at='2026-01-01T00:00:00.000' WHERE uid LIKE 'Seed%'`); err != nil {
		t.Fatal(err)
	}
	// The full pull's listing started a minute ago and listed nothing (seen empty).
	since := e.app.Hub.CentralNow().Add(-time.Minute).Format("2006-01-02T15:04:05.000")
	n := e.app.Hub.RemoveVisibilityExcept(u.ID, []string{fc.Program, ""}, map[uint32]bool{}, since)
	saved, seed := e.app.Hub.Index.Get("S2Te0000001"), e.app.Hub.Index.Get("SeedTe00001")
	if saved == nil || seed == nil {
		t.Fatalf("objects missing: saved=%v seed=%v", saved, seed)
	}
	t.Logf("RemoveVisibilityExcept removed %d objects; the save made during the pull: visible=%v; the seed: visible=%v",
		n, e.app.Hub.Index.VisibleTo(saved, u.ID), e.app.Hub.Index.VisibleTo(seed, u.ID))
	if !e.app.Hub.Index.VisibleTo(saved, u.ID) {
		t.Error("S2: the person saved on central during the full pull was hidden from its author offline")
	}
	if e.app.Hub.Index.VisibleTo(seed, u.ID) {
		t.Error("an object the listing did not include and nothing updated since stayed visible")
	}
	if n == 0 {
		t.Error("nothing was removed: the check did not run")
	}
}
