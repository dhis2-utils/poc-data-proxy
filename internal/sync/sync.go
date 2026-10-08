// Package sync is the sync worker (spec 11): outbox push, issue reporting, pull,
// reserved value pool refill and heartbeat. It runs every sync_interval while online,
// immediately on the switch from offline to online, and on "sync now".
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	stdsync "sync"
	"syscall"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/compat"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

type Worker struct {
	H *core.Hub
	// AfterCycle runs at the end of each online cycle (the upgrade check, spec 12a).
	AfterCycle func(ctx context.Context)

	mu     stdsync.Mutex // one cycle at a time
	pushMu stdsync.Mutex // one push at a time: Cycle, the upgrade drain and reset all push
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
	w.H.Monitor.OnChange(func(online bool) {
		if online {
			w.H.TriggerSync()
		}
	})
	t := time.NewTicker(w.H.Cfg.SyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.H.SyncNow:
		}
		if err := w.Cycle(ctx); err != nil {
			slog.Warn("sync cycle", "err", err)
		}
	}
}

// Cycle runs one full sync if the hub is online, unlocked and set up.
func (w *Worker) Cycle(ctx context.Context) error {
	h := w.H
	if !h.Online() || !h.Store.Unlocked() || h.Mode() != core.ModeActive || h.Runtime() == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	start := time.Now()
	h.Stats.Set(func(s *core.SyncStats) { s.CycleStart, s.Syncing = store.Now(), true })
	err := w.cycle(ctx)
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
	if _, err := h.RefreshServerVersion(ctx, h.HubAuth()); err != nil {
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
	if err := h.RenewHubToken(ctx); err != nil {
		errs = append(errs, err)
	}
	w.checkRegistry(ctx)
	if rt := h.Runtime(); rt != nil && rt.FacilityPath == "" {
		var ou struct {
			Path string `json:"path"`
		}
		if err := h.Central.JSON(ctx, "GET", central.Q("/api/organisationUnits/"+rt.Facility, "fields", "path"), nil, h.HubAuth(), &ou); err != nil {
			errs = append(errs, fmt.Errorf("facility path: %w", err))
		} else if ou.Path != "" {
			h.UpdateRuntime(func(rt *core.Runtime) { rt.FacilityPath = ou.Path })
		}
	}
	if err := h.RefreshCatalog(ctx); err != nil {
		if central.IsNetwork(err) {
			return err
		}
		errs = append(errs, fmt.Errorf("catalog: %w", err))
	}
	users, err := h.Store.Users()
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.NeedsLogin || (!u.HasPAT && !u.HasPassword) {
			continue
		}
		if err := h.RefreshAccess(ctx, u.ID); err != nil {
			if central.IsNetwork(err) {
				return err
			}
			slog.Warn("access refresh", "user", u.Username, "err", err)
		}
	}
	if pruned, err := h.Store.PruneKeySlots(h.Cfg.KeySlotMaxAge, 3); err != nil {
		errs = append(errs, fmt.Errorf("key slots: %w", err))
	} else if len(pruned) > 0 {
		slog.Info("removed unused key slots", "users", pruned)
	}
	if err := w.Push(ctx); err != nil {
		return err
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
	if err := w.RefillPool(ctx); err != nil {
		errs = append(errs, err)
	}
	if _, err := h.Store.PruneCache(30 * 24 * time.Hour); err != nil {
		errs = append(errs, fmt.Errorf("cache: %w", err))
	}
	w.heartbeat(ctx)
	return errors.Join(errs...)
}

// checkRegistry reads this hub's registry entry and records whether another hub
// replaced it (FINDINGS item 16, REVIEW-2 F9).
func (w *Worker) checkRegistry(ctx context.Context) {
	h := w.H
	var reg struct {
		Status string `json:"status"`
	}
	if err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-registry/"+h.HubID(), nil, h.HubAuth(), &reg); err != nil {
		return // unknown: keep the last answer
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

// --- push (spec 11.1) -------------------------------------------------------------

type pushResult struct {
	userBlocked bool
	stop        bool
}

// Push sends pending outbox entries in seq order.
func (w *Worker) Push(ctx context.Context) error {
	w.pushMu.Lock()
	defer w.pushMu.Unlock()
	h := w.H
	entries, err := h.Store.PendingOutbox()
	if err != nil {
		return err
	}
	skipUser := map[int64]bool{}
	for _, e := range entries {
		if e.DependsOnSeq > 0 && h.Store.OutboxState(e.DependsOnSeq) == store.OutboxPending {
			continue // its parent entry has not been pushed yet
		}
		if skipUser[e.UserID] {
			continue
		}
		res, err := w.pushEntry(ctx, e)
		if err != nil {
			return err
		}
		if res.userBlocked {
			skipUser[e.UserID] = true
		}
		if res.stop {
			break
		}
	}
	if h.Store.CountOutbox(store.OutboxPending) == 0 || len(entries) > 0 {
		now := store.Now()
		h.Stats.Set(func(s *core.SyncStats) { s.LastPush = now })
		h.Store.SetPrivate("last_push", now)
	}
	return nil
}

func (w *Worker) setState(uid, state string) {
	w.H.Store.SetSyncState(w.H.Store.DB(), uid, state)
	w.H.Index.SetSyncState(uid, state)
}

func (w *Worker) reject(e *store.OutboxEntry, it *tracker.Item, errorReports []tracker.ErrorReport, message string) {
	w.setState(it.UID, store.SyncRejected)
	if errorReports == nil {
		errorReports = []tracker.ErrorReport{}
	}
	b, _ := json.Marshal(map[string]any{"message": message, "errorReports": errorReports})
	if err := w.H.Store.AddIssue(e.Seq, it.UID, it.Kind, e.UserID, b); err != nil {
		slog.Error("add issue", "err", err)
	}
	slog.Warn("object rejected", "uid", it.UID, "kind", it.Kind, "seq", e.Seq, "reason", tracker.OutgoingMessage(message, errorReports))
}

func parents(it *tracker.Item) []string {
	switch it.Kind {
	case store.KindEnrollment:
		return []string{it.TE}
	case store.KindEvent:
		return []string{it.Enrollment}
	case store.KindRelationship:
		return []string{it.From.UID, it.To.UID}
	}
	return nil
}

func (w *Worker) pushEntry(ctx context.Context, e *store.OutboxEntry) (pushResult, error) {
	h := w.H
	var payload tracker.M
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, "corrupt payload: "+err.Error())
	}
	items := tracker.Flatten(payload)
	// Objects whose parent was rejected are blocked, not sent (spec 11.1 step 4).
	blocked := map[string]bool{}
	var send []*tracker.Item
	for _, it := range items {
		var by string
		for _, p := range parents(it) {
			if blocked[p] {
				by = p
				break
			}
			if o := h.Index.Get(p); o != nil && o.SyncState == store.SyncRejected {
				by = p
				break
			}
		}
		if by != "" {
			blocked[it.UID] = true
			w.reject(e, it, nil, "blocked by "+by)
			continue
		}
		send = append(send, it)
	}
	if len(send) == 0 {
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, "all objects blocked")
	}
	body, _ := json.Marshal(flat(send))
	auth, err := h.UpstreamAuth(e.UserID)
	if err != nil {
		h.Store.SetNeedsLogin(e.UserID, true)
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{userBlocked: true}, nil
	}
	resp, err := h.Central.Do(ctx, "POST", "/api/tracker?async=false&atomicMode=OBJECT&importStrategy=CREATE_AND_UPDATE", body, auth, nil)
	if err != nil {
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{stop: true}, err // network: retry next cycle
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		// Expired or revoked PAT: keep the entry, the user must log in online again.
		h.Store.SetNeedsLogin(e.UserID, true)
		h.Store.NoteAttempt(e.Seq, "401 from central: needs online login")
		slog.Warn("push: PAT rejected, user needs online login", "user", e.UserID)
		return pushResult{userBlocked: true}, nil
	case resp.StatusCode >= 500:
		h.Store.NoteAttempt(e.Seq, fmt.Sprintf("central returned %d", resp.StatusCode))
		return pushResult{stop: true}, nil
	}
	persisted, errsByUID, perr := h.API().TrackerResult(resp.StatusCode, rb)
	if perr != nil {
		// A non-report answer (e.g. 400 parse error) rejects the whole entry.
		for _, it := range send {
			w.reject(e, it, nil, (&central.HTTPError{Status: resp.StatusCode, Body: rb}).Error())
		}
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, perr.Error())
	}
	ok, bad := 0, len(blocked)
	for _, it := range send {
		if persisted[it.UID] {
			ok++
			if !h.Store.HasPendingOutboxFor(it.UID, e.Seq) {
				w.setState(it.UID, store.SyncSynced)
			}
			continue
		}
		bad++
		msg := "not persisted by central"
		if len(errsByUID[it.UID]) > 0 {
			msg = errsByUID[it.UID][0].Message
		} else {
			for _, p := range parents(it) {
				if len(errsByUID[p]) > 0 || (!persisted[p] && slices.ContainsFunc(send, func(x *tracker.Item) bool { return x.UID == p })) {
					msg = "blocked by " + p
					break
				}
			}
		}
		w.reject(e, it, errsByUID[it.UID], msg)
	}
	state := store.OutboxSynced
	switch {
	case ok == 0:
		state = store.OutboxRejected
	case bad > 0:
		state = store.OutboxPartial
	}
	slog.Info("pushed outbox entry", "seq", e.Seq, "ok", ok, "rejected", bad, "state", state)
	return pushResult{}, h.Store.FinishOutbox(e.Seq, state, "")
}

