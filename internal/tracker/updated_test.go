package tracker

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"math/rand"
	"net/url"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/store"
)

// updatedMatchAll is updatedMatch as it was before it stopped at the first child that
// decides: every child read, the visible ones kept, the newest compared (a child
// written on the hub by its hub write time, against the floor).
func updatedMatchAll(ix *Index, v *Viewer, o *Obj, tf timeFilter, q url.Values) (bool, *APIError) {
	if q.Get(tf.after) == "" && q.Get(tf.before) == "" {
		return true, nil
	}
	objs := []*Obj{o}
	if o.Kind == store.KindTrackedEntity {
		rows, err := ix.store.QueryObjects(`SELECT `+slimCol.ReplaceAllString(store.ObjectCols(""), "''")+
			` FROM objects WHERE te_uid = ? AND kind IN ('enrollment', 'event')`, o.UID)
		if err != nil {
			panic(err)
		}
		var children []*Obj
		for _, r := range rows {
			children = append(children, ix.row(r, false))
		}
		objs = append(objs, ix.visible(v, children)...)
	}
	floor, apiErr := updatedFloor(q)
	if apiErr != nil {
		return false, apiErr
	}
	if hubClockBehind(q) {
		floor = ""
	}
	newest := ""
	for _, x := range objs {
		if x.Origin == store.OriginLocal {
			if floor == "" || x.UpdatedAt >= floor {
				return true, nil
			}
			continue
		}
		newest = max(newest, x.UpdatedAt)
	}
	return tf.match(M{"updatedAt": newest}, q)
}

