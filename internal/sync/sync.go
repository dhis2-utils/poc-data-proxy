// Package sync is the sync worker (spec 11): outbox push, issue reporting, pull,
// reserved value pool refill and heartbeat. It runs every sync_interval while online,
// immediately on the switch from offline to online, and on "sync now".
package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	stdsync "sync"
	"sync/atomic"
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
	// pushAnswered: central answered a push in the current Push (under pushMu).
	pushAnswered bool
	skipped      atomic.Pointer[string] // why the last cycle was skipped; nil after one ran
	// OverwriteSample: one outbox entry in this many is checked for overwrites
	// (overwrites.go); 0 for OverwriteSample.
	OverwriteSample int

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

// skipReason says why no cycle can run now; "" when one can.
func skipReason(h *core.Hub) string {
	switch {
	case !h.Online():
		return "offline"
	case !h.Store.Unlocked():
		return "store locked: no staff login since the start"
	case h.Mode() != core.ModeActive:
		return "not set up"
	case h.Runtime() == nil:
		return "no runtime settings"
	case h.Revoked():
		return "revoked on central"
	}
	return ""
}

// Cycle runs one full sync if the hub is online, unlocked and set up.
func (w *Worker) Cycle(ctx context.Context) error {
	h := w.H
	if !h.Online() && h.Store.Unlocked() && h.Mode() == core.ModeActive {
		h.TryCentralMove(ctx) // central may have moved (docs/OPERATIONS.md, "Moving central")
	}
	if why := skipReason(h); why != "" {
		// Once per change of reason: a night without cycles left nothing in the log to
		// say why (PENDING.md, item 14).
		if prev := w.skipped.Swap(&why); prev == nil || *prev != why {
			slog.Info("sync cycle skipped", "why", why)
		}
		return nil
	}
	w.skipped.Store(nil)
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
	hourly := false
	if every := h.Cfg.AccessRefreshInterval; every <= 0 || time.Since(w.lastRefresh) >= every {
		if err := w.refreshAccess(ctx, &errs); err != nil {
			return err
		}
		w.lastRefresh, hourly = time.Now(), true
	}
	if err := w.checkMetadata(ctx, hourly); err != nil {
		if central.IsNetwork(err) {
			return err
		}
		errs = append(errs, fmt.Errorf("metadata check: %w", err))
	}
	if pruned, err := h.Store.PruneKeySlots(h.Cfg.KeySlotMaxAge, 3); err != nil {
		errs = append(errs, fmt.Errorf("key slots: %w", err))
	} else if len(pruned) > 0 {
		slog.Info("removed unused key slots", "users", pruned)
	}
	if err := w.ReportIssues(ctx); err != nil {
		errs = append(errs, err)
	}
	// The pool before the pull: a user's first pull can take many minutes, and their
	// programs' generated attributes, new to the catalog at their login, would have no
	// values offline if the link dropped during it (PENDING.md, item 31). One request
	// per attribute below its watermark, none otherwise.
	if err := w.RefillPool(ctx); err != nil {
		if central.IsNetwork(err) {
			return err
		}
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
// replaced it (FINDINGS item 16, REVIEW-2 F9). An approved hub takes a revocation
// signed with its move key (core.TakeRegistryStatus); any other "revoked" read here
// is ignored.
func (w *Worker) checkRegistry(ctx context.Context) {
	h := w.H
	var raw json.RawMessage
	var reg struct {
		Status string `json:"status"`
	}
	if err := h.Central.JSON(ctx, "GET", core.RegistryPath(h.HubID()), nil, h.HubAuth(), &raw); err != nil || json.Unmarshal(raw, &reg) != nil {
		return // unknown: keep the last answer
	}
	if h.TakeRegistryStatus(ctx, raw) && h.Revoked() {
		return
	}
	if reg.Status == "revoked" {
		// Not signed, or read with the hub's token, which central still takes: forged
		// (core.revokedForReal). A real revocation of a hub without a move key is
		// found at the next token made.
		slog.Warn("the registry marks this hub's tokens as revoked, unsigned or while central still takes its token: ignored")
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

// notEnoughLeft reports whether central refused a reservation because the pattern has
// fewer values left than asked for (2.42 and 2.43: 409 "Could not reserve value: Not
// enough values left to reserve N values.", no error code).
func notEnoughLeft(err error) bool {
	var he *central.HTTPError
	return errors.As(err, &he) && he.Status == 409 && bytes.Contains(he.Body, []byte("Not enough values left"))
}

// poolLowWatermark is the fraction of a pool's target below which it is refilled:
// above it, no request goes to central, so a nearly full pool does not cost a small
// top-up every cycle (T2/T4, decided 2026-10-30). The comparison multiplies instead
// of dividing (`have*poolLowWatermark >= target`): integer division once truncated
// the watermark of a target below 4 to 0, so a pool of 1–3 never refilled, even empty.
const poolLowWatermark = 4

// poolPatternKey + attribute is the pattern the attribute's pool was filled under.
const poolPatternKey = "pool_pattern:"

// RefillPool tops up the reserved value pool for every generated attribute, from the
// low watermark to the target (the app's per-attribute setting, else the default).
// When an attribute's pattern changed on central, its pool is emptied first, so the
// hub stops handing out values of the old pattern (A22); the values dropped stay
// reserved on central until they expire, as values a reset tablet loses do.
func (w *Worker) RefillPool(ctx context.Context) error {
	h := w.H
	rt := h.Runtime()
	now := h.CentralTime(time.Now())
	var errs []error
	for _, a := range h.Catalog().GeneratedAttributes() {
		if old := h.Store.Private(poolPatternKey + a.ID); old != a.Pattern {
			if old != "" {
				n, err := h.Store.DropReservedValues(a.ID)
				if err != nil {
					return err
				}
				slog.Info("reserved values dropped: the pattern changed", "attribute", a.ID, "from", old, "to", a.Pattern, "dropped", n)
			}
			if err := h.Store.SetPrivate(poolPatternKey+a.ID, a.Pattern); err != nil {
				return err
			}
		}
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
		// A short pattern (Laos: ORG_UNIT_CODE + RANDOM(###), 1000 values per facility)
		// runs out: central refuses a request for more than are left, so ask for half
		// as many, down to one. A pool still below its watermark after that is reported:
		// the error shows on the hub page as the last sync error.
		var vals []json.RawMessage
		var err error
		short := false
		for {
			err = h.Central.JSON(ctx, "GET", h.API().ReservePath(a.ID, need, code), nil, h.HubAuth(), &vals)
			if err == nil || !notEnoughLeft(err) {
				break
			}
			short = true
			if need == 1 {
				err = nil
				break
			}
			need /= 2
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("reserve %s: %w", a.ID, err))
			continue
		}
		if short && (have+len(vals))*poolLowWatermark < target {
			errs = append(errs, fmt.Errorf("reserved values for %s (pattern %s): central has too few left, the pool holds %d of %d",
				a.ID, a.Pattern, have+len(vals), target))
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