func flat(items []*tracker.Item) tracker.M {
	out := tracker.M{"trackedEntities": []any{}, "enrollments": []any{}, "events": []any{}, "relationships": []any{}}
	for _, it := range items {
		key := map[string]string{store.KindTrackedEntity: "trackedEntities", store.KindEnrollment: "enrollments",
			store.KindEvent: "events", store.KindRelationship: "relationships"}[it.Kind]
		out[key] = append(out[key].([]any), it.Data)
	}
	return out
}

// --- issues (spec 11.2) -----------------------------------------------------------

// ReportIssues writes new issues to the central data store with the hub token.
// No patient attribute values are included.
func (w *Worker) ReportIssues(ctx context.Context) error {
	h := w.H
	issues, err := h.Store.Issues(true, 500)
	if err != nil {
		return err
	}
	slices.Reverse(issues) // oldest first
	for _, is := range issues {
		var userUID string
		if u, _ := h.Store.UserByID(is.UserID); u != nil {
			userUID = u.UserUID
		}
		var stored struct {
			Message      string                `json:"message"`
			ErrorReports []tracker.ErrorReport `json:"errorReports"`
		}
		json.Unmarshal(is.Error, &stored)
		errBody := map[string]any{"message": tracker.OutgoingMessage(stored.Message, stored.ErrorReports),
			"errorReports": tracker.OutgoingReports(stored.ErrorReports)}
		payload := map[string]any{"hub_id": h.HubID(), "facility": h.Facility(), "user": userUID, "objectUid": is.ObjectUID,
			"kind": is.Kind, "outboxSeq": is.OutboxSeq, "error": errBody, "createdAt": is.CreatedAt}
		key := fmt.Sprintf("%s-%d", h.HubID(), is.ID)
		err := h.Central.JSON(ctx, "POST", "/api/dataStore/facility-hub-issues/"+key, payload, h.HubAuth(), nil)
		if err != nil && central.Status(err) != http.StatusConflict {
			return fmt.Errorf("report issue %d: %w", is.ID, err)
		}
		h.Store.MarkIssueReported(is.ID)
	}
	return nil
}

