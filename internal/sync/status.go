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
		"rejected": h.Store.CountObjects(store.SyncRejected), "pendingOutbox": h.Store.CountOutbox(store.OutboxPending),
		"stuckOutbox": h.StuckOutbox(), "retired": h.Retired(),
		"issues": h.Store.CountIssues(), "diskFreeBytes": diskfree.Free(h.Cfg.DataDir), "lastUpgrade": upgrade,
		"needsUpgrade": h.API() == nil && h.ServerVersion() != "", "time": time.Now().UTC().Format(time.RFC3339),
		// Whose token the hub's housekeeping uses, so the Facility Hub app can list the
		// hubs that depend on a user who is being disabled.
		"hubTokenOwner": h.Runtime().TokenOwner(), "hubTokenExpires": hubTokenExpires(h),
		"refused": refused, "refusedGroups": refusedGroups,
		// What staff asked for offline that the hub could not answer ("needs connection":
		// a screen nobody opened online), since the hub started: the 10 most frequent,
		// by endpoint (identifiers as {uid}, parameter names without values).
		"offlineMisses": misses, "offlineMissesTotal": missTotal,
		// The USB stick (docs/RECOVERY.md, C2): central sees a hub without a working one.
		"usb": usbStatus(h),
		// How devices reach the hub (docs/DEPLOYMENT.md, "Reaching the hub securely"): its
		// addresses where dhis2.local fails, and the authority's fingerprint to compare.
		"addresses": hubtls.Addresses(), "caFingerprint": hubtls.Fingerprint(h.Cfg.DataDir),
		// How the hub has lived, a summary per day for 90 days (history.go).
		"history": h.History(),
	}
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
	counts := map[[3]string]int{}
	for _, is := range issues {
		program, codes, _ := issueGroup(h, is)
		for c := range codes {
			counts[[3]string{is.Kind, program, c}]++
		}
	}
	keys := slices.SortedFunc(maps.Keys(counts), func(a, b [3]string) int {
		return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1]), cmp.Compare(a[2], b[2]))
	})
	var out []map[string]any
	for _, k := range keys[:min(len(keys), 100)] {
		out = append(out, map[string]any{"kind": k[0], "program": k[1], "code": k[2], "count": counts[k]})
	}
	return out, len(keys)
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
