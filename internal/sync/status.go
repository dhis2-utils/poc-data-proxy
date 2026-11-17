package sync

import (
	"cmp"
	"context"
	"encoding/json"
	"github.com/dhis2/facility-hub/internal/diskfree"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/devkey"
	"github.com/dhis2/facility-hub/internal/hubtls"
	"github.com/dhis2/facility-hub/internal/store"
)

// --- heartbeat (spec 11.4) -------------------------------------------------------------

func (w *Worker) heartbeat(ctx context.Context) {
	h := w.H
	h.NoteCycle(h.Store.CountObjects(store.SyncPending), diskfree.Free(h.Cfg.DataDir))
	if !w.lastBeat.IsZero() && time.Since(w.lastBeat) < h.Cfg.HeartbeatInterval {
		return
	}
	h.CheckProtection(ctx)
	if err := h.Central.JSON(ctx, "PUT", "/api/dataStore/facility-hub-status/"+h.HubID(), Status(h), h.HubAuth(), nil); err != nil {
		slog.Warn("heartbeat", "err", err)
		return
	}
	w.lastBeat = time.Now()
}

// Status is the heartbeat payload. It holds no patient data.
func Status(h *core.Hub) map[string]any {
	refused, refusedGroups := refusedCounts(h)
	_, _, lastSync, lastErr := h.Stats.Get()
	misses, missTotal := h.ReqLog.OfflineMisses(10)
	lastPush, lastPull := h.LastPushPull()
	var upgrade any
	json.Unmarshal([]byte(h.Store.Meta(core.MetaUpgrade)), &upgrade)
	return map[string]any{
		"hubId": h.HubID(), "facility": h.Facility(), "version": h.Version, "serverVersion": h.ServerVersion(),
		"uptimeSeconds": int(time.Since(h.Started).Seconds()), "lastPush": lastPush, "lastPull": lastPull,
		"lastSync": lastSync, "lastError": lastErr, "pending": h.Store.CountObjects(store.SyncPending),
		// oldestPending: when the oldest waiting save was made (the app's "pending" flag).
		"rejected": h.Store.CountObjects(store.SyncRejected), "pendingOutbox": h.Store.CountOutbox(store.OutboxPending),
		"oldestPending": h.Store.OldestPendingOutbox(), "stuckOutbox": h.StuckOutbox(),
		"retired": h.Retired(), "superseded": h.Store.CountObjects(store.SyncSuperseded),
		"issues": h.Store.CountIssues(), "diskFreeBytes": diskfree.Free(h.Cfg.DataDir), "lastUpgrade": upgrade,
		"needsUpgrade": h.API() == nil && h.ServerVersion() != "", "time": time.Now().UTC().Format(time.RFC3339),
		// Whose token the hub's housekeeping uses, so the Facility Hub app can list the
		// hubs that depend on a user who is being disabled.
		"hubTokenOwner": h.Runtime().TokenOwner(), "hubTokenExpires": hubTokenExpires(h),
		"refused": refused, "refusedGroups": refusedGroups, "retriesNotApplied": retriesNotApplied(h),
		// The dataStore entries the hub acts on that any user may write: until a country
		// protects them, anyone could retire the hub or announce a move (not followed).
		"unprotected": h.Unprotected(),
		// The groups central left out of the registry entry's sharing: the set-up account
		// cannot see them, and without the hub accounts group the hub's account cannot
		// write its entry.
		"registryDropped": h.RegistryDropped(),
		// The instructions on central the hub ignored: not signed for it (review R-07).
		"unsigned": h.Unsigned(),
		// What staff asked for offline that the hub could not answer ("needs connection":
		// a screen nobody opened online), since the hub started: the 10 most frequent,
		// by endpoint (identifiers as {uid}, parameter names without values).
		"offlineMisses": misses, "offlineMissesTotal": missTotal,
		// Whether the hub's keys are sealed with a TPM (docs/DEPLOYMENT.md, "TPM"): a
		// country sees which hubs rely on staff passwords alone.
		"deviceKey": devkey.Key() != nil,
		// The USB stick (docs/RECOVERY.md, C2): central sees a hub without a working one.
		"usb": usbStatus(h),
		// How devices reach the hub (docs/DEPLOYMENT.md, "Reaching the hub securely"): its
		// addresses where dhis2.local fails, and the authority's fingerprint to compare.
		"addresses": hubtls.Addresses(), "caFingerprint": hubtls.Fingerprint(h.Cfg.DataDir),
		// How the hub has lived, a summary per day for 90 days (history.go).
		"history": h.History(),
		// The facility's generated attributes (id, pattern), so the Facility Hub app
		// can set each one's reserved-value pool (facility-hub-config/reserved).
		"generatedAttributes": generatedAttributes(h),
		// Users whose device clock ran more than half an hour ahead of central's at
		// their latest upload (core.ClientClockAhead): their dates may be refused.
		"clientClocksAhead": len(h.ClientClocksAhead()),
	}
}

// generatedAttributes lists the catalog's generated attributes for the heartbeat.
func generatedAttributes(h *core.Hub) []map[string]string {
	var out []map[string]string
	for _, a := range h.Catalog().GeneratedAttributes() {
		out = append(out, map[string]string{"id": a.ID, "pattern": a.Pattern})
	}
	return out
}

// refusedCounts counts the changes central refused that are not resolved, by kind,
// program and error code: with hundreds stuck after a metadata change, central sees
// the pattern without reading each issue. Codes only, no values. Only open issues are
// read (and decrypted), so the cost follows what is stuck, not the hub's history.
// It returns at most 100 groups, and how many there are.
func refusedCounts(h *core.Hub) ([]map[string]any, int) {
	issues, err := h.Store.UnresolvedIssues(100000)
	if err != nil || len(issues) == 0 {
		return nil, 0
	}
	counts, superseded := map[[3]string]int{}, map[[3]string]int{}
	for _, is := range issues {
		program, codes, _, sup := issueGroup(h, is)
		for c := range codes {
			counts[[3]string{is.Kind, program, c}]++
			if sup {
				superseded[[3]string{is.Kind, program, c}]++
			}
		}
	}
	keys := slices.SortedFunc(maps.Keys(counts), func(a, b [3]string) int {
		return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1]), cmp.Compare(a[2], b[2]))
	})
	var out []map[string]any
	for _, k := range keys[:min(len(keys), 100)] {
		g := map[string]any{"kind": k[0], "program": k[1], "code": k[2], "count": counts[k]}
		if superseded[k] > 0 {
			// Of count, those whose record central changed after the refusal: the hub
			// shows central's version, and the refused change waits for Resend or Discard.
			g["superseded"] = superseded[k]
		}
		out = append(out, g)
	}
	return out, len(keys)
}

// retriesNotApplied lists the resend instructions this hub did not apply at all, and
// why (a user to send as whom it cannot send for), for the Facility Hub app.
func retriesNotApplied(h *core.Hub) []map[string]string {
	var m map[string]string
	json.Unmarshal([]byte(h.Store.Meta(metaRetriesNotApplied)), &m)
	out := []map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(m)) {
		out = append(out, map[string]string{"id": id, "reason": m[id]})
	}
	return out
}

func usbStatus(h *core.Hub) any {
	if h.USBStatus == nil {
		return nil
	}
	return h.USBStatus()
}

func hubTokenExpires(h *core.Hub) any {
	if rt := h.Runtime(); rt != nil && rt.HubTokenExp != "" {
		return rt.HubTokenExp
	}
	return nil
}