// --- pull (spec 11.3) ----------------------------------------------------------------

func (w *Worker) Pull(ctx context.Context) error {
	h := w.H
	api := h.API()
	users, err := h.Store.Users()
	if err != nil {
		return err
	}
	facility := h.Facility()
	cat := h.Catalog()
	var failed []error
	for _, u := range users {
		if u.NeedsLogin || (!u.HasPAT && !u.HasPassword) {
			continue
		}
		acc := h.Access(u.ID)
		if !acc.InFacility(h.FacilityPath()) {
			continue
		}
		auth, err := h.UpstreamAuth(u.ID)
		if err != nil {
			continue
		}
		var programs []string
		// A user whose access changed since their last pull gets a full pull, so what
		// they gained or lost shows at this sync rather than the next daily one.
		accKey := "pull_access:" + strconv.FormatInt(u.ID, 10)
		accJSON, _ := json.Marshal(acc)
		full := h.Store.Private(accKey) != string(accJSON)
		failedBefore := len(failed)
		var unreadable []string // programs the user may not read: a full pull settles them empty
		for _, p := range cat.ProgramIDs() {
			pm := cat.Programs[p]
			if !acc.Program(p).Read || (pm != nil && pm.TET != "" && !acc.TET(pm.TET).Read) {
				unreadable = append(unreadable, p) // central would refuse the search (E1006 without tracked entity type access)
				continue
			}
			programs = append(programs, p)
			_, lastFull := h.Store.PullState(u.ID, p)
			if t := store.ParseTime(lastFull); t.IsZero() || time.Since(t) > h.Cfg.FullPullInterval {
				full = true
			}
		}
		// Also each readable tracked entity type, for people enrolled in nothing the
		// user can read (found by the differential oracle, docs/TESTING.md).
		for _, tet := range cat.TETs() {
			if acc.TET(tet).Read {
				programs = append(programs, "tet:"+tet)
			}
		}
		seen := map[string]bool{}
		var settled []string // programs whose visibility this pull may decide
		for _, p := range programs {
			lastPull, _ := h.Store.PullState(u.ID, p)
			after := ""
			if !full {
				after = lastPull
			}
			maxUpdated, n, err := w.pullProgram(ctx, api, auth, u.ID, facility, p, after, seen)
			if err != nil {
				st := central.Status(err)
				switch {
				case central.IsNetwork(err):
					return fmt.Errorf("pull %s/%s: %w", u.Username, p, err)
				case st == http.StatusUnauthorized:
					h.Store.SetNeedsLogin(u.ID, true)
				case st >= 400 && st < 500:
					// Central refuses this user the program: they lose what they saw of
					// it, and the other users' pulls go on.
					slog.Warn("pull refused by central", "user", u.Username, "program", p, "err", err)
					settled = append(settled, p)
					continue
				default:
					// A server error says nothing about access: keep what the user sees.
					slog.Warn("pull failed", "user", u.Username, "program", p, "err", err)
					failed = append(failed, fmt.Errorf("pull %s/%s: %w", u.Username, p, err))
					continue
				}
				break
			}
			settled = append(settled, p)
			if maxUpdated == "" {
				maxUpdated = lastPull
			}
			if full {
				// Full pulls record their own time so the daily check works.
				h.Store.SetPullState(u.ID, p, maxUpdated, true)
				h.Store.DB().Exec(`UPDATE pull_state SET last_full_at=? WHERE user_id=? AND program=?`, store.Now(), u.ID, p)
			} else {
				h.Store.SetPullState(u.ID, p, maxUpdated, false)
			}
			slog.Info("pulled", "user", u.Username, "program", p, "full", full, "objects", n)
		}
		if len(failed) == failedBefore { // this user's pull completed
			h.Store.SetPrivate(accKey, string(accJSON))
		}
		settled = append(settled, unreadable...)
		if full && len(settled) > 0 {
			if n := h.RemoveVisibilityExcept(u.ID, settled, seen); n > 0 {
				slog.Info("full pull removed visibility", "user", u.Username, "objects", n)
			}
		}
	}
	now := store.Now()
	h.Stats.Set(func(s *core.SyncStats) { s.LastPull = now })
	h.Store.SetPrivate("last_pull", now)
	return errors.Join(failed...)
}

