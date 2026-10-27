package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"reflect"
	"slices"
	"time"

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
	// Everything the batch refers to, read in one go, with the payloads merges need.
	var refs []string
	for _, it := range items {
		refs = append(refs, it.UID, it.TE, it.Enrollment, it.From.UID, it.To.UID)
	}
	known := h.Index.GetMany(slices.DeleteFunc(refs, func(u string) bool { return u == "" }))
	get := func(uid string) *tracker.Obj { return known[uid] }
	var olds []*tracker.Obj
	for _, it := range items {
		if o := known[it.UID]; o != nil {
			olds = append(olds, o)
		}
	}
	fulls := h.Index.FullMany(olds)
	batch := map[string]*tracker.Obj{}
	owned := func(uid string) bool {
		if o := batch[uid]; o != nil {
			return true
		}
		o := get(uid)
		return o != nil && o.OwnerOrgUnit == facility
	}
	// Program owners as central reports them in this answer (programOwners), else as
	// last known. An enrollment owned elsewhere is not the facility's, even when the
	// person was registered or enrolled here: a user may have read it online through
	// temporary ownership ("break the glass"), which must not outlive the connection.
	owners := map[string]string{} // te|program -> org unit
	for _, it := range items {
		if it.Kind == store.KindTrackedEntity {
			for _, po := range listOf(it.Data["programOwners"]) {
				owners[it.UID+"|"+tracker.Str(po, "program")] = tracker.Str(po, "orgUnit")
			}
		}
	}
	ownerOf := func(te, program string) string {
		if ou, ok := owners[te+"|"+program]; ok {
			return ou
		}
		return tracker.ProgramOwner(get(te), program)
	}
	stubbed := h.stubOld(items, get)
	skipped := map[string]bool{}
	var objs []*tracker.Obj
	for _, it := range items {
		if !tracker.ValidUID(it.UID) {
			continue
		}
		old := get(it.UID)
		o := &tracker.Obj{UID: it.UID, Kind: it.Kind, Origin: store.OriginCentral, SyncState: store.SyncSynced,
			OwnerOrgUnit: facility}
		if old != nil {
			o.Slot = old.Slot
		}
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
			if forceFacility {
				break
			}
			if owner := ownerOf(it.TE, o.Program); (owner != "" && owner != facility) ||
				(owner == "" && !(ou == facility || owned(it.TE))) {
				skipped[it.UID] = true
				continue
			}
		case store.KindEvent:
			if stubbed[it.Enrollment] {
				continue // under an enrollment kept as a stub (tracker retention)
			}
			o.TE, o.Enrollment = it.TE, it.Enrollment
			o.Program, o.Stage = tracker.Str(it.Data, "program"), tracker.Str(it.Data, "programStage")
			if o.TE == "" {
				if e := batch[o.Enrollment]; e != nil {
					o.TE = e.TE
				} else if e := get(o.Enrollment); e != nil {
					o.TE = e.TE
				}
			}
			if forceFacility {
				break
			}
			if e := get(it.Enrollment); skipped[it.Enrollment] || (e != nil && e.OwnerOrgUnit != facility) ||
				!(ou == facility || owned(it.Enrollment)) {
				continue
			}
		case store.KindRelationship:
			if !(forceFacility || owned(it.From.UID) || owned(it.To.UID)) {
				continue
			}
		}
		if old != nil {
			if old.SyncState == store.SyncPending || (old.SyncState == store.SyncRejected && !h.changedSinceRefused(old.UID, it.Data)) {
				// Local version wins until pushed; only record that central showed it.
				objs = append(objs, &tracker.Obj{UID: old.UID, Kind: "visibility-only", Slot: old.Slot})
				batch[it.UID] = old
				continue
			}
			if old.Stub || it.Data["hubStub"] == true || old.SyncState == store.SyncRejected {
				// A stub never merges: merging would keep the old fields or the stub mark.
				// Nor does central's version of a refused object: the local data is what
				// central refused.
				o.Data = it.Data
			} else {
				full := fulls[old.UID]
				if full == nil {
					full = h.Index.Full(old)
				}
				o.Data = tracker.Merge(full, it.Data)
				if old.SyncState == store.SyncSynced && old.OwnerOrgUnit == o.OwnerOrgUnit && reflect.DeepEqual(o.Data, full) {
					// Nothing new: only the user's visibility. Rewriting every object of
					// every online read wrote about 250 MB a minute on the Pis
					// (docs/PERFORMANCE.md, History, 2026-10-27).
					objs = append(objs, &tracker.Obj{UID: old.UID, Kind: "visibility-only", Slot: old.Slot})
					batch[it.UID] = old
					continue
				}
			}
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
// params are the client's import flags to send along on push (store.OutboxEntry.Params).
func (h *Hub) PersistImport(userID int64, out *tracker.ImportOutcome, params string) (int64, error) {
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
		seq, err = h.Store.AppendOutbox(tx, userID, payload, params, out.Refs, dep)
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
// programs (and objects without one) that a full pull did not list (spec 11.3). It
// starts from the user's bitmap in memory, so only the objects it may remove are
// read: scanning the store for each user's full pull read the whole data file.
func (h *Hub) RemoveVisibilityExcept(userID int64, programs []string, seen map[uint32]bool) int {
	var cands []int64
	for _, slot := range h.Index.VisibleSlots(userID) {
		if !seen[slot] {
			cands = append(cands, int64(slot))
		}
	}
	if len(cands) == 0 {
		return 0
	}
	objs, err := h.Store.SlotOrigins(cands)
	if err != nil {
		slog.Warn("removing visibility", "user", userID, "err", err)
		return 0
	}
	changes := map[uint32]bool{}
	for slot, o := range objs {
		if o[0] != store.OriginCentral || (o[1] != "" && !slices.Contains(programs, o[1])) {
			continue
		}
		changes[uint32(slot)] = false
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

// MarkVisible records that central showed objects already held to the user (the
// ID-list pull, spec 11.3), by their slots.
func (h *Hub) MarkVisible(userID int64, slots []uint32) error {
	changes := map[uint32]bool{}
	for _, s := range slots {
		if s != 0 && !h.Index.VisibleTo(&tracker.Obj{Slot: s}, userID) {
			changes[s] = true
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
	return h.setVisibilities(map[int64]map[uint32]bool{userID: changes})
}

// setVisibilities stores and applies several users' visibility changes in one
// transaction.
func (h *Hub) setVisibilities(changes map[int64]map[uint32]bool) error {
	cs := h.Index.PrepareVisibilities(changes)
	err := h.Store.Tx(context.Background(), func(tx *sql.Tx) error { return cs.Write(h.Store, tx) })
	cs.Done(err == nil)
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
	all := map[int64]map[uint32]bool{}
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
			all[u.ID] = changes
		}
	}
	if len(all) > 0 {
		if err := h.setVisibilities(all); err != nil {
			slog.Warn("sharing by rules", "users", len(all), "err", err)
		}
	}
}

// ApplyTransfer records that the facility no longer owns the tracked entity in the
// program (a transfer out, through the hub or found by the ownership check): the
// enrollments and their events are marked as owned by owner ("" when unknown), which
// hides them from everyone offline, and every user's visibility of them is cleared.
func (h *Hub) ApplyTransfer(teUID, program, owner string) error {
	if owner == h.Facility() {
		return nil // a transfer in: the next pull brings it
	}
	var objs []*tracker.Obj
	if te := h.Index.Get(teUID); te != nil && owner != "" {
		objs = append(objs, te.WithData(tracker.SetProgramOwner(h.Index.Full(te), teUID, program, owner)))
	}
	slots := map[uint32]bool{}
	for _, e := range h.Index.EnrollmentsOf(teUID) {
		if e.Program != program {
			continue
		}
		related := append([]*tracker.Obj{e}, h.Index.EventsOf(e.UID)...)
		for _, x := range related {
			n := x.WithData(h.Index.Full(x))
			n.OwnerOrgUnit = owner
			objs = append(objs, n)
			slots[x.Slot] = false
		}
	}
	err := h.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		for _, o := range objs {
			if err := h.Store.PutObject(tx, o.ToStore()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, o := range objs {
		h.Index.Put(o)
	}
	users, err := h.Store.Users()
	if err != nil {
		return err
	}
	all := map[int64]map[uint32]bool{}
	for _, u := range users {
		all[u.ID] = slots
	}
	if err := h.setVisibilities(all); err != nil {
		return err
	}
	slog.Info("ownership moved away", "trackedEntity", teUID, "program", program, "owner", owner, "objects", len(objs))
	return nil
}

// stubOld replaces, among items central returned, each closed enrollment whose last
// change is before its program's retention cut-off by its stub (tracker retention),
// and returns those it replaced: their events are not stored. Central sends them with
// a person fetched for another reason, or to an online read. An enrollment something
// unsynced refers to (an offline edit of one of its events, a relationship) is kept in
// full until that is pushed.
func (h *Hub) stubOld(items []*tracker.Item, get func(string) *tracker.Obj) map[string]bool {
	out := map[string]bool{}
	cat, ret, now := h.Catalog(), h.Retention(), h.CentralNow()
	if cat == nil {
		return out
	}
	for _, it := range items {
		program := tracker.Str(it.Data, "program")
		if it.Kind != store.KindEnrollment || it.Data["hubStub"] == true || cat.Programs[program] == nil ||
			cat.Programs[program].Event || tracker.Str(it.Data, "status") == "ACTIVE" {
			continue
		}
		cutoff, ok := ret.Cutoff(program, now)
		if !ok || tracker.Str(it.Data, "updatedAt") >= cutoff || h.referenced(it.UID, get) {
			continue
		}
		it.Data = tracker.StubOf(it.Data)
		out[it.UID] = true
	}
	return out
}

// referenced reports whether something unsynced refers to a stored enrollment: the
// enrollment itself, an event under it, or a relationship with it.
func (h *Hub) referenced(uid string, get func(string) *tracker.Obj) bool {
	if o := get(uid); o != nil && o.SyncState != store.SyncSynced {
		return true
	}
	for _, o := range slices.Concat(h.Index.EventsOf(uid), h.Index.RelationshipsOf(uid)) {
		if o.SyncState != store.SyncSynced {
			return true
		}
	}
	return false
}

// changedSinceRefused reports whether central's version of an object the hub holds as
// refused changed after central refused the hub's: someone fixed or changed it on
// central, and central's version wins, which resolves the issue. One second of margin:
// the refusal time comes from central's Date header, in whole seconds. If an
// unrelated change on central comes after the refusal, the refused values disappear
// from offline reads too; they stay in the refused outbox entry and the issue
// (docs/FINDINGS.md, "Refused changes").
func (h *Hub) changedSinceRefused(uid string, data tracker.M) bool {
	at, err := time.Parse(time.RFC3339Nano, h.Store.RefusedAt(uid))
	if err != nil {
		return false
	}
	upd, err := time.ParseInLocation(tracker.DHIS2Layout, tracker.Str(data, "updatedAt"), h.centralZone())
	if err != nil {
		return false
	}
	return upd.After(at.Add(time.Second))
}
