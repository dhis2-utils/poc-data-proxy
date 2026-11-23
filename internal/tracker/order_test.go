package tracker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/store"
)

// Events ordered by scheduledAt, some without one: central (2.42) sorts by the date,
// those without one after the rest ascending and before descending (as its database
// sorts NULL), ties newest first. The hub took "scheduledAt", which has a UID's shape,
// for a data element and ignored the order (the oracle on Sierra Leone 2.42, 17
// November 2026).
func TestEventsOrderedByScheduledAt(t *testing.T) {
	s := testStore(t)
	ix := NewStoreIndex(s)
	const ou, prog, stage = "OuAaaaaaaa1", "PrEvAaaaaa1", "StEvAaaaaa1"
	var slots []uint32
	err := s.Tx(context.Background(), func(tx *sql.Tx) error {
		for i := 1; i <= 4; i++ {
			d := M{"createdAt": fmt.Sprintf("2026-02-0%dT00:00:00.000", i), "event": fmt.Sprintf("EvAaaaaa%03d", i), "program": prog, "programStage": stage, "orgUnit": ou}
			if i <= 2 {
				d["scheduledAt"] = fmt.Sprintf("2025-05-2%dT00:00:00.000", i)
			}
			ev := &Obj{UID: fmt.Sprintf("EvAaaaaa%03d", i), Kind: store.KindEvent, Program: prog, Stage: stage, OwnerOrgUnit: ou,
				Origin: store.OriginCentral, SyncState: store.SyncSynced, Data: d}
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
	ch := map[uint32]bool{}
	for _, sl := range slots {
		ch[sl] = true
	}
	ix.PrepareVisibility(1, ch).Done(true)
	v := &Viewer{UserID: 1, Facility: ou, Access: &access.List{Programs: map[string]access.Perm{prog: {Read: true}},
		Stages: map[string]access.Perm{stage: {Read: true}}},
		Catalog: &access.Catalog{Programs: map[string]*access.ProgramMeta{prog: {ID: prog, Event: true, Stages: []string{stage}}}}}
	want := map[string]string{
		"scheduledAt:asc":  `[{"event":"EvAaaaaa001"},{"event":"EvAaaaaa002"},{"event":"EvAaaaaa004"},{"event":"EvAaaaaa003"}]`,
		"scheduledAt:desc": `[{"event":"EvAaaaaa004"},{"event":"EvAaaaaa003"},{"event":"EvAaaaaa002"},{"event":"EvAaaaaa001"}]`,
	}
	for _, o := range []string{"scheduledAt:asc", "scheduledAt:desc"} {
		for _, pg := range []string{"", "1"} {
			q := url.Values{"program": {prog}, "orgUnit": {ou}, "order": {o}, "pageSize": {"20"}, "fields": {"event"}}
			if pg != "" {
				q.Set("page", pg)
			}
			res, err := ix.Search(v, "events", q)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(res.Items)
			if string(b) != want[o] {
				t.Errorf("%s (page %q) = %s, want %s", o, pg, b, want[o])
			}
		}
	}
}