// pullProgram pulls one user's view of one program (or "tet:<type>") in two steps:
// it lists what central shows the user, identifiers and update times only, then
// fetches in full only the tracked entities the hub lacks or holds older, or has not
// yet fetched for this program's attributes. Everything listed becomes visible to the
// user. So central still decides per user, while each record's content crosses the
// uplink once, whoever is first to see it (docs/PERFORMANCE.md). It returns the
// newest updatedAt listed (central's clock, used as the next updatedAfter).
func (w *Worker) pullProgram(ctx context.Context, api compat.API, auth central.Auth, userID int64,
	facility, program, after string, seen map[string]bool) (string, int, error) {
	h := w.H
	maxUpdated, total := "", 0
	var stale, current []string
	for page := 1; ; page++ {
		items, more, err := w.getPage(ctx, api, auth, api.ListPath(facility, program, after, page, listPageSize), page, listPageSize)
		if err != nil {
			return "", total, err
		}
		for _, te := range items {
			uid := tracker.Str(te, "trackedEntity")
			isStale := !h.Index.HasScope(uid, program)
			var listed []string
			for _, it := range tracker.Flatten(tracker.M{"trackedEntities": []any{te}}) {
				upd := tracker.Str(it.Data, "updatedAt")
				maxUpdated = max(maxUpdated, upd)
				if o := h.Index.Get(it.UID); o == nil || o.UpdatedAt < upd {
					isStale = true
				}
				listed = append(listed, it.UID)
			}
			for _, u := range listed {
				seen[u] = true
			}
			total += len(listed)
			if isStale {
				stale = append(stale, uid)
			} else {
				current = append(current, listed...)
			}
		}
		if !more {
			break
		}
	}
	for start := 0; start < len(stale); start += fetchBatch {
		batch := stale[start:min(start+fetchBatch, len(stale))]
		items, _, err := w.getPage(ctx, api, auth, api.FetchPath(facility, program, batch), 1, len(batch))
		if err != nil {
			return "", total, err
		}
		anyItems := make([]any, len(items))
		for i, te := range items {
			anyItems[i] = te
		}
		if _, err := h.UpsertCentral(userID, tracker.M{"trackedEntities": anyItems}, true); err != nil {
			return "", total, err
		}
		if err := h.RecordScopes(batch, program); err != nil {
			return "", total, err
		}
	}
	if err := h.MarkVisible(userID, current); err != nil {
		return "", total, err
	}
	slog.Debug("pull listed", "program", program, "objects", total, "fetched", len(stale))
	return maxUpdated, total, nil
}

