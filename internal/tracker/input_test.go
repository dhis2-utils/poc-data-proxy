package tracker

import (
	"context"
	"database/sql"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/store"
)

// Hostile or careless tracker input from a client on the LAN (review R-01, R-02,
// R-03, R-10, R-51).

const (
	inOU, inTET              = "OuAaaaaaaa1", "TetAaaaaaa1"
	inProgA, inProgB         = "PrInputAaa1", "PrInputBbb1"
	inStA1, inStA2, inStB1   = "StInputA001", "StInputA002", "StInputB001"
	inAttrName, inAttrA      = "AtInputNam1", "AtInputPrA1"
	inAttrB                  = "AtInputPrB1" // only in program B, which the nurse may not read
	inRelType, inRelReadOnly = "RtInputAaa1", "RtInputRo01"
)

type inputFixture struct {
	t   *testing.T
	s   *store.Store
	ix  *Index
	cat *access.Catalog
}

func newInputFixture(t *testing.T) *inputFixture {
	cat := &access.Catalog{
		Programs: map[string]*access.ProgramMeta{
			inProgA: {ID: inProgA, TET: inTET, Stages: []string{inStA1, inStA2}, Attributes: []access.AttrMeta{{ID: inAttrA}}},
			inProgB: {ID: inProgB, TET: inTET, Stages: []string{inStB1}, Attributes: []access.AttrMeta{{ID: inAttrB}}},
		},
		TETAttributes: map[string][]access.AttrMeta{inTET: {{ID: inAttrName}}},
	}
	cat.Index()
	s := testStore(t)
	f := &inputFixture{t: t, s: s, ix: NewStoreIndex(s), cat: cat}
	// Two people in program A, each with a value in program B's attribute, which only
	// someone with access to program B entered.
	admin := f.viewer(func(l *access.List) { l.Programs[inProgB] = access.Perm{Read: true, Write: true} })
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for i, p := range []struct{ te, enr, name, b string }{
		{"TeInput0001", "EnInput0001", "Ama", "z"},
		{"TeInput0002", "EnInput0002", "Kofi", "a"},
	} {
		f.mustImport(admin, at.Add(time.Duration(i)*time.Hour), M{"trackedEntities": []any{M{"trackedEntity": p.te,
			"trackedEntityType": inTET, "orgUnit": inOU, "attributes": []any{
				M{"attribute": inAttrName, "value": p.name}, M{"attribute": inAttrA, "value": "x"}, M{"attribute": inAttrB, "value": p.b}},
			"enrollments": []any{M{"enrollment": p.enr, "program": inProgA, "orgUnit": inOU, "enrolledAt": "2026-11-01",
				"events": []any{M{"event": "Ev" + p.enr[2:], "programStage": inStA1, "orgUnit": inOU, "occurredAt": "2026-11-01"}}}}}}})
	}
	return f
}

// viewer is a nurse who may read and write program A and its stages, but not
// program B; edit changes her access list.
func (f *inputFixture) viewer(edit ...func(*access.List)) *Viewer {
	l := &access.List{
		Programs: map[string]access.Perm{inProgA: {Read: true, Write: true}},
		Stages:   map[string]access.Perm{inStA1: {Read: true, Write: true}, inStA2: {Read: true, Write: true}},
		TETs:     map[string]access.Perm{inTET: {Read: true, Write: true}},
		RelationshipTypes: map[string]access.Perm{inRelType: {Read: true, Write: true},
			inRelReadOnly: {Read: true}},
	}
	for _, e := range edit {
		e(l)
	}
	return &Viewer{UserID: 1, Access: l, Facility: inOU, SearchScope: true, Catalog: f.cat}
}

