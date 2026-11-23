package sync

import (
	"context"
	"log/slog"
	"net/url"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// countOverwrites reads, before the outbox entry e is pushed, central's current
// updatedAt of each object e edits that the hub held as central's, and counts those
// central changed after the copy the save edited: the save is about to replace a newer
// change (PENDING.md, item 3; store/overwrites.go). Telemetry only: the push goes
// ahead whatever this finds, and a read that fails is not counted.
//
// The cost on central: one small read per object an entry edits (fields updatedAt),
// only for edits of objects pulled from central; registrations, new events and edits
// of what the hub made itself cost nothing. Each entry is checked once, and only one
// entry in OverwriteSample: measured on 2.42.7.1 (17 November 2026, a token, median of
// 30), a read took 65 to 94 ms and the push of a one-event entry 133 ms, so reading
// for every entry would add about half again to central's work for updates; one in
// ten adds about 5 %. The heartbeat says the count is a sample.
func (w *Worker) countOverwrites(ctx context.Context, e *store.OutboxEntry, auth central.Auth) {
	h := w.H
	bases, err := h.Store.TakeOutboxBases(e.Seq)
	if err != nil || len(bases) == 0 || h.OverwriteCountOff() || e.Seq%int64(w.overwriteSample()) != 0 {
		return
	}
	user := ""
	if u, _ := h.Store.UserByID(e.UserID); u != nil {
		user = u.Username
	}
	for _, b := range bases {
		path := map[string]string{store.KindTrackedEntity: "/api/tracker/trackedEntities/",
			store.KindEnrollment: "/api/tracker/enrollments/", store.KindEvent: "/api/tracker/events/"}[b.Kind]
		if path == "" {
			continue
		}
		var now struct {
			UpdatedAt string `json:"updatedAt"`
		}
		if err := h.Central.JSON(ctx, "GET", central.Q(path+url.PathEscape(b.UID), "fields", "updatedAt"), nil, auth, &now); err != nil {
			continue
		}
		base, ok1 := updatedTime(b.UpdatedAt)
		cur, ok2 := updatedTime(now.UpdatedAt)
		if !ok1 || !ok2 || !cur.After(base) {
			continue
		}
		o := store.Overwrite{UID: b.UID, Kind: b.Kind, User: user, Base: b.UpdatedAt, Central: now.UpdatedAt,
			Saved: e.CreatedAt, Pushed: store.Now()}
		if obj := h.Index.Get(b.UID); obj != nil {
			o.Program = obj.Program
		}
		slog.Info("an offline save replaces a newer change on central", "kind", b.Kind, "uid", b.UID,
			"user", user, "base", b.UpdatedAt, "central", now.UpdatedAt)
		h.Store.AddOverwrite(o)
	}
}

// OverwriteSample is the default of Worker.OverwriteSample.
const OverwriteSample = 10

func (w *Worker) overwriteSample() int {
	if w.OverwriteSample > 0 {
		return w.OverwriteSample
	}
	return OverwriteSample
}

// updatedTime parses an updatedAt as central writes it (its own zone, no offset).
func updatedTime(s string) (time.Time, bool) {
	for _, l := range []string{tracker.DHIS2Layout, "2006-01-02T15:04:05.999", "2006-01-02T15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