func (w *Worker) getPage(ctx context.Context, api compat.API, auth central.Auth, path string, page, size int) ([]tracker.M, bool, error) {
	resp, err := w.H.Central.Do(ctx, "GET", path, nil, auth, nil)
	if err != nil {
		return nil, false, err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, &central.HTTPError{Status: resp.StatusCode, Body: b}
	}
	return api.PullItems(b, page, size)
}

// --- reserved values (spec 10.3) -----------------------------------------------------

// RefillPool tops up the reserved value pool for every generated attribute.
func (w *Worker) RefillPool(ctx context.Context) error {
	h := w.H
	rt := h.Runtime()
	now := tracker.DHIS2Time(time.Now())
	var errs []error
	for _, a := range h.Catalog().GeneratedAttributes() {
		have := h.Store.CountReservedValues(a.ID, now)
		need := h.Cfg.ReservedPoolSize - have
		if need <= 0 {
			continue
		}
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

// --- heartbeat (spec 11.4) -------------------------------------------------------------

func (w *Worker) heartbeat(ctx context.Context) {
	h := w.H
	if err := h.Central.JSON(ctx, "PUT", "/api/dataStore/facility-hub-status/"+h.HubID(), Status(h), h.HubAuth(), nil); err != nil {
		slog.Warn("heartbeat", "err", err)
	}
}

// Status is the heartbeat payload. It holds no patient data.
func Status(h *core.Hub) map[string]any {
	_, _, lastSync, lastErr := h.Stats.Get()
	lastPush, lastPull := h.LastPushPull()
	var upgrade any
	json.Unmarshal([]byte(h.Store.Meta(core.MetaUpgrade)), &upgrade)
	return map[string]any{
		"hubId": h.HubID(), "facility": h.Facility(), "version": h.Version, "serverVersion": h.ServerVersion(),
		"uptimeSeconds": int(time.Since(h.Started).Seconds()), "lastPush": lastPush, "lastPull": lastPull,
		"lastSync": lastSync, "lastError": lastErr, "pending": h.Store.CountObjects(store.SyncPending),
		"rejected": h.Store.CountObjects(store.SyncRejected), "pendingOutbox": h.Store.CountOutbox(store.OutboxPending),
		"stuckOutbox": h.StuckOutbox(), "retired": h.Retired(),
		"issues": h.Store.CountIssues(), "diskFreeBytes": diskFree(h.Cfg.DataDir), "lastUpgrade": upgrade,
		"needsUpgrade": h.API() == nil && h.ServerVersion() != "", "time": time.Now().UTC().Format(time.RFC3339),
	}
}

func diskFree(dir string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return uint64(st.Bavail) * uint64(st.Bsize)
}
