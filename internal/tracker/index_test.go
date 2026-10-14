package tracker

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"

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
