package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// pushNote makes again, as its author, Capture's request that added a note offline:
// POST /api/tracker/{events|enrollments}/{uid}/note with the note's UID and value.
// Central adds the note and changes nothing else on the record, so a note cannot undo
// a change made on central meanwhile (PENDING.md, item 24; checked on 2.42.7.1 and
// 2.43.2.1). A note central already has (400, E1003: a push whose answer was lost) is
// sent. Any other refusal, such as a record deleted on central (404), is kept as an
// issue, which a hub manager can Resend or Discard.
func (w *Worker) pushNote(ctx context.Context, e *store.OutboxEntry) (pushResult, error) {
	h := w.H
	var p core.NotePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil || p.UID == "" {
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, fmt.Sprintf("corrupt payload: %v", err))
	}
	refuse := func(code, message string) (pushResult, error) {
		issue, _ := json.Marshal(map[string]any{"message": message,
			"errorReports": []map[string]string{{"errorCode": code, "message": message}}, "note": p.Note})
		if err := h.Store.AddIssue(e.Seq, p.UID, store.OutboxNote, e.UserID, issue, ""); err != nil {
			slog.Error("add issue", "err", err)
		}
		slog.Warn("note refused", "seq", e.Seq, "record", p.UID, "code", code)
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, message)
	}
	if w.authorGone(e.UserID) {
		return refuse(tracker.NoLoginCode, tracker.NoLoginMessage)
	}
	auth, err := h.UpstreamAuth(e.UserID)
	if err != nil {
		h.Store.SetNeedsLogin(e.UserID, true)
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{userBlocked: true}, nil
	}
	body, _ := json.Marshal(map[string]any{"note": p.Note["note"], "value": p.Note["value"]})
	path := "/api/tracker/" + p.Collection + "/" + url.PathEscape(p.UID) + "/note"
	resp, err := h.Central.Do(ctx, "POST", path, body, auth, http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{stop: true}, err
	}
	w.pushAnswered = true
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var msg struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	json.Unmarshal(rb, &msg)
	switch {
	case resp.StatusCode/100 == 2, resp.StatusCode == http.StatusBadRequest && msg.ErrorCode == "E1003":
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxSynced, "")
	case resp.StatusCode == http.StatusUnauthorized:
		h.Store.SetNeedsLogin(e.UserID, true)
		h.Store.NoteAttempt(e.Seq, "401 from central: needs online login")
		return pushResult{userBlocked: true}, nil
	case resp.StatusCode == http.StatusInternalServerError:
		if time.Since(h.Store.FailingSince(e.Seq)) < failingRefusedAfter {
			h.Store.NoteAttempt(e.Seq, "central returned 500")
			return pushResult{}, nil
		}
	case resp.StatusCode >= 500 || onTheWay(resp.StatusCode):
		h.Store.NoteAttempt(e.Seq, fmt.Sprintf("central returned %d", resp.StatusCode))
		return pushResult{stop: true}, nil
	}
	if msg.Message == "" {
		msg.Message = fmt.Sprintf("central returned %d", resp.StatusCode)
	}
	return refuse(msg.ErrorCode, msg.Message)
}