// A person counts as changed exactly as when every child was read, whatever makes a
// child visible or not: central's list for the user, the owning facility, deleted,
// a stub, written on the hub, and the program and stage the user may read.
func TestUpdatedMatchAsReadingEveryChild(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	const ou, other = "OuAaaaaaaa1", "OuAaaaaaaa2"
	progs := []string{"PrAaaaaaaa1", "PrAaaaaaaa2"}
	stages := []string{"PsAaaaaaaa1", "PsAaaaaaaa2"}
	days := []string{"2024-03-01T10:00:00.000", "2024-03-05T10:00:00.000", "2024-03-09T10:00:00.000", "2024-03-12T10:00:00.000", ""}
	r := rand.New(rand.NewSource(7))
	pick := func(l []string) string { return l[r.Intn(len(l))] }
	var people []*Obj
	listed := map[uint32]bool{}
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		put := func(o *Obj, deleted, stub bool) error {
			o.Slot = ix.SlotFor(o.UID)
			if deleted {
				o.Data["deleted"] = true
			}
			if stub {
				o.Data["hubStub"] = true
			}
			if r.Intn(5) > 0 {
				listed[o.Slot] = true
			}
			return s.PutObject(tx, o.ToStore())
		}
		origin := func(local int) (string, string) {
			if r.Intn(local) == 0 {
				return store.OriginLocal, store.SyncPending
			}
			return store.OriginCentral, store.SyncSynced
		}
		for i := range 300 {
			te := &Obj{UID: fmt.Sprintf("TeAaaaaa%03d", i), Kind: store.KindTrackedEntity, TET: "TetAaaaaaa1",
				OwnerOrgUnit: ou, UpdatedAt: pick(days), Data: M{}}
			te.Origin, te.SyncState = origin(30)
			if err := put(te, false, false); err != nil {
				return err
			}
			people = append(people, te)
			for j := range r.Intn(8) {
				c := &Obj{UID: fmt.Sprintf("EnAaa%03d%03d", i, j), Kind: store.KindEnrollment, TE: te.UID,
					Program: pick(progs), OwnerOrgUnit: ou, UpdatedAt: pick(days), Data: M{}}
				if r.Intn(2) == 0 {
					c.UID, c.Kind, c.Stage, c.Enrollment = fmt.Sprintf("EvAaa%03d%03d", i, j), store.KindEvent, pick(stages), "EnAaa000000"
				}
				if r.Intn(6) == 0 {
					c.OwnerOrgUnit = other
				}
				c.Origin, c.SyncState = origin(12)
				if err := put(c, r.Intn(6) == 0, c.Kind == store.KindEnrollment && r.Intn(8) == 0); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var deleted, stubs int
	s.DB().QueryRow(`SELECT sum(deleted), sum(stub) FROM objects`).Scan(&deleted, &stubs)
	if deleted == 0 || stubs == 0 {
		t.Fatalf("the fixture stored %d deleted and %d stubs", deleted, stubs)
	}
	ix.PrepareVisibility(1, listed).Done(true)
	acc := &access.List{TETs: map[string]access.Perm{"TetAaaaaaa1": {Read: true}},
		Programs: map[string]access.Perm{progs[0]: {Read: true}, progs[1]: {Read: true}},
		Stages:   map[string]access.Perm{stages[0]: {Read: true}}} // not the second stage
	upd := timeFilter{"updatedAt", "updatedAfter", "updatedBefore"}
	queries := []url.Values{
		{"updatedAfter": {"2024-03-04"}}, {"updatedAfter": {"2024-03-10"}}, {"updatedAfter": {"2024-03-20"}},
		{"updatedBefore": {"2024-03-06"}}, {"updatedAfter": {"2024-03-02"}, "updatedBefore": {"2024-03-10"}},
		{"updatedAfter": {time.Now().UTC().Add(72 * time.Hour).Format("2006-01-02")}}, // the hub's clock behind
	}
	differ, changed := 0, 0
	for _, scope := range []bool{true, false} {
		for _, deleted := range []bool{false, true} {
			v := &Viewer{UserID: 1, Access: acc, Facility: ou, SearchScope: scope, IncludeDeleted: deleted}
			for _, q := range queries {
				for _, te := range people {
					want, _ := updatedMatchAll(ix, v, te, upd, q)
					got, apiErr := ix.updatedMatch(v, te, upd, q)
					if apiErr != nil {
						t.Fatal(apiErr)
					}
					if got != want {
						differ++
						if differ <= 5 {
							t.Errorf("%s %v scope=%v deleted=%v: changed=%v, reading every child gives %v", te.UID, q, scope, deleted, got, want)
						}
					}
					if want {
						changed++
					}
				}
			}
		}
	}
	if differ > 0 {
		t.Errorf("%d answers differ", differ)
	}
	if changed == 0 || changed == 4*len(queries)*len(people) {
		t.Fatalf("the fixture does not tell the answers apart: %d changed", changed)
	}
}

// A search whose client has gone stops: it answers 499 and reads no further.
func TestSearchStopsWhenClientGone(t *testing.T) {
	ix, v, p := pagingFixture(t, 60)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v.Ctx = ctx
	if out, _, _, err := ix.run(v, p, 0); err == nil || err.Status != 499 {
		t.Fatalf("search for a client that has gone: %d found, %v; want 499", len(out), err)
	}
	v.Ctx = context.Background()
	if out, _, _, err := ix.run(v, p, 0); err != nil || len(out) == 0 {
		t.Fatalf("search for a client still there: %d found, %v", len(out), err)
	}
}

// An incremental sync with many changed people (more than any list size) returns,
// page by page, exactly the people that changed, in both orders clients use. A person
// counts as changed through their own update time or their enrollment's.
func TestIncrementalSearchManyChanged(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	const ou, prog, tet, n = "OuAaaaaaaa1", "PrAaaaaaaa1", "TetAaaaaaa1", 1200
	const old, recent = "2024-01-01T00:00:00.000", "2024-06-01T00:00:00.000"
	want := map[string]bool{}
	visible := map[uint32]bool{}
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := range n {
			te := &Obj{UID: fmt.Sprintf("TeAaaaa%04d", i), Kind: store.KindTrackedEntity, TET: tet, OwnerOrgUnit: ou,
				Origin: store.OriginCentral, SyncState: store.SyncSynced, UpdatedAt: old,
				Data: M{"trackedEntity": fmt.Sprintf("TeAaaaa%04d", i), "createdAt": fmt.Sprintf("2023-%02d-%02dT00:00:00.000", 1+i%12, 1+i%28)}}
			enr := &Obj{UID: fmt.Sprintf("EnAaaaa%04d", i), Kind: store.KindEnrollment, TE: te.UID, Program: prog,
				OwnerOrgUnit: ou, Origin: store.OriginCentral, SyncState: store.SyncSynced, UpdatedAt: old, Data: M{"status": "ACTIVE"}}
			switch i % 12 {
			case 0, 1, 2, 3, 4, 5:
				te.UpdatedAt = recent
			case 6:
				enr.UpdatedAt = recent // changed through the enrollment
			}
			for _, o := range []*Obj{te, enr} {
				o.Slot = ix.SlotFor(o.UID)
				if err := s.PutObject(tx, o.ToStore()); err != nil {
					return err
				}
				if i%50 != 49 { // a few the user may not see
					visible[o.Slot] = true
				}
			}
			if i%12 <= 6 && i%50 != 49 {
				want[te.UID] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(want) <= 500 {
		t.Fatalf("fixture: %d changed people, want more than 500", len(want))
	}
	ix.PrepareVisibility(1, visible).Done(true)
	v := &Viewer{UserID: 1, Facility: ou, Access: &access.List{Programs: map[string]access.Perm{prog: {Read: true}},
		TETs: map[string]access.Perm{tet: {Read: true}}},
		Catalog: &access.Catalog{Programs: map[string]*access.ProgramMeta{prog: {ID: prog}}}}
	for _, order := range []string{"", "createdAt:desc"} {
		got := map[string]bool{}
		for pg := 1; ; pg++ {
			q := url.Values{"program": {prog}, "orgUnits": {ou}, "pageSize": {"50"}, "page": {fmt.Sprint(pg)},
				"fields": {"trackedEntity"}, "updatedAfter": {"2024-05-01"}}
			if order != "" {
				q.Set("order", order)
			}
			res, apiErr := ix.Search(v, "trackedEntities", q)
			if apiErr != nil {
				t.Fatal(apiErr)
			}
			for _, it := range res.Items {
				uid := Str(it, "trackedEntity")
				if uid == "" {
					t.Fatalf("item without trackedEntity: %v", it)
				}
				if got[uid] {
					t.Fatalf("order %q: %s on two pages", order, uid)
				}
				got[uid] = true
			}
			if len(res.Items) < 50 {
				break
			}
		}
		if len(got) != len(want) {
			t.Errorf("order %q: %d people, want %d", order, len(got), len(want))
		}
		for uid := range got {
			if !want[uid] {
				t.Errorf("order %q: %s has not changed", order, uid)
				break
			}
		}
	}
}

// An incremental sync of a program returns exactly the people that pass updatedMatch
// as when every child was read, also when people whose rows changed in the day before
// updatedAfter (past the floor) are left out of the candidates without reading their
// children (changedTEs).
func TestIncrementalSearchAsReadingEveryChild(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	const ou, other, tet = "OuAaaaaaaa1", "OuAaaaaaaa2", "TetAaaaaaa1"
	progs := []string{"PrAaaaaaaa1", "PrAaaaaaaa2"}
	stages := []string{"PsAaaaaaaa1", "PsAaaaaaaa2"}
	// 03-09 10:00 is in the day before 03-10: past the floor, before updatedAfter.
	days := []string{"2024-03-01T10:00:00.000", "2024-03-09T10:00:00.000", "2024-03-09T23:00:00", "2024-03-10T10:00:00.000", "2024-03-12T10:00:00.000"}
	r := rand.New(rand.NewSource(11))
	pick := func(l []string) string { return l[r.Intn(len(l))] }
	var people []*Obj
	listed := map[uint32]bool{}
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		put := func(o *Obj) error {
			o.Slot = ix.SlotFor(o.UID)
			if r.Intn(6) > 0 {
				listed[o.Slot] = true
			}
			if r.Intn(10) == 0 {
				o.Origin, o.SyncState = store.OriginLocal, store.SyncPending
			} else {
				o.Origin, o.SyncState = store.OriginCentral, store.SyncSynced
			}
			return s.PutObject(tx, o.ToStore())
		}
		for i := range 400 {
			te := &Obj{UID: fmt.Sprintf("TeAaaaaa%03d", i), Kind: store.KindTrackedEntity, TET: tet, OwnerOrgUnit: ou,
				UpdatedAt: pick(days), Data: M{"trackedEntity": fmt.Sprintf("TeAaaaaa%03d", i), "createdAt": fmt.Sprintf("2023-%02d-%02dT00:00:00.000", 1+i%12, 1+i%28)}}
			if err := put(te); err != nil {
				return err
			}
			people = append(people, te)
			enr := &Obj{UID: fmt.Sprintf("EnAaa%03d000", i), Kind: store.KindEnrollment, TE: te.UID, Program: progs[0],
				OwnerOrgUnit: ou, UpdatedAt: pick(days), Data: M{"status": "ACTIVE"}}
			if err := put(enr); err != nil {
				return err
			}
			for j := 1; j <= r.Intn(5); j++ {
				c := &Obj{UID: fmt.Sprintf("EvAaa%03d%03d", i, j), Kind: store.KindEvent, TE: te.UID, Enrollment: enr.UID,
					Program: pick(progs), Stage: pick(stages), OwnerOrgUnit: ou, UpdatedAt: pick(days), Data: M{}}
				if r.Intn(6) == 0 {
					c.OwnerOrgUnit = other
				}
				if err := put(c); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ix.PrepareVisibility(1, listed).Done(true)
	acc := &access.List{TETs: map[string]access.Perm{tet: {Read: true}},
		Programs: map[string]access.Perm{progs[0]: {Read: true}, progs[1]: {Read: true}},
		Stages:   map[string]access.Perm{stages[0]: {Read: true}}} // not the second stage
	v := &Viewer{UserID: 1, Facility: ou, Access: acc, SearchScope: true,
		Catalog: &access.Catalog{Programs: map[string]*access.ProgramMeta{progs[0]: {ID: progs[0]}}}}
	upd := timeFilter{"updatedAt", "updatedAfter", "updatedBefore"}
	search := func(q url.Values) map[string]bool {
		got := map[string]bool{}
		for pg := 1; ; pg++ {
			q.Set("page", fmt.Sprint(pg))
			res, apiErr := ix.Search(v, "trackedEntities", q)
			if apiErr != nil {
				t.Fatal(apiErr)
			}
			for _, it := range res.Items {
				got[Str(it, "trackedEntity")] = true
			}
			if len(res.Items) < 50 {
				return got
			}
		}
	}
	base := url.Values{"program": {progs[0]}, "orgUnits": {ou}, "pageSize": {"50"}, "fields": {"trackedEntity"}}
	all := search(base)
	decided := 0
	for _, after := range []string{"2024-03-10", "2024-03-10T05:00:00.000", "2024-03-11", "2024-03-12T12:00:00Z"} {
		q := url.Values{"updatedAfter": {after}}
		want := map[string]bool{}
		for _, te := range people {
			if all[te.UID] {
				if ok, _ := updatedMatchAll(ix, v, te, upd, q); ok {
					want[te.UID] = true
				}
			}
		}
		floor, _ := updatedFloor(q)
		at, _ := parseTime(after)
		decided += len(ix.changedTEs(floor, false, at, false)) - len(ix.changedTEs(floor, false, at, true))
		sq := maps.Clone(base)
		sq.Set("updatedAfter", after)
		got := search(sq)
		for uid := range want {
			if !got[uid] {
				t.Errorf("updatedAfter %s: %s changed and is missing", after, uid)
			}
		}
		for uid := range got {
			if !want[uid] {
				t.Errorf("updatedAfter %s: %s has not changed and is returned", after, uid)
			}
		}
		t.Logf("updatedAfter %s: %d of %d changed, %d returned", after, len(want), len(all), len(got))
		if len(want) == 0 || len(want) == len(all) {
			t.Fatalf("updatedAfter %s: the fixture does not tell the answers apart: %d of %d changed", after, len(want), len(all))
		}
	}
	if decided == 0 {
		t.Fatal("no candidate past the floor was left out")
	}
}
