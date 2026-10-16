package live

import (
	"slices"
	"testing"

	"github.com/dhis2/facility-hub/internal/tracker"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The catalog records which programs allow one enrollment ever.
func TestCatalogOnlyEnrollOnce(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.OnlyEnrollOnce = true
	e.Setup()
	if pm := e.App.Hub.Catalog().Programs[fc.Program]; pm == nil || !pm.OnlyEnrollOnce {
		t.Fatalf("onlyEnrollOnce not in the catalog: %+v", pm)
	}
}

// stubEnrollment turns a stored enrollment into a stub, as pruning does.
func stubEnrollment(t *testing.T, e *tk.Env, uid string) {
	t.Helper()
	ix := e.App.Hub.Index
	o := ix.Get(uid)
	if o == nil {
		t.Fatalf("%s not held", uid)
	}
	n := &tracker.Obj{UID: o.UID, Kind: o.Kind, TE: o.TE, Program: o.Program, OwnerOrgUnit: o.OwnerOrgUnit,
		UpdatedAt: o.UpdatedAt, Origin: o.Origin, SyncState: o.SyncState, Slot: o.Slot, Data: tracker.StubOf(ix.Full(o))}
	if err := e.App.Hub.Store.PutObject(e.App.Hub.Store.DB(), n.ToStore()); err != nil {
		t.Fatal(err)
	}
	ix.Forget()
}

// listed returns the values of key in body[coll].
func listed(body map[string]any, coll, key string) []string {
	var out []string
	for _, x := range asList(body[coll]) {
		if m, ok := x.(map[string]any); ok {
			out = append(out, tracker.Str(m, key))
		}
	}
	return out
}

// A stub is never read: not in lists, not under its person, not singly (needs
// connection, since central has it), not even with includeDeleted.
func TestStubHiddenFromReads(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.Seed("RetTe000001", "RetEn000001", "Old", "NAT-9001", map[string]string{"RetEv000001": fc.Stage1})
	e.Setup()
	nurse := e.Android(tk.RoleNurse)
	nurse.Do("GET", "/api/me?fields=id", nil)
	e.Sync()
	stubEnrollment(t, e, "RetEn000001")
	e.Offline()
	for _, extra := range []string{"", "&includeDeleted=true"} {
		ens := nurse.JSON("GET", "/api/tracker/enrollments?program="+fc.Program+"&orgUnit="+fc.Facility+"&fields=enrollment"+extra, nil, 200)
		if slices.Contains(listed(ens, "enrollments", "enrollment"), "RetEn000001") {
			t.Errorf("a stub listed%s", extra)
		}
		tes := nurse.JSON("GET", "/api/tracker/trackedEntities?program="+fc.Program+"&orgUnits="+fc.Facility+"&fields=trackedEntity"+extra, nil, 200)
		if slices.Contains(listed(tes, "trackedEntities", "trackedEntity"), "RetTe000001") {
			t.Errorf("a person listed in a program where only a stub is held%s", extra)
		}
	}
	te := nurse.JSON("GET", "/api/tracker/trackedEntities/RetTe000001?fields=enrollments[enrollment]", nil, 200)
	if slices.Contains(listed(te, "enrollments", "enrollment"), "RetEn000001") {
		t.Error("a stub under its person")
	}
	if st, body := nurse.Do("GET", "/api/tracker/enrollments/RetEn000001", nil); st != 503 {
		t.Errorf("a stub read singly: %d %s", st, body)
	}
}