func (f *inputFixture) importAs(v *Viewer, now time.Time, payload M) *ImportOutcome {
	out := (&Importer{Ix: f.ix, V: v, Username: "nurse", UserUID: "UsNurse0001", Now: now, AtomicAll: true}).Import(payload)
	err := f.s.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, o := range out.Accepted {
			if o.Slot == 0 {
				o.Slot = f.ix.SlotFor(o.UID)
			}
			if err := f.s.PutObject(tx, o.ToStore()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *inputFixture) mustImport(v *Viewer, now time.Time, payload M) {
	f.t.Helper()
	if out := f.importAs(v, now, payload); len(out.Errors) > 0 {
		f.t.Fatalf("seed import: %v", out.Errors)
	}
}

func codesOf(out *ImportOutcome) []string {
	var codes []string
	for _, e := range out.Errors {
		codes = append(codes, e.ErrorCode)
	}
	return codes
}

func (f *inputFixture) put(objs ...*Obj) {
	err := f.s.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, o := range objs {
			o.Slot = f.ix.SlotFor(o.UID)
			if err := f.s.PutObject(tx, o.ToStore()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func rel(uid, rt string, from, to M) M {
	return M{"relationship": uid, "relationshipType": rt, "from": from, "to": to}
}

// R-01: a relationship end is of the kind its reference names, never a relationship.
// A relationship pointing at itself, accepted, made every read of the person recurse
// until the hub died, after every restart too.
func TestRelationshipEndsAreOfTheirKind(t *testing.T) {
	f := newInputFixture(t)
	v := f.viewer()
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	te1 := M{"trackedEntity": M{"trackedEntity": "TeInput0001"}}
	for _, c := range []struct {
		what    string
		payload M
		want    []string
	}{
		{"the same relationship again, pointing at itself", M{"relationships": []any{
			rel("RlInputSelf", inRelType, te1, M{"trackedEntity": M{"trackedEntity": "TeInput0002"}}),
			rel("RlInputSelf", inRelType, te1, M{"event": M{"event": "RlInputSelf"}})}}, []string{"E4012"}},
		{"two pointing at each other", M{"relationships": []any{
			rel("RlInputMut1", inRelType, te1, M{"event": M{"event": "RlInputMut2"}}),
			rel("RlInputMut2", inRelType, te1, M{"event": M{"event": "RlInputMut1"}})}}, []string{"E4012", "E5000"}},
		{"an end named an event that is a person", M{"relationships": []any{
			rel("RlInputKind", inRelType, te1, M{"event": M{"event": "TeInput0002"}})}}, []string{"E4012"}},
		{"a type central does not have", M{"relationships": []any{
			rel("RlInputType", "RtNoSuchTy1", te1, M{"trackedEntity": M{"trackedEntity": "TeInput0002"}})}}, []string{"E4006"}},
		{"a type the user may only read", M{"relationships": []any{
			rel("RlInputRead", inRelReadOnly, te1, M{"trackedEntity": M{"trackedEntity": "TeInput0002"}})}}, []string{"E4020"}},
	} {
		out := f.importAs(v, now, c.payload)
		if !slices.Equal(codesOf(out), c.want) || len(out.Accepted) != 0 {
			t.Errorf("%s: codes %v accepted %d, want %v and none", c.what, codesOf(out), len(out.Accepted), c.want)
		}
	}
	if out := f.importAs(v, now, M{"relationships": []any{
		rel("RlInputGood", inRelType, te1, M{"trackedEntity": M{"trackedEntity": "TeInput0002"}})}}); len(out.Errors) != 0 {
		t.Fatalf("a good relationship: %v", out.Errors)
	}

	// Rows stored before the check (or by an older hub) are read without recursing.
	stored := func(uid string, to M) *Obj {
		return &Obj{UID: uid, Kind: store.KindRelationship, OwnerOrgUnit: inOU, Origin: store.OriginLocal,
			SyncState: store.SyncPending, Data: rel(uid, inRelType, te1, to)}
	}
	f.put(stored("RlStoreSelf", M{"event": M{"event": "RlStoreSelf"}}),
		stored("RlStoreMut1", M{"event": M{"event": "RlStoreMut2"}}),
		stored("RlStoreMut2", M{"event": M{"event": "RlStoreMut1"}}))
	for _, uid := range []string{"RlStoreSelf", "RlStoreMut1", "RlStoreMut2"} {
		if f.ix.CanRead(v, f.ix.Get(uid)) {
			t.Errorf("%s: readable", uid)
		}
	}
	if !f.ix.CanRead(v, f.ix.Get("RlInputGood")) {
		t.Error("the good relationship is not readable")
	}
	res, apiErr := f.ix.Search(v, "relationships", url.Values{"trackedEntity": {"TeInput0001"}})
	if apiErr != nil || len(res.Items) != 1 {
		t.Fatalf("the person's relationships: %v %v, want the good one", res, apiErr)
	}
}

// R-02: deleted, hubStub, and the times and authors are the hub's. A client's
// "deleted": true hid a record from everyone offline; "deleted": "x" broke every list
// with the row.
func TestImportKeepsHubOwnedFields(t *testing.T) {
	f := newInputFixture(t)
	v := f.viewer()
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	out := f.importAs(v, now, M{"trackedEntities": []any{
		M{"trackedEntity": "TeInput0001", "deleted": true, "updatedAt": "2099-01-01T00:00:00.000", "updatedBy": M{"username": "someone"}},
		M{"trackedEntity": "TeInput0003", "trackedEntityType": inTET, "orgUnit": inOU, "deleted": "x", "hubStub": true,
			"createdAt": "2001-01-01T00:00:00.000", "createdBy": M{"username": "someone"}},
	}})
	if len(out.Errors) != 0 {
		t.Fatalf("errors: %v", out.Errors)
	}
	for _, uid := range []string{"TeInput0001", "TeInput0003"} {
		o := f.ix.Get(uid)
		d := f.ix.Full(o)
		if o.Deleted || o.Stub || d["deleted"] != false || d["hubStub"] != nil || o.UpdatedAt != DHIS2Time(now) ||
			Str(asM(d["updatedBy"]), "username") != "nurse" || Str(asM(d["createdBy"]), "username") != "nurse" {
			t.Errorf("%s: deleted=%v stub=%v data %v", uid, o.Deleted, o.Stub, d)
		}
	}
	for _, del := range []string{"true", "false"} {
		res, apiErr := f.ix.Search(v, "trackedEntities", url.Values{"program": {inProgA}, "orgUnits": {inOU}, "includeDeleted": {del}})
		if apiErr != nil || len(res.Items) != 2 {
			t.Errorf("includeDeleted=%s: %v %v, want both people", del, res, apiErr)
		}
	}
}

// R-10: an update does not move an enrollment to another program or person, nor an
// event to another stage or enrollment (central: E1127, E1128). Access is then
// checked against where the object is.
func TestUpdateKeepsWhereObjectIs(t *testing.T) {
	f := newInputFixture(t)
	v := f.viewer(func(l *access.List) { l.Programs[inProgB] = access.Perm{Read: true, Write: true} })
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	enr := func(extra M) M {
		e := M{"enrollment": "EnInput0001", "trackedEntity": "TeInput0001", "program": inProgA, "orgUnit": inOU,
			"enrolledAt": "2026-11-01", "status": "ACTIVE"}
		for k, x := range extra {
			e[k] = x
		}
		return M{"enrollments": []any{e}}
	}
	ev := func(extra M) M {
		e := M{"event": "EvInput0001", "enrollment": "EnInput0001", "programStage": inStA1, "orgUnit": inOU, "occurredAt": "2026-11-01"}
		for k, x := range extra {
			e[k] = x
		}
		return M{"events": []any{e}}
	}
	for _, c := range []struct {
		what    string
		v       *Viewer
		payload M
		want    []string
	}{
		{"enrollment to another person", v, enr(M{"trackedEntity": "TeInput0002"}), []string{"E1127"}},
		{"enrollment to another program", v, enr(M{"program": inProgB}), []string{"E1127"}},
		{"event to another enrollment", v, ev(M{"enrollment": "EnInput0002"}), []string{"E1128"}},
		{"event to another stage", v, ev(M{"programStage": inStA2}), []string{"E1128"}},
		{"event to a stage of another program", v, ev(M{"programStage": inStB1}), []string{"E1128"}},
		{"event to another program", v, ev(M{"program": inProgB}), []string{"E1079"}},
		// Write access on stage 2 only: a stage 1 event is not hers to change.
		{"event of a stage she may not write", f.viewer(func(l *access.List) { l.Stages[inStA1] = access.Perm{Read: true} }),
			ev(M{"programStage": inStA2}), []string{"E1128"}},
		{"event of a stage she may not write, stage left out", f.viewer(func(l *access.List) { l.Stages[inStA1] = access.Perm{Read: true} }),
			ev(M{"programStage": ""}), []string{"E1095"}},
		{"enrollment unchanged", v, enr(nil), nil},
		{"event unchanged", v, ev(nil), nil},
	} {
		if got := codesOf(f.importAs(c.v, now, c.payload)); !slices.Equal(got, c.want) {
			t.Errorf("%s: codes %v, want %v", c.what, got, c.want)
		}
	}
	if o := f.ix.Get("EvInput0001"); o.Enrollment != "EnInput0001" || o.Stage != inStA1 || o.TE != "TeInput0001" {
		t.Errorf("the event moved: %+v", o)
	}
}

// R-03: event lists filter, and person lists sort, only on attributes the user may
// read: program B's attribute is not the nurse's to see.
func TestSearchesUseOnlyReadableAttributes(t *testing.T) {
	f := newInputFixture(t)
	nurse := f.viewer()
	both := f.viewer(func(l *access.List) { l.Programs[inProgB] = access.Perm{Read: true} })
	events := func(v *Viewer, filter string) int {
		res, apiErr := f.ix.Search(v, "events", url.Values{"program": {inProgA}, "orgUnit": {inOU}, "filterAttributes": {filter}})
		if apiErr != nil {
			t.Fatal(apiErr)
		}
		return len(res.Items)
	}
	if n := events(both, inAttrB+":eq:z"); n != 1 {
		t.Errorf("with program B: %d events, want 1", n)
	}
	if n := events(nurse, inAttrB+":eq:z"); n != 0 {
		t.Errorf("without program B: %d events match program B's attribute, want 0", n)
	}
	if n := events(nurse, inAttrA+":eq:x"); n != 2 {
		t.Errorf("a readable attribute: %d events, want 2", n)
	}
	people := func(v *Viewer) []string {
		res, apiErr := f.ix.Search(v, "trackedEntities", url.Values{"program": {inProgA}, "orgUnits": {inOU},
			"order": {inAttrB + ":desc"}, "fields": {"trackedEntity"}})
		if apiErr != nil {
			t.Fatal(apiErr)
		}
		var out []string
		for _, it := range res.Items {
			out = append(out, Str(it, "trackedEntity"))
		}
		return out
	}
	if got := people(both); !slices.Equal(got, []string{"TeInput0001", "TeInput0002"}) {
		t.Errorf("with program B, by its attribute descending: %v", got)
	}
	// Without access the attribute orders as empty: newest first, as on a tie.
	if got := people(nurse); !slices.Equal(got, []string{"TeInput0002", "TeInput0001"}) {
		t.Errorf("without program B: %v, want the order of a tie", got)
	}
}

// R-51: an order central refuses is refused (400), not ignored.
func TestInvalidOrderRefused(t *testing.T) {
	f := newInputFixture(t)
	v := f.viewer()
	for _, c := range []struct {
		coll, order string
		status      int
	}{
		{"trackedEntities", "nosuchfld", 400},
		{"trackedEntities", "createdAt:sideways", 400},
		{"trackedEntities", "createdAt,createdAt:desc", 400},
		{"trackedEntities", "occurredAt", 400},
		{"trackedEntities", inAttrA + ":desc,createdAt", 0},
		{"enrollments", inAttrA, 400},
		{"enrollments", "enrolledAt:DESC", 0},
		{"events", "occurredAt:desc", 0},
		{"events", "eventDate", 400},
	} {
		q := url.Values{"program": {inProgA}, "orgUnits": {inOU}, "orgUnit": {inOU}, "order": {c.order}}
		if c.coll == "events" {
			delete(q, "orgUnits")
		} else {
			delete(q, "orgUnit")
		}
		_, apiErr := f.ix.Search(v, c.coll, q)
		got := 0
		if apiErr != nil {
			got = apiErr.Status
		}
		if got != c.status {
			t.Errorf("%s order=%s: %v, want status %d", c.coll, c.order, apiErr, c.status)
		}
	}
}
