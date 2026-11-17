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

// Push sends pending outbox entries in seq order. It sends nothing while local writes
// are frozen for an upgrade: what it marked as sent would be marked in a store that a
// rollback, or the swap, throws away, and be pushed again. After the entries it closes
// the refusals of changes to what central has deleted (closeMoot).
func (w *Worker) Push(ctx context.Context) error {
	w.pushMu.Lock()
	defer w.pushMu.Unlock()
	h := w.H
	w.pushAnswered = false
	if h.Upgrading.Load() {
		return nil
	}
	if h.API() == nil {
		// Central's import reports are read through the version's handling: the
		// outbox waits for a release that supports central.
		return fmt.Errorf("hub needs upgrade: central runs %s", h.ServerVersion())
	}
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
	if err := w.closeMoot(ctx); err != nil {
		return fmt.Errorf("closing refusals of what central deleted: %w", err)
	}
	// The last push is when central last answered one: not when the outbox was found
	// empty, or held only entries refused or kept without asking central (PENDING.md,
	// item 35). How long saves have waited is the heartbeat's oldestPending.
	if w.pushAnswered {
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
	if w.authorGone(e.UserID) {
		// What the refused objects of this entry hold is blocked by them, as when
		// central refuses: a resend of the refused ones sends it along.
		for _, it := range send {
			if p := slices.IndexFunc(parents(it), func(p string) bool { return blocked[p] }); p >= 0 {
				w.reject(e, it, nil, "blocked by "+parents(it)[p], "")
			} else {
				w.reject(e, it, []tracker.ErrorReport{{ErrorCode: tracker.NoLoginCode, Message: tracker.NoLoginMessage,
					TrackerType: tracker.TrackerType(it.Kind), UID: it.UID}}, tracker.NoLoginMessage, "")
			}
			blocked[it.UID] = true
		}
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, tracker.NoLoginMessage)
	}
	body, _ := json.Marshal(flat(send))
	auth, err := h.UpstreamAuth(e.UserID)
	if err != nil {
		h.Store.SetNeedsLogin(e.UserID, true)
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{userBlocked: true}, nil
	}
	w.countOverwrites(ctx, e, auth) // telemetry (sync/overwrites.go)
	path := "/api/tracker?async=false&atomicMode=OBJECT&importStrategy=CREATE_AND_UPDATE"
	if e.Params != "" {
		path += "&" + e.Params
	}
	resp, err := h.Central.Do(ctx, "POST", path, body, auth, nil)
	if err != nil {
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{stop: true}, err // network: retry next cycle
	}
	w.pushAnswered = true
	rb, rerr := io.ReadAll(resp.Body)
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
	case resp.StatusCode > 500 || onTheWay(resp.StatusCode) || rerr != nil:
		note := fmt.Sprintf("central returned %d", resp.StatusCode)
		if rerr != nil {
			note += ", cut off: " + rerr.Error() // central may have imported it: the resend updates
		}
		h.Store.NoteAttempt(e.Seq, note)
		return pushResult{stop: true}, nil
	}
	// A plain 500 is central failing on this import (a bug or a lock its payload trips),
	// not central or the way to it down (502-504): the entries after it go on, those
	// that depend on it wait (DependsOnSeq), and it is refused after a day as below.
	failed := resp.StatusCode == http.StatusInternalServerError
	var persisted map[string]bool
	var errsByUID map[string][]tracker.ErrorReport
	perr := fmt.Errorf("central returned %d", resp.StatusCode)
	if !failed {
		persisted, errsByUID, perr = h.API().TrackerResult(resp.StatusCode, rb)
	}
	if perr != nil {
		// Not an import report (a proxy's page, say): it says nothing about the saves,
		// so they are tried again. After a day of such answers the entry is refused, so
		// it does not hold up the saves after it for ever, and a manager sees why.
		if time.Since(h.Store.FailingSince(e.Seq)) < failingRefusedAfter {
			h.Store.NoteAttempt(e.Seq, perr.Error())
			return pushResult{stop: !failed}, nil
		}
		msg := (&central.HTTPError{Status: resp.StatusCode, Body: rb}).Error()
		for _, it := range send {
			w.reject(e, it, nil, msg, refusedAt)
		}
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, perr.Error())
	}
	ok, bad := 0, len(blocked)
	for _, it := range send {
		if persisted[it.UID] {
			ok++
			if err := h.Store.AcceptedByCentral(it.UID); err != nil {
				return pushResult{}, err
			}
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

// loginWait is how long the saves of a user central stopped accepting wait for that
// user to log in online again. After that they are refused: a user disabled or deleted
// on central never logs in again, and a hub manager can resend them as another user.
const loginWait = 7 * 24 * time.Hour

// failingRefusedAfter is how long an entry central fails on (a plain 500, an answer
// that is no import report) is tried again before it is refused.
const failingRefusedAfter = 24 * time.Hour

// authorGone reports whether the user has needed an online login for loginWait.
func (w *Worker) authorGone(userID int64) bool {
	u, _ := w.H.Store.UserByID(userID)
	return u != nil && u.NeedsLogin && u.NeedsLoginSince != "" && time.Since(store.ParseTime(u.NeedsLoginSince)) >= loginWait
}

// onTheWay: an answer from the way to central rather than about the request: a
// redirect (not followed), a timeout or a rate limit. Try again next cycle, as for 5xx.
func onTheWay(status int) bool {
	return status/100 == 3 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
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
// No patient attribute values are included. The author's username is, so a supervisor
// knows whom to tell, and the record's person, for a link to it in Capture (the
// Facility Hub app's hub page).
func (w *Worker) ReportIssues(ctx context.Context) error {
	h := w.H
	issues, err := h.Store.Issues(true, 500)
	if err != nil {
		return err
	}
	slices.Reverse(issues) // oldest first
	for _, is := range issues {
		var userUID, username string
		if u, _ := h.Store.UserByID(is.UserID); u != nil {
			userUID, username = u.UserUID, u.Username
		}
		var stored struct {
			Message      string                `json:"message"`
			ErrorReports []tracker.ErrorReport `json:"errorReports"`
		}
		json.Unmarshal(is.Error, &stored)
		errBody := map[string]any{"message": tracker.OutgoingMessage(stored.Message, stored.ErrorReports),
			"errorReports": tracker.OutgoingReports(stored.ErrorReports, w.sentValues(is))}
		payload := map[string]any{"hub_id": h.HubID(), "facility": h.Facility(), "user": userUID, "username": username, "objectUid": is.ObjectUID,
			"kind": is.Kind, "outboxSeq": is.OutboxSeq, "error": errBody, "createdAt": is.CreatedAt}
		// An enrollment's or event's person ("" for an event programme's event): the app's
		// link to the record in Capture needs it to tell the two kinds of event apart.
		o, _ := h.Store.GetObject(is.ObjectUID)
		if o != nil && (is.Kind == store.KindEnrollment || is.Kind == store.KindEvent) {
			payload["trackedEntity"] = o.TE
		}
		if o != nil && o.SyncState == store.SyncSuperseded {
			// Central's version changed after the refusal: the hub shows central's, and
			// the refused change waits for a hub manager to resend or discard it.
			payload["superseded"] = true
		}
		key := fmt.Sprintf("%s-%d", h.HubID(), is.ID)
		err := h.Central.JSON(ctx, "POST", "/api/dataStore/facility-hub-issues/"+key, payload, h.HubAuth(), nil)
		if central.Status(err) == http.StatusConflict {
			// Reported before: reported again because it was superseded since.
			err = h.Central.JSON(ctx, "PUT", "/api/dataStore/facility-hub-issues/"+key, payload, h.HubAuth(), nil)
		}
		if err != nil {
			return fmt.Errorf("report issue %d: %w", is.ID, err)
		}
		h.Store.MarkIssueReported(is.ID)
	}
	return w.clearResolvedIssues(ctx)
}

// clearResolvedIssues removes issues that are no longer open (sent again and accepted,
// or discarded) from central's dataStore and from the hub:
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
func issueGroup(h *core.Hub, is *store.Issue) (program string, codes map[string]bool, blockedBy string, superseded bool) {
	var e struct {
		Message string `json:"message"`
		Reports []struct {
			ErrorCode string `json:"errorCode"`
		} `json:"errorReports"`
	}
	json.Unmarshal(is.Error, &e)
	if o := h.Index.Get(is.ObjectUID); o != nil {
		program, superseded = o.Program, o.SyncState == store.SyncSuperseded
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
	return program, codes, blockedBy, superseded
}

// refusalGroup is what an open issue is grouped by (issueGroup).
type refusalGroup struct {
	program string
	codes   map[string]bool
}

// refusals are the hub's open issues, with what each is grouped by and what each
// blocked.
type refusals struct {
	issues  []*store.Issue
	groups  map[int64]refusalGroup
	blocked map[string][]*store.Issue // by the object that blocked them
}

func (w *Worker) openRefusals() (*refusals, error) {
	h := w.H
	issues, err := h.Store.UnresolvedIssues(100000)
	if err != nil {
		return nil, err
	}
	r := &refusals{issues: issues, groups: map[int64]refusalGroup{}, blocked: map[string][]*store.Issue{}}
	for _, is := range issues {
		program, codes, by, _ := issueGroup(h, is)
		if by != "" {
			r.blocked[by] = append(r.blocked[by], is)
		} else if o := h.Index.Get(is.ObjectUID); codes["E5000"] && o != nil {
			// Central's "cannot be created because <parent> ... could not be created".
			for _, p := range []string{o.TE, o.Enrollment} {
				if p != "" && p != o.UID {
					r.blocked[p] = append(r.blocked[p], is)
				}
			}
		}
		r.groups[is.ID] = refusalGroup{program, codes}
	}
	return r, nil
}

// with returns the open issues pick chooses, with what they blocked, newest first.
func (r *refusals) with(pick func(*store.Issue, refusalGroup) bool) []*store.Issue {
	sel := map[int64]bool{}
	var queue []string
	for _, is := range r.issues {
		if pick(is, r.groups[is.ID]) {
			sel[is.ID] = true
			queue = append(queue, is.ObjectUID)
		}
	}
	seen := map[string]bool{}
	for ; len(queue) > 0; queue = queue[1:] {
		if seen[queue[0]] {
			continue
		}
		seen[queue[0]] = true
		for _, is := range r.blocked[queue[0]] {
			sel[is.ID] = true
			queue = append(queue, is.ObjectUID)
		}
	}
	return slices.DeleteFunc(slices.Clone(r.issues), func(is *store.Issue) bool { return !sel[is.ID] })
}

// retry is a resend instruction from the Facility Hub app (facility-hub-admin/retries).
// User, if given, is the hub user to send the saves as instead of their authors.
// Action "discard" throws the saves away instead (discard.go); "" or "resend" sends
// them again. Issue, if given, is one refused change's key in facility-hub-issues
// (<hub ID>-<n>) in place of a group: a key of another hub's names nothing here.
// OrgUnits, for a group, are the org units of the manager who asked: only hubs below
// them take it (review R-30a). At is when it was asked: a group takes only saves
// refused before then.
type retry struct {
	ID, Kind, Program, Code, At, User, Action, Issue string
	OrgUnits                                         []string
}

// forThisHub reports whether a group instruction is for this hub: its facility is
// below one of the instruction's org units. One without them is for no hub.
func forThisHub(r retry, facilityPath string) bool {
	return slices.ContainsFunc(r.OrgUnits, func(ou string) bool {
		return ou != "" && slices.Contains(strings.Split(facilityPath, "/"), ou)
	})
}

const (
	metaRetriesApplied    = "retries_applied"     // the retry IDs this hub has applied
	metaRetriesNotApplied = "retries_not_applied" // why it applied some of them not at all, by ID (JSON)
)

// applyRetries takes up the resend instructions a hub manager gave in the Facility Hub
// app after central was fixed: this hub's saves refused with that kind, programme and
// error code, and what they blocked, are sent again as their authors, or as the user
// the instruction names, once per instruction, when the hub first sees it. Central's
// new answer stands. A discard instruction throws the same saves away (discard.go).
func (w *Worker) applyRetries(ctx context.Context) error {
	h := w.H
	if !h.Approved() {
		return nil // no move key: no code to check (review R-07)
	}
	var raw json.RawMessage
	var v struct {
		Retries []retry `json:"retries"`
	}
	const kind = "facility-hub-admin/retries"
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/"+kind, nil, h.HubAuth(), &raw)
	if central.Status(err) == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if len(v.Retries) == 0 {
		h.NoteUnsigned(kind, false)
		return nil
	}
	// Instructions can discard refused saves or resend them as someone else: only
	// signed for this hub, and nothing of an entry that is not (review R-07).
	if !h.Signed(kind, raw) {
		return nil
	}
	applied := slices.DeleteFunc(strings.Split(h.Store.Meta(metaRetriesApplied), ","), func(id string) bool { return id == "" })
	var notApplied map[string]string
	json.Unmarshal([]byte(h.Store.Meta(metaRetriesNotApplied)), &notApplied)
	kept := map[string]string{}
	var todo []retry
	// The applied set only grows: the app may briefly publish {} while it rewrites the
	// entry, and forgetting what was applied then would run old discards again.
	ids := slices.Clone(applied)
	for _, r := range v.Retries {
		if !slices.Contains(ids, r.ID) {
			ids = append(ids, r.ID)
		}
		if why, ok := notApplied[r.ID]; ok {
			kept[r.ID] = why
		}
		if !slices.Contains(applied, r.ID) {
			todo = append(todo, r)
		}
	}
	if len(todo) > 0 {
		open, err := w.openRefusals()
		if err != nil {
			return err
		}
		// picked: the open issues an instruction names, with what they blocked. A group
		// takes only saves refused before it was asked (review R-30a): a later refusal
		// was not what the manager looked at.
		picked := func(r retry) []*store.Issue {
			at, _ := time.Parse(time.RFC3339Nano, r.At)
			return open.with(func(is *store.Issue, g refusalGroup) bool {
				if r.Issue != "" {
					return r.Issue == fmt.Sprintf("%s-%d", h.HubID(), is.ID)
				}
				return is.Kind == r.Kind && g.program == r.Program && g.codes[r.Code] && store.ParseTime(is.CreatedAt).Before(at)
			})
		}
		reassigned := false
		for _, r := range todo {
			if r.Issue == "" && !forThisHub(r, h.FacilityPath()) {
				slog.Info("retry from the Facility Hub app for other hubs' org units: not applied here", "id", r.ID, "orgUnits", r.OrgUnits)
				continue
			}
			switch r.Action {
			case "", "resend":
			case discardAction:
				why, err := w.discard(ctx, picked(r))
				if err != nil {
					return err
				}
				if why != "" {
					kept[r.ID] = why
					slog.Warn("discard from the Facility Hub app not applied in full", "id", r.ID, "reason", why)
				}
				continue
			default:
				kept[r.ID] = "unknown action " + r.Action
				continue
			}
			var as int64 // 0: as their authors
			if r.User != "" {
				u, err := h.Store.UserByName(r.User)
				if err != nil {
					return err
				}
				switch {
				case u == nil:
					kept[r.ID] = "user " + r.User + " is not known on this hub"
				case u.CannotPush():
					kept[r.ID] = "user " + r.User + " cannot send: they must log in online on this hub first"
				default:
					as = u.ID
				}
				if as == 0 {
					slog.Warn("retry from the Facility Hub app not applied", "id", r.ID, "reason", kept[r.ID])
					continue
				}
			}
			uids, seqs := map[string]bool{}, map[int64]bool{}
			for _, is := range picked(r) {
				if is.Kind == store.OutboxAggregate && w.aggregateReplaced(is) {
					// A later change of the cell reached central: sending this edit
					// again would undo it. It is done, and its issue closes.
					h.Store.FinishOutbox(is.OutboxSeq, store.OutboxSynced, "replaced by a later change")
					continue
				}
				uids[is.ObjectUID], seqs[is.OutboxSeq] = true, true
			}
			n, err := h.Store.Requeue(slices.Collect(maps.Keys(uids)), slices.Collect(maps.Keys(seqs)), as)
			if err != nil {
				return err
			}
			reassigned = reassigned || (as != 0 && n > 0)
			slog.Info("retry from the Facility Hub app", "id", r.ID, "as", r.User, "objects", len(uids), "entries", n)
		}
		if reassigned {
			// Saves sent as another user get central's answer to that user: their old
			// issues go, so a refusal comes back with central's reason.
			if err := w.clearResolvedIssues(ctx); err != nil {
				return err
			}
		}
	}
	b, _ := json.Marshal(kept)
	h.Store.SetMeta(metaRetriesNotApplied, string(b))
	h.Store.SetMeta(metaRetriesApplied, strings.Join(ids, ","))
	return nil
}
