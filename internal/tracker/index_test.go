package tracker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	k, err := hubcrypto.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDK(k); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Rows stored before the query columns existed (schema 2) are filled from their
// payloads on the first unlock, and are then found by searches.
func TestLoadFillsQueryColumns(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	te := &Obj{UID: "TeAaaaaaaa1", Kind: store.KindTrackedEntity, TET: "TetAaaaaaa1", OwnerOrgUnit: "OuAaaaaaaa1",
		Origin: store.OriginLocal, SyncState: store.SyncPending, Data: M{"trackedEntity": "TeAaaaaaaa1",
			"createdAt": "2024-01-02T03:04:05.000", "attributes": []any{M{"attribute": "AtAaaaaaaa1", "value": "Amina"}}}}
	enr := &Obj{UID: "EnAaaaaaaa1", Kind: store.KindEnrollment, TE: te.UID, Program: "PrAaaaaaaa1", OwnerOrgUnit: "OuAaaaaaaa1",
		Origin: store.OriginLocal, SyncState: store.SyncPending, Data: M{"enrollment": "EnAaaaaaaa1", "status": "ACTIVE"}}
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, o := range []*Obj{enr, te} { // the enrollment first: its person's date comes later
			o.Slot = ix.SlotFor(o.UID)
			if err := s.PutObject(tx, o.ToStore()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var created string
	s.DB().QueryRow(`SELECT te_created FROM objects WHERE uid = ?`, enr.UID).Scan(&created)
	if created != "2024-01-02T03:04:05.000" {
		t.Fatalf("enrollment's te_created = %q, want its person's createdAt", created)
	}

	// Back to how schema 2 left them.
	if _, err := s.DB().Exec(`UPDATE objects SET slim = NULL, created_at = '', status = '', te_created = '';
DELETE FROM te_attrs`); err != nil {
		t.Fatal(err)
	}
	if err := ix.Load(s); err != nil {
		t.Fatal(err)
	}
	if o := ix.Get(te.UID); o == nil || Str(o.Data, "createdAt") == "" || len(anyList(o.Data["attributes"])) != 1 {
		t.Fatalf("slim data not filled: %+v", o)
	}
	if e := ix.Get(enr.UID); e == nil || Str(e.Data, "status") != "ACTIVE" {
		t.Fatalf("enrollment not filled: %+v", e)
	}
	if !ix.AttributeValueTaken("AtAaaaaaaa1", "Amina", "TeOther0001") {
		t.Fatal("attribute values not filled")
	}
	s.DB().QueryRow(`SELECT te_created FROM objects WHERE uid = ?`, enr.UID).Scan(&created)
	if created == "" {
		t.Fatal("te_created not filled")
	}

	// And a search finds the person by name.
	acc := &access.List{}
	v := &Viewer{UserID: 1, Access: acc, Facility: "OuAaaaaaaa1", SearchScope: true}
	p, apiErr := ix.searchTEs(v, url.Values{"trackedEntityType": {"TetAaaaaaa1"}, "orgUnits": {"OuAaaaaaaa1"},
		"filter": {"AtAaaaaaaa1:like:mIn"}}, "")
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	// The plan's SQL alone (visibility and readable attributes need an access list
	// with grants, which this test does not build).
	rows, err := s.QueryObjects(p.sel, p.args...)
	if err != nil || len(rows) != 1 || rows[0].UID != te.UID {
		t.Fatalf("search by name: %v %d rows", err, len(rows))
	}
}

// LIKE patterns match the text literally: SQL's wildcards in a search are escaped.
func TestLikePatternEscapes(t *testing.T) {
	if got := likePattern(`50%_A\b`); got != `%50\%\_a\\b%` {
		t.Fatalf("likePattern = %q", got)
	}
}

// A cached payload is served only for the version it was decoded from: rewriting an
// object seals it with a new nonce, so the next read decodes the new payload.
func TestPayloadCacheFollowsVersions(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	o := &Obj{UID: "EvAaaaaaaa1", Kind: store.KindEvent, OwnerOrgUnit: "OuAaaaaaaa1", Origin: store.OriginLocal,
		SyncState: store.SyncPending, Data: M{"event": "EvAaaaaaaa1", "status": "ACTIVE"}}
	o.Slot = ix.SlotFor(o.UID)
	put := func() {
		if err := s.Tx(context.Background(), func(tx *sql.Tx) error { return s.PutObject(tx, o.ToStore()) }); err != nil {
			t.Fatal(err)
		}
	}
	put()
	if d := ix.loadPayloads([]string{o.UID})[o.UID]; Str(d, "status") != "ACTIVE" {
		t.Fatalf("first read: %v", d)
	}
	first := ix.loadPayloads([]string{o.UID})[o.UID]
	if again := ix.loadPayloads([]string{o.UID})[o.UID]; fmt.Sprintf("%p", again) != fmt.Sprintf("%p", first) {
		t.Fatal("an unchanged payload was decoded again")
	}
	o.Data = M{"event": "EvAaaaaaaa1", "status": "COMPLETED"}
	put()
	if d := ix.loadPayloads([]string{o.UID})[o.UID]; Str(d, "status") != "COMPLETED" {
		t.Fatalf("after a rewrite the cache served the old version: %v", d)
	}
}

// A first page that needs a second read continues where the first read stopped: the
// people the user cannot see are skipped, none of those they can see are.
func TestFirstPageReadsOnWithoutGaps(t *testing.T) {
	ix, v, p := pagingFixture(t, 10)
	got, more, _, apiErr := ix.run(v, p, 2)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	var uids []string
	for _, o := range got {
		uids = append(uids, o.UID)
	}
	if fmt.Sprint(uids) != "[TeAaaaaa003 TeAaaaaa004]" || !more {
		t.Fatalf("page 1 = %v (more %v), want [TeAaaaaa003 TeAaaaaa004] and more", uids, more)
	}
}

// Pages read on from where the page before ended (pagePositions) are the pages read
// from the start: the same people, in the same order, with the same count.
func TestPagesReadOnFromLastPosition(t *testing.T) {
	ix, v, _ := pagingFixture(t, 40)
	const ou, prog = "OuAaaaaaaa1", "PrAaaaaaaa1"
	v.Catalog = &access.Catalog{Programs: map[string]*access.ProgramMeta{prog: {ID: prog}}}
	order := ""
	query := func(pg int) url.Values {
		q := url.Values{"program": {prog}, "orgUnits": {ou}, "pageSize": {"5"}, "fields": {"trackedEntity"}}
		if order != "" {
			q.Set("order", order)
		}
		if pg > 0 {
			q.Set("page", fmt.Sprint(pg))
		}
		return q
	}
	pageOf := func(pg int) string {
		res, err := ix.Search(v, "trackedEntities", query(pg))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res.Items)
		return fmt.Sprint(string(b), " total ", res.Total)
	}
	for _, order = range []string{"", "createdAt:desc"} { // the web app's default and Android's
		var fresh []string
		for pg := 1; pg <= 9; pg++ {
			ix.pages.clear()
			fresh = append(fresh, pageOf(pg))
		}
		ix.pages.clear()
		for pg := 1; pg <= 9; pg++ {
			if got := pageOf(pg); got != fresh[pg-1] {
				t.Fatalf("order %q, page %d read on = %s, from the start = %s", order, pg, got, fresh[pg-1])
			}
		}
		key := fmt.Sprintf("%d|trackedEntities|%s", v.UserID, withoutPage(query(0)))
		if _, ok := ix.pages.get(key, 8); !ok {
			t.Errorf("order %q: no position remembered after page 7, the last full one", order)
		}
	}
}

// pagingFixture stores n people enrolled in one program, created a day apart; the
// user (1) may see all but the first two. It returns the program's search plan.
func pagingFixture(t *testing.T, n int) (*Index, *Viewer, *plan) {
	t.Helper()
	s := testStore(t)
	ix := NewStoreIndex(s)
	const ou, prog, tet = "OuAaaaaaaa1", "PrAaaaaaaa1", "TetAaaaaaa1"
	var visible []uint32
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := 1; i <= n; i++ {
			te := &Obj{UID: fmt.Sprintf("TeAaaaaa%03d", i), Kind: store.KindTrackedEntity, TET: tet, OwnerOrgUnit: ou,
				Origin: store.OriginCentral, SyncState: store.SyncSynced,
				Data: M{"createdAt": time.Date(2024, 1, (i+2)/3, 0, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000")}} // three a day
			enr := &Obj{UID: fmt.Sprintf("EnAaaaaa%03d", i), Kind: store.KindEnrollment, TE: te.UID, Program: prog,
				OwnerOrgUnit: ou, Origin: store.OriginCentral, SyncState: store.SyncSynced, Data: M{"status": "ACTIVE"}}
			for _, o := range []*Obj{te, enr} {
				o.Slot = ix.SlotFor(o.UID)
				if err := s.PutObject(tx, o.ToStore()); err != nil {
					return err
				}
				if i > 2 { // the first two are not the user's
					visible = append(visible, o.Slot)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	changes := map[uint32]bool{}
	for _, sl := range visible {
		changes[sl] = true
	}
	ix.PrepareVisibility(1, changes).Done(true)
	v := &Viewer{UserID: 1, Facility: ou, Access: &access.List{Programs: map[string]access.Perm{prog: {Read: true}},
		TETs: map[string]access.Perm{tet: {Read: true}}}}
	p, apiErr := ix.searchTEs(v, url.Values{"program": {prog}, "orgUnits": {ou}}, prog)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	return ix, v, p
}

// An event program's events paged by event date, newest first (the working lists),
// read on from the last position, are the pages read from the start: the same events
// in the same order, with several on one date.
func TestEventPagesByDateReadOn(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	const ou, prog, stage = "OuAaaaaaaa1", "PrEvAaaaaa1", "StEvAaaaaa1"
	var slots []uint32
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := 1; i <= 40; i++ {
			ev := &Obj{UID: fmt.Sprintf("EvAaaaaa%03d", i), Kind: store.KindEvent, Program: prog, Stage: stage, OwnerOrgUnit: ou,
				Origin: store.OriginCentral, SyncState: store.SyncSynced,
				Data: M{"occurredAt": time.Date(2026, 1, (i+2)/3, 0, 0, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000"),
					"createdAt": "2026-02-01T00:00:00.000", "event": fmt.Sprintf("EvAaaaaa%03d", i), "program": prog, "programStage": stage, "orgUnit": ou}}
			ev.Slot = ix.SlotFor(ev.UID)
			if err := s.PutObject(tx, ev.ToStore()); err != nil {
				return err
			}
			slots = append(slots, ev.Slot)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	changes := map[uint32]bool{}
	for _, sl := range slots {
		changes[sl] = true
	}
	ix.PrepareVisibility(1, changes).Done(true)
	v := &Viewer{UserID: 1, Facility: ou, Access: &access.List{Programs: map[string]access.Perm{prog: {Read: true}},
		Stages: map[string]access.Perm{stage: {Read: true}}},
		Catalog: &access.Catalog{Programs: map[string]*access.ProgramMeta{prog: {ID: prog, Event: true, Stages: []string{stage}}}}}
	query := func(pg int) url.Values {
		return url.Values{"program": {prog}, "orgUnit": {ou}, "order": {"occurredAt:desc"}, "pageSize": {"6"},
			"page": {fmt.Sprint(pg)}, "fields": {"event"}}
	}
	pageOf := func(pg int) string {
		res, err := ix.Search(v, "events", query(pg))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res.Items)
		return string(b)
	}
	var fresh []string
	for pg := 1; pg <= 8; pg++ {
		ix.pages.clear()
		fresh = append(fresh, pageOf(pg))
	}
	ix.pages.clear()
	for pg := 1; pg <= 8; pg++ {
		if got := pageOf(pg); got != fresh[pg-1] {
			t.Fatalf("page %d read on = %s, from the start = %s", pg, got, fresh[pg-1])
		}
	}
	if !strings.Contains(fresh[0], "EvAaaaaa040") {
		t.Fatalf("newest first: %s", fresh[0])
	}
}
