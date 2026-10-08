package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// UpsertCentral stores objects central returned to a user (pull or online read) with
// origin=central and adds the user to their visibility set (spec 8.2, 11.3).
// Only objects owned by the facility are stored; forceFacility is set for pulls,
// where central already scoped the query to the facility. A pending local version
// wins until it is pushed. It returns the UIDs stored.
func (h *Hub) UpsertCentral(userID int64, payload tracker.M, forceFacility bool) ([]string, error) {
	facility := h.Facility()
	if facility == "" || !h.Store.Unlocked() {
		return nil, nil
	}
	items := tracker.Flatten(payload)
	batch := map[string]*tracker.Obj{}
	owned := func(uid string) bool {
		if o := batch[uid]; o != nil {
			return true
		}
		o := h.Index.Get(uid)
		return o != nil && o.OwnerOrgUnit == facility
	}
	var objs []*tracker.Obj
	for _, it := range items {
		if !tracker.ValidUID(it.UID) {
			continue
		}
		old := h.Index.Get(it.UID)
		o := &tracker.Obj{UID: it.UID, Kind: it.Kind, Origin: store.OriginCentral, SyncState: store.SyncSynced,
			OwnerOrgUnit: facility}
		ou := tracker.Str(it.Data, "orgUnit")
		switch it.Kind {
		case store.KindTrackedEntity:
			o.TET = tracker.Str(it.Data, "trackedEntityType")
			ok := forceFacility || ou == facility
			for _, po := range listOf(it.Data["programOwners"]) {
				if tracker.Str(po, "orgUnit") == facility {
					ok = true
				}
			}
			if !ok {
				continue
			}
		case store.KindEnrollment:
			o.TE, o.Program = it.TE, tracker.Str(it.Data, "program")
			if !(forceFacility || ou == facility || owned(it.TE)) {
				continue
			}
		case store.KindEvent:
			o.TE, o.Enrollment = it.TE, it.Enrollment
			o.Program, o.Stage = tracker.Str(it.Data, "program"), tracker.Str(it.Data, "programStage")
			if o.TE == "" {
				if e := batch[o.Enrollment]; e != nil {
					o.TE = e.TE
				} else if e := h.Index.Get(o.Enrollment); e != nil {
					o.TE = e.TE
				}
			}
			if !(forceFacility || ou == facility || owned(it.Enrollment)) {
				continue
			}
		case store.KindRelationship:
			if !(forceFacility || owned(it.From.UID) || owned(it.To.UID)) {
				continue
			}
		}
		if old != nil {
			if old.SyncState == store.SyncPending || old.SyncState == store.SyncRejected {
				// Local version wins until pushed; only record that central showed it.
				objs = append(objs, &tracker.Obj{UID: old.UID, Kind: "visibility-only", Slot: old.Slot})
				batch[it.UID] = old
				continue
			}
			o.Data = tracker.Merge(h.Index.Full(old), it.Data)
			if o.TET == "" {
				o.TET = old.TET
			}
		} else {
			o.Data = it.Data
		}
		o.UpdatedAt = tracker.Str(o.Data, "updatedAt")
		batch[it.UID] = o
		objs = append(objs, o)
	}
	changes := map[uint32]bool{}
	for _, o := range objs {
		if o.Slot == 0 {
			o.Slot = h.Index.SlotFor(o.UID)
		}
		changes[o.Slot] = true
	}
	vc := h.Index.PrepareVisibility(userID, changes)
	err := h.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, o := range objs {
			if o.Kind != "visibility-only" {
				if err := h.Store.PutObject(tx, o.ToStore()); err != nil {
					return err
				}
			}
		}
		return vc.Write(h.Store, tx)
	})
	vc.Done(err == nil)
	if err != nil {
		return nil, err
	}
	var uids []string
	for _, o := range objs {
		if o.Kind != "visibility-only" {
			h.Index.Put(o)
		}
		uids = append(uids, o.UID)
	}
	return uids, nil
}

func listOf(v any) []tracker.M {
	arr, _ := v.([]any)
	var out []tracker.M
	for _, x := range arr {
		if m, ok := x.(tracker.M); ok {
			out = append(out, m)
		}
	}
	return out
}

