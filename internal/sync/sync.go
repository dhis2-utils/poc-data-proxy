// Package sync is the sync worker (spec 11): outbox push, issue reporting, pull,
// reserved value pool refill and heartbeat. It runs every sync_interval while online,
// immediately on the switch from offline to online, and on "sync now".
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"math/rand/v2"
	"strings"
	stdsync "sync"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

type Worker struct {
	H *core.Hub
	// AfterCycle runs at the end of each online cycle (the upgrade check, spec 12a).
	AfterCycle func(ctx context.Context)

	mu     stdsync.Mutex // one cycle at a time
	pushMu stdsync.Mutex // one push at a time: Cycle, the upgrade drain and reset all push

	lastRefresh time.Time // last access and catalog refresh (Config.AccessRefreshInterval)
	lastBeat    time.Time // last heartbeat sent (Config.HeartbeatInterval)
}

// Exclusive runs f while no sync cycle runs, and keeps cycles out until f returns.
func (w *Worker) Exclusive(f func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return f()
}

// listPageSize is the page size of the ID listing; fetchBatch how many tracked
// entities one full fetch asks for (the URL holds their UIDs).
const (
	listPageSize = 1000
	fetchBatch   = 50
)

// Run loops until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	w.H.Monitor.OnChange(func(online, _ bool) {
		if online {
			// A random wait first: after an outage of central, every hub reconnects
			// at once.
			go func() {
				if d := w.H.Cfg.ReconnectDelay; d > 0 {
					time.Sleep(rand.N(d))
				}
				w.H.TriggerSync()
			}()
		}
	})
	// Each wait is the interval ±10 %, so hubs started together drift apart.
	next := func() time.Duration {
		i := w.H.Cfg.SyncInterval
		return i - i/10 + rand.N(i/5+1)
	}
	t := time.NewTimer(next())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.H.SyncNow:
		}
		t.Reset(next())
		if err := w.Cycle(ctx); err != nil {
			slog.Warn("sync cycle", "err", err)
		}
	}
}

// refreshAccess re-reads the program catalog and every user's access from central.
// A network error ends the cycle; others are added to errs.
func (w *Worker) refreshAccess(ctx context.Context, errs *[]error) error {
	h := w.H
	if err := h.RefreshCatalog(ctx); err != nil {
		if central.IsNetwork(err) {
			return err
		}
		*errs = append(*errs, fmt.Errorf("catalog: %w", err))
	}
	users, err := h.Store.Users()
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.CannotPush() {
			continue
		}
		if err := h.RefreshAccess(ctx, u.ID); err != nil {
			if central.IsNetwork(err) {
				return err
			}
			slog.Warn("access refresh", "user", u.Username, "err", err)
		}
	}
	return nil
}

