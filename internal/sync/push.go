package sync

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

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
		var res pushResult
		if e.Kind == store.OutboxAggregate {
			res, err = w.pushAggregate(ctx, e)
		} else {
			res, err = w.pushEntry(ctx, e)
		}
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
}

// reject marks a refused object and records the issue; at is when central refused it
// (its answer's Date; "" for an object blocked before sending).
func (w *Worker) reject(e *store.OutboxEntry, it *tracker.Item, errorReports []tracker.ErrorReport, message, at string) {
	w.setState(it.UID, store.SyncRejected)
	if errorReports == nil {
		errorReports = []tracker.ErrorReport{}
	}
	b, _ := json.Marshal(map[string]any{"message": message, "errorReports": errorReports})
	if err := w.H.Store.AddIssue(e.Seq, it.UID, it.Kind, e.UserID, b, at); err != nil {
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
		if o := h.Index.Get(it.UID); o != nil && o.SyncState == store.SyncSynced && e.Attempts > 0 {
			continue // accepted when the entry was sent before (a retry of a partial entry)
		}
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
			w.reject(e, it, nil, "blocked by "+by, "")
			continue
		}
		send = append(send, it)
	}
	if len(send) == 0 && len(blocked) == 0 {
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxSynced, "")
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
	path := "/api/tracker?async=false&atomicMode=OBJECT&importStrategy=CREATE_AND_UPDATE"
	if e.Params != "" {
		path += "&" + e.Params
	}
	resp, err := h.Central.Do(ctx, "POST", path, body, auth, nil)
	if err != nil {
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{stop: true}, err // network: retry next cycle
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	refusedAt := "" // central's clock, for telling a later change on central apart
	if d, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		refusedAt = d.UTC().Format(time.RFC3339Nano)
	}
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
			w.reject(e, it, nil, (&central.HTTPError{Status: resp.StatusCode, Body: rb}).Error(), refusedAt)
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
		w.reject(e, it, errsByUID[it.UID], msg, refusedAt)
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
			"errorReports": tracker.OutgoingReports(stored.ErrorReports, w.sentValues(is))}
		payload := map[string]any{"hub_id": h.HubID(), "facility": h.Facility(), "user": userUID, "objectUid": is.ObjectUID,
			"kind": is.Kind, "outboxSeq": is.OutboxSeq, "error": errBody, "createdAt": is.CreatedAt}
		key := fmt.Sprintf("%s-%d", h.HubID(), is.ID)
		err := h.Central.JSON(ctx, "POST", "/api/dataStore/facility-hub-issues/"+key, payload, h.HubAuth(), nil)
		if err != nil && central.Status(err) != http.StatusConflict {
			return fmt.Errorf("report issue %d: %w", is.ID, err)
		}
		h.Store.MarkIssueReported(is.ID)
	}
	return w.clearResolvedIssues(ctx)
}

// clearResolvedIssues removes issues that are no longer open (sent again and accepted,
// or replaced by central's newer version) from central's dataStore and from the hub:
// facility-hub-issues then holds what is stuck now, not every refusal ever, and stays
// usable (docs/FINDINGS.md, dataStore measured).
func (w *Worker) clearResolvedIssues(ctx context.Context) error {
	h := w.H
	ids, reported, err := h.Store.ResolvedIssues()
	if err != nil {
		return err
	}
	for i, id := range ids {
		if reported[i] {
			key := fmt.Sprintf("%s-%d", h.HubID(), id)
			err := h.Central.JSON(ctx, "DELETE", "/api/dataStore/facility-hub-issues/"+key, nil, h.HubAuth(), nil)
			if err != nil && central.Status(err) != http.StatusNotFound {
				return fmt.Errorf("clear resolved issue %d: %w", id, err)
			}
		}
		if err := h.Store.DeleteIssue(id); err != nil {
			return err
		}
	}
	return nil
}