// PersistImport writes accepted offline objects and one outbox entry (spec 10.2
// steps 3-4). The entry depends on the latest pending entry that holds any object it
// references or updates.
func (h *Hub) PersistImport(userID int64, out *tracker.ImportOutcome) (int64, error) {
	if len(out.Accepted) == 0 {
		return 0, nil
	}
	payload, err := json.Marshal(out.Flat)
	if err != nil {
		return 0, err
	}
	var seq int64
	err = h.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		refs := slices.Clone(out.Parents)
		for _, r := range out.Refs {
			refs = append(refs, r.UID)
		}
		dep := h.Store.LatestPendingSeqFor(tx, refs)
		for _, o := range out.Accepted {
			if o.Slot == 0 {
				o.Slot = h.Index.SlotFor(o.UID)
			}
			if err := h.Store.PutObject(tx, o.ToStore()); err != nil {
				return err
			}
		}
		seq, err = h.Store.AppendOutbox(tx, userID, payload, out.Refs, dep)
		return err
	})
	if err != nil {
		return 0, err
	}
	for _, o := range out.Accepted {
		h.Index.Put(o)
	}
	return seq, nil
}

// RemoveVisibilityExcept drops userID from central-origin objects of the given
// programs that a full pull did not return (spec 11.3).
func (h *Hub) RemoveVisibilityExcept(userID int64, programs []string, seen map[string]bool) int {
	changes := map[uint32]bool{}
	for _, kind := range []string{store.KindTrackedEntity, store.KindEnrollment, store.KindEvent, store.KindRelationship} {
		for _, o := range h.Index.All(kind) {
			if o.Origin != store.OriginCentral || seen[o.UID] || !h.Index.VisibleTo(o, userID) {
				continue
			}
			if o.Program != "" && !slices.Contains(programs, o.Program) {
				continue
			}
			changes[o.Slot] = false
		}
	}
	if len(changes) == 0 {
		return 0
	}
	if err := h.setVisibility(userID, changes); err != nil {
		slog.Warn("removing visibility", "user", userID, "err", err)
		return 0
	}
	return len(changes)
}

// MarkVisible records that central showed these objects, already held, to the user
// (the ID-list pull, spec 11.3). Unknown UIDs are skipped.
func (h *Hub) MarkVisible(userID int64, uids []string) error {
	changes := map[uint32]bool{}
	for _, uid := range uids {
		if o := h.Index.Get(uid); o != nil && o.Slot != 0 && !h.Index.VisibleTo(o, userID) {
			changes[o.Slot] = true
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return h.setVisibility(userID, changes)
}

// RecordScopes notes that these tracked entities were fetched in full for scope.
func (h *Hub) RecordScopes(tes []string, scope string) error {
	err := h.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, te := range tes {
			if err := h.Store.AddTEScope(tx, te, scope); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		h.Index.AddScopes(tes, scope)
	}
	return err
}

// setVisibility stores and applies one user's visibility changes.
func (h *Hub) setVisibility(userID int64, changes map[uint32]bool) error {
	vc := h.Index.PrepareVisibility(userID, changes)
	err := h.Store.Tx(context.Background(), func(tx *sql.Tx) error { return vc.Write(h.Store, tx) })
	vc.Done(err == nil)
	return err
}

// ShareByRules adds every user who may read an object under the local-object rules
// (spec 8.2) to its visibility set. Used for objects the hub learns from online
// writes, so other users see them offline before their own next pull.
func (h *Hub) ShareByRules(uids []string) {
	users, err := h.Store.Users()
	if err != nil {
		return
	}
	for _, u := range users {
		v := h.Viewer(u.ID)
		if !v.Access.InFacility(h.FacilityPath()) {
			continue
		}
		changes := map[uint32]bool{}
		for _, uid := range uids {
			o := h.Index.Get(uid)
			if o == nil || o.Slot == 0 || h.Index.VisibleTo(o, u.ID) {
				continue
			}
			probe := *o
			probe.Origin = store.OriginLocal
			if h.Index.CanRead(v, &probe) {
				changes[o.Slot] = true
			}
		}
		if len(changes) > 0 {
			if err := h.setVisibility(u.ID, changes); err != nil {
				slog.Warn("sharing by rules", "user", u.Username, "err", err)
			}
		}
	}
}