// Cycle runs one full sync if the hub is online, unlocked and set up.
func (w *Worker) Cycle(ctx context.Context) error {
	h := w.H
	if !h.Online() && h.Store.Unlocked() && h.Mode() == core.ModeActive {
		h.TryCentralMove(ctx) // central may have moved (docs/DEPLOYMENT.md, section 6)
	}
	if !h.Online() || !h.Store.Unlocked() || h.Mode() != core.ModeActive || h.Runtime() == nil || h.Revoked() {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	start := time.Now()
	h.Stats.Set(func(s *core.SyncStats) { s.CycleStart, s.Syncing = store.Now(), true })
	err := w.cycle(ctx)
	h.NoteStoreErr(err)
	h.Stats.Set(func(s *core.SyncStats) {
		s.Syncing = false
		s.LastSync = store.Now()
		if err != nil {
			s.LastErr = err.Error()
		} else {
			s.LastErr = ""
		}
	})
	slog.Info("sync cycle done", "ms", time.Since(start).Milliseconds(), "err", err)
	if w.AfterCycle != nil && h.Online() {
		w.AfterCycle(ctx)
	}
	return err
}

func (w *Worker) cycle(ctx context.Context) error {
	h := w.H
	var errs []error
	_, err := h.RefreshServerVersion(ctx, h.HubAuth())
	if central.Status(err) == http.StatusUnauthorized {
		// The hub's token is refused (deleted on central): a new one now, with the hub's
		// own account, rather than near its expiry. Shown on the hub page until it works.
		if err = h.ReplaceHubToken(ctx); err == nil {
			_, err = h.RefreshServerVersion(ctx, h.HubAuth())
		}
		housekeeping := ""
		if err != nil {
			housekeeping = "central refuses the hub's token and the hub could not make a new one (" + err.Error() + ")"
		}
		h.Stats.Set(func(s *core.SyncStats) { s.Housekeeping = housekeeping })
	} else if err == nil {
		h.Stats.Set(func(s *core.SyncStats) { s.Housekeeping = "" })
	}
	if err != nil {
		if central.IsNetwork(err) {
			return err
		}
		errs = append(errs, fmt.Errorf("server version: %w", err))
	}
	if h.API() == nil {
		// Unsupported central version: pass-through only (spec 12a).
		w.heartbeat(ctx)
		return fmt.Errorf("hub needs upgrade: central runs %s", h.ServerVersion())
	}
	if w.checkRegistry(ctx); h.Revoked() {
		h.DropTokens(ctx)
		return nil
	}
	// The push first: what staff saved offline goes before the hub's own housekeeping,
	// which on a link that keeps dropping may never finish (docs/ADVERSARIAL-TESTING.md,
	// D11). Central decides on each save whatever the hub knows of access.
	if err := w.applyRetries(ctx); err != nil {
		errs = append(errs, fmt.Errorf("retries: %w", err))
	}
	before := h.Store.CountOutbox(store.OutboxPending)
	if err := w.Push(ctx); err != nil {
		return err
	}
	// Writes made while the push ran keep the hub answering tracker requests itself
	// (core.PassThrough): push them in the next cycle at once rather than in 15
	// minutes, as long as pushing makes progress.
	if h.Store.CountOutbox(store.OutboxPending) < before && h.OutboxWaiting() {
		defer h.TriggerSync()
	}
	if err := h.RenewHubToken(ctx); err != nil {
		errs = append(errs, err)
	}
	if every := h.Cfg.AccessRefreshInterval; every <= 0 || time.Since(w.lastRefresh) >= every {
		if err := w.refreshAccess(ctx, &errs); err != nil {
			return err
		}
		w.lastRefresh = time.Now()
	}
	if pruned, err := h.Store.PruneKeySlots(h.Cfg.KeySlotMaxAge, 3); err != nil {
		errs = append(errs, fmt.Errorf("key slots: %w", err))
	} else if len(pruned) > 0 {
		slog.Info("removed unused key slots", "users", pruned)
	}
	if err := w.ReportIssues(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := w.Pull(ctx); err != nil {
		if central.IsNetwork(err) {
			return err
		}
		errs = append(errs, err)
	}
	if err := w.pullAggregate(ctx); err != nil {
		if central.IsNetwork(err) {
			return err
		}
		errs = append(errs, fmt.Errorf("aggregate pull: %w", err))
	}
	if n, err := h.PruneEvents(); err != nil {
		errs = append(errs, fmt.Errorf("retention: %w", err))
	} else if n > 0 {
		slog.Info("retention pruned events", "events", n)
	}
	if s, d, err := h.PruneTracker(); err != nil {
		errs = append(errs, fmt.Errorf("tracker retention: %w", err))
	} else if s+d > 0 {
		slog.Info("retention pruned tracker", "stubbed", s, "deleted", d)
	}
	if err := w.RefillPool(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, err := h.Store.PruneCache(30 * 24 * time.Hour); err != nil {
		errs = append(errs, fmt.Errorf("cache: %w", err))
	}
	if n, err := h.Store.PruneSentOutbox(); err != nil {
		errs = append(errs, fmt.Errorf("sent outbox: %w", err))
	} else if n > 0 {
		slog.Info("removed sent outbox entries", "entries", n, "sentBefore", store.SentOutboxKept)
	}
	if err := w.publishTokens(ctx); err != nil {
		errs = append(errs, fmt.Errorf("registry tokens: %w", err))
	}
	w.heartbeat(ctx)
	return errors.Join(errs...)
}

// checkRegistry reads this hub's registry entry and records whether another hub
// replaced it (FINDINGS item 16, REVIEW-2 F9). A "revoked" read here is forged.
func (w *Worker) checkRegistry(ctx context.Context) {
	h := w.H
	var reg struct {
		Status string `json:"status"`
	}
	if err := h.Central.JSON(ctx, "GET", core.RegistryPath(h.HubID()), nil, h.HubAuth(), &reg); err != nil {
		return // unknown: keep the last answer
	}
	if reg.Status == "revoked" {
		// Read with the hub's token: central still takes it, so the entry is forged
		// (core.revokedForReal). A real revocation is found at the next token made.
		slog.Warn("the registry marks this hub's tokens as revoked, but central still takes its token: ignored (protect facility-hub-registry, docs/DEPLOYMENT.md)")
		return
	}
	retired := reg.Status == "retired"
	if retired != h.Retired() {
		v := ""
		if retired {
			v = "1"
			slog.Warn("registry marks this hub as replaced: no longer serving patient data offline; pushing its outbox")
		} else {
			slog.Info("registry marks this hub active again")
		}
		h.Store.SetMeta(core.MetaRetired, v)
	}
}

// publishTokens lists the tokens the hub holds in its registry entry, when they
// changed, so the Facility Hub app can revoke them (core/tokens.go).
func (w *Worker) publishTokens(ctx context.Context) error {
	h := w.H
	uids := h.TokenUIDs()
	if strings.Join(uids, ",") == h.Store.Meta(metaRegistryTokens) {
		return nil
	}
	if err := h.EditRegistry(ctx, h.HubAuth(), func(reg map[string]any) bool {
		reg["tokens"] = uids
		return true
	}); err != nil {
		return err
	}
	h.Store.SetMeta(metaRegistryTokens, strings.Join(uids, ","))
	return nil
}

const metaRegistryTokens = "registry_tokens" // the token IDs last listed in the registry

// --- reserved values (spec 10.3) -----------------------------------------------------

// maxReservePerRequest is central's cap on one generateAndReserve call ("You can only
// reserve between 1 and 1000 values"): a bigger pool fills over several cycles.
const maxReservePerRequest = 1000

// poolLowWatermark is the fraction of a pool's target below which it is refilled:
// above it, no request goes to central, so a nearly full pool does not cost a small
// top-up every cycle (T2/T4, decided 2026-10-30). The comparison multiplies instead
// of dividing (`have*poolLowWatermark >= target`): integer division once truncated
// the watermark of a target below 4 to 0, so a pool of 1–3 never refilled, even empty.
const poolLowWatermark = 4

// RefillPool tops up the reserved value pool for every generated attribute, from the
// low watermark to the target (the app's per-attribute setting, else the default).
func (w *Worker) RefillPool(ctx context.Context) error {
	h := w.H
	rt := h.Runtime()
	now := h.CentralTime(time.Now())
	var errs []error
	for _, a := range h.Catalog().GeneratedAttributes() {
		have := h.Store.CountReservedValues(a.ID, now)
		target := h.PoolTarget(a.ID)
		if have*poolLowWatermark >= target {
			continue
		}
		need := min(target-have, maxReservePerRequest)
		code := ""
		if strings.Contains(a.Pattern, "ORG_UNIT_CODE") {
			code = rt.FacilityCode
		}
		var vals []json.RawMessage
		if err := h.Central.JSON(ctx, "GET", h.API().ReservePath(a.ID, need, code), nil, h.HubAuth(), &vals); err != nil {
			errs = append(errs, fmt.Errorf("reserve %s: %w", a.ID, err))
			continue
		}
		var rvs []store.ReservedValue
		for _, v := range vals {
			var meta struct {
				ExpiryDate string `json:"expiryDate"`
			}
			json.Unmarshal(v, &meta)
			rvs = append(rvs, store.ReservedValue{Attribute: a.ID, Value: v, ExpiresAt: meta.ExpiryDate})
		}
		if err := h.Store.AddReservedValues(a.ID, rvs); err != nil {
			return err
		}
		slog.Info("reserved values", "attribute", a.ID, "added", len(rvs), "had", have)
	}
	return errors.Join(errs...)
}