// sentValues are the values the hub sent with an issue's object, for masking central's
// messages: an aggregate edit's, or a tracker object's with its person's and
// enrollment's, which central may quote too.
func (w *Worker) sentValues(is *store.Issue) []string {
	if is.Kind == store.OutboxAggregate {
		// Only what was entered: the edit's identifiers are metadata.
		var stored struct {
			Edit struct {
				Value, Comment *string
				By             string
			} `json:"edit"`
		}
		json.Unmarshal(is.Error, &stored)
		m := tracker.M{"by": stored.Edit.By}
		for k, v := range map[string]*string{"value": stored.Edit.Value, "comment": stored.Edit.Comment} {
			if v != nil {
				m[k] = *v
			}
		}
		return tracker.SentValues(m)
	}
	var payloads []tracker.M
	uids := []string{is.ObjectUID}
	for i := 0; i < len(uids); i++ {
		o, err := w.H.Store.GetObject(uids[i])
		if err != nil || o == nil {
			continue
		}
		var p tracker.M
		if json.Unmarshal(o.Payload, &p) == nil {
			payloads = append(payloads, p)
		}
		if i == 0 {
			for _, u := range []string{o.TE, o.Enrollment} {
				if u != "" && u != o.UID {
					uids = append(uids, u)
				}
			}
		}
	}
	return tracker.SentValues(payloads...)
}

// --- resending refusals (the Facility Hub app's retry instructions) ---------------

// issueGroup returns what a refusal is grouped by, in the heartbeat and for retries:
// the object's programme and central's error codes ("none" when it was blocked by an
// earlier refusal, or central gave no report), and the object that blocked it.
func issueGroup(h *core.Hub, is *store.Issue) (program string, codes map[string]bool, blockedBy string) {
	var e struct {
		Message string `json:"message"`
		Reports []struct {
			ErrorCode string `json:"errorCode"`
		} `json:"errorReports"`
	}
	json.Unmarshal(is.Error, &e)
	if o := h.Index.Get(is.ObjectUID); o != nil {
		program = o.Program
	}
	codes = map[string]bool{}
	for _, r := range e.Reports {
		codes[cmp.Or(r.ErrorCode, "none")] = true
	}
	if len(codes) == 0 {
		codes["none"] = true
	}
	blockedBy, _ = strings.CutPrefix(e.Message, "blocked by ")
	if blockedBy == e.Message {
		blockedBy = ""
	}
	return program, codes, blockedBy
}

// retry is a resend instruction from the Facility Hub app (facility-hub-admin/retries).
type retry struct {
	ID, Kind, Program, Code, At string
}

const metaRetriesApplied = "retries_applied" // the retry IDs this hub has applied

// applyRetries takes up the resend instructions a hub manager gave in the Facility Hub
// app after central was fixed: this hub's saves refused with that kind, programme and
// error code, and what they blocked, are sent again as their authors, once per
// instruction, when the hub first sees it. Central's new answer stands.
func (w *Worker) applyRetries(ctx context.Context) error {
	h := w.H
	var v struct {
		Retries []retry `json:"retries"`
	}
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-admin/retries", nil, h.HubAuth(), &v)
	if central.Status(err) == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	applied := strings.Split(h.Store.Meta(metaRetriesApplied), ",")
	var todo []retry
	var ids []string
	for _, r := range v.Retries {
		ids = append(ids, r.ID)
		if !slices.Contains(applied, r.ID) {
			todo = append(todo, r)
		}
	}
	if len(todo) > 0 {
		issues, err := h.Store.UnresolvedIssues(100000)
		if err != nil {
			return err
		}
		uids, seqs := map[string]bool{}, map[int64]bool{}
		blocked := map[string][]*store.Issue{} // by the object that blocked them
		for _, is := range issues {
			program, codes, by := issueGroup(h, is)
			if by != "" {
				blocked[by] = append(blocked[by], is)
			}
			for _, r := range todo {
				if is.Kind != r.Kind || program != r.Program || !codes[r.Code] {
					continue
				}
				if is.Kind == store.OutboxAggregate && w.aggregateReplaced(is) {
					// A later change of the cell reached central: sending this edit
					// again would undo it. It is done, and its issue closes.
					h.Store.FinishOutbox(is.OutboxSeq, store.OutboxSynced, "replaced by a later change")
					continue
				}
				uids[is.ObjectUID], seqs[is.OutboxSeq] = true, true
			}
		}
		for queue := slices.Collect(maps.Keys(uids)); len(queue) > 0; queue = queue[1:] {
			for _, is := range blocked[queue[0]] {
				if !uids[is.ObjectUID] {
					uids[is.ObjectUID], seqs[is.OutboxSeq] = true, true
					queue = append(queue, is.ObjectUID)
				}
			}
		}
		n, err := h.Store.Requeue(slices.Collect(maps.Keys(uids)), slices.Collect(maps.Keys(seqs)))
		if err != nil {
			return err
		}
		slog.Info("retry from the Facility Hub app", "instructions", len(todo), "objects", len(uids), "entries", n)
	}
	h.Store.SetMeta(metaRetriesApplied, strings.Join(ids, ","))
	return nil
}
