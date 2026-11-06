package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// Metadata answered from the hub while online (proxy.localCopy): with programs listed
// in localReads, the hub answers a user's metadata and settings requests from their
// own copy, as offline, but only from a copy fetched after MetadataCutoff. The cutoff
// moves when the hub learns that something changed:
//   - the hourly check finds metadata changed on central (CheckMetadata);
//   - a hub manager presses "Refresh metadata on all hubs" in the Facility Hub app
//     (facility-hub-config/metadataReload, read at every sync: CheckMetadataReload);
//   - someone presses "Refresh metadata now" on the hub page;
//   - a write passes through the hub to central (a setting, a data store entry);
//   - for one user, their access changes (SetAccess).
//
// Offline nothing changes: the hub serves whatever copy it has.

const (
	metaChangedKey  = "meta_changed_at" // hub time; copies before it are not answered online
	metaCheckedKey  = "meta_checked_at" // central time of the last check that went through
	metaReloadKey   = "meta_reload_seen"
	accessChangedAt = "access_changed_at:"

	metaCheckOverlap = 10 * time.Minute
)

// MetadataChanged moves the cutoff to now for everyone.
func (h *Hub) MetadataChanged(why string) {
	slog.Info("metadata changed: copies are fetched again online", "why", why)
	h.Store.SetPrivate(metaChangedKey, store.Now())
}

// MetadataCutoff is the time a user's copy must be newer than to be answered online.
func (h *Hub) MetadataCutoff(userID int64) time.Time {
	t := store.ParseTime(h.Store.Private(metaChangedKey))
	if u := store.ParseTime(h.Store.Private(accessChangedAt + strconv.FormatInt(userID, 10))); u.After(t) {
		t = u
	}
	return t
}

// MetadataTimes: when metadata was last checked on central (central time) and when
// the hub last saw a change (hub time), for the hub page.
func (h *Hub) MetadataTimes() (checked, changed string) {
	return h.Store.Private(metaCheckedKey), h.Store.Private(metaChangedKey)
}

// metadataTypes are the metadata the apps load for tracker and aggregate forms. The
// check asks which of them changed since the last one, as a member of each access
// group (central answers per sharing). Central reads each type's whole table for it
// (no index on lastUpdated), so large tables that forms do not need are left out:
// org units, category option combos (they follow their categories). Not seen:
// deletions (central lists them only to superusers, /api/deletedObjects) and
// assignments of programs and data sets to org units (they change no lastUpdated,
// checked on 2.43, 6 November); the national refresh covers both, and a change in the
// facility's programs is seen by the catalog refresh.
const metadataTypes = "programs=true&programStages=true&programSections=true&programStageSections=true" +
	"&programRules=true&programRuleActions=true&programRuleVariables=true&programIndicators=true" +
	"&optionSets=true&options=true&optionGroups=true&dataElements=true&trackedEntityAttributes=true" +
	"&trackedEntityTypes=true&relationshipTypes=true&dataSets=true&sections=true" +
	"&categoryCombos=true&categories=true&categoryOptions=true"

// CheckMetadata asks central, as one member of each group, what metadata changed
// since the last check; any change moves the cutoff. The first check only records
// the time.
func (h *Hub) CheckMetadata(ctx context.Context, members []central.Auth) error {
	// In central's zone, as its lastUpdated values are; ten minutes back for a hub
	// clock ahead of central's (a change seen twice only refreshes copies twice).
	start := h.CentralNow().Add(-metaCheckOverlap).Format(tracker.DHIS2Layout)
	since := h.Store.Private(metaCheckedKey)
	changed := false
	if since != "" {
		for _, a := range members {
			if changed {
				break
			}
			var out map[string]json.RawMessage
			err := h.Central.JSON(ctx, "GET", "/api/metadata?"+metadataTypes+"&fields=id&filter=lastUpdated:gt:"+since, nil, a, &out)
			if err != nil {
				return err
			}
			for k, v := range out {
				var list []json.RawMessage
				if k != "system" && json.Unmarshal(v, &list) == nil && len(list) > 0 {
					h.MetadataChanged("changed on central: " + k)
					changed = true
					break
				}
			}
		}
	}
	h.Store.SetPrivate(metaCheckedKey, start)
	return nil
}

// CheckMetadataReload reads facility-hub-config/metadataReload ({"at": ...}), which
// the Facility Hub app writes; a value the hub has not seen moves the cutoff.
func (h *Hub) CheckMetadataReload(ctx context.Context) error {
	var v struct {
		At string `json:"at"`
	}
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-config/metadataReload", nil, h.HubAuth(), &v)
	if central.Status(err) == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if v.At != "" && v.At != h.Store.Private(metaReloadKey) {
		h.MetadataChanged("refresh on all hubs, " + v.At)
		h.Store.SetPrivate(metaReloadKey, v.At)
	}
	return nil
}

// accessChanged moves one user's cutoff: their copies were fetched with their old
// access (sharing decides what central answers them).
func sortedIDs(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func (h *Hub) accessChanged(userID int64) {
	h.Store.SetPrivate(accessChangedAt+strconv.FormatInt(userID, 10), store.Now())
}
