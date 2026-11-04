package sync

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// discardAction is a retry instruction's action that throws refused saves away.
const discardAction = "discard"

// discard throws away refused saves a hub manager discarded in the Facility Hub app,
// for a change on central that the waiting data does not fit, or a duplicate. For each
// refused tracker object the hub reads central's version by UID, as the object's
// author, and keeps that. Where central has none (a refused registration), it keeps a
// tombstone, so Android's next download drops the record. Its update time is the hub's
// now, in central's time, so that download includes it. The refused outbox entries
// are marked discarded, and their issues close at the next report. An aggregate edit's
// entry is marked discarded only: the stored form left the edit out at the refusal.
// It returns why some saves were not discarded ("" when all were).
func (w *Worker) discard(ctx context.Context, issues []*store.Issue) (string, error) {
	h := w.H
	var seqs []int64
	author := map[string]int64{} // by object: the author of its newest refusal
	for _, is := range issues {  // newest first
		if is.Kind == store.OutboxAggregate {
			seqs = append(seqs, is.OutboxSeq)
		} else if _, ok := author[is.ObjectUID]; !ok {
			author[is.ObjectUID] = is.UserID
		}
	}
	var why []string
	var done []string
	byAuthor := map[int64][]*tracker.Obj{}
	for uid, a := range author {
		if o := h.Index.Get(uid); o != nil && o.SyncState == store.SyncRejected {
			byAuthor[a] = append(byAuthor[a], o)
		}
	}
	for _, a := range slices.Sorted(maps.Keys(byAuthor)) {
		objs := byAuthor[a]
		name := fmt.Sprint(a)
		if u, _ := h.Store.UserByID(a); u != nil {
			name = u.Username
		}
		auth, err := h.UpstreamAuth(a)
		if err != nil {
			why = append(why, fmt.Sprintf("%d refused saves of %s kept: they must log in online on this hub first", len(objs), name))
			continue
		}
		have, failed, err := w.centralVersions(ctx, auth, objs)
		if err != nil {
			return "", err
		}
		discarded := map[string]bool{}
		var items []*tracker.Item
		now := h.CentralTime(time.Now())
		for _, o := range objs {
			if failed[o.UID] != "" {
				why = append(why, fmt.Sprintf("%s %s kept: %s", o.Kind, o.UID, failed[o.UID]))
				continue
			}
			data := have[o.UID]
			if data == nil {
				data = h.Index.Full(o) // a tombstone of the hub's copy
				data["deleted"] = true
			}
			data["updatedAt"] = now
			discarded[o.UID] = true
			items = append(items, &tracker.Item{Kind: o.Kind, UID: o.UID, Data: data})
			slog.Info("refused save discarded", "uid", o.UID, "kind", o.Kind, "central", have[o.UID] != nil)
		}
		if len(items) == 0 {
			continue
		}
		if _, err := h.ReplaceDiscarded(a, flat(items), discarded); err != nil {
			return "", err
		}
		done = append(done, slices.Collect(maps.Keys(discarded))...)
	}
	if _, err := h.Store.DiscardOutbox(done, seqs); err != nil {
		return "", err
	}
	return strings.Join(why, "; "), nil
}

// centralVersions reads central's versions of these objects by UID, as auth: each
// person once per programme the hub holds them in (central gives a programme's
// attributes with the programme only), with their enrollments and events, and each
// event of an event programme and each relationship alone. have holds the versions
// found, without their children (a child missing from its person's answer is not on
// central); failed says why an object could not be read.
func (w *Worker) centralVersions(ctx context.Context, auth central.Auth, objs []*tracker.Obj) (have map[string]tracker.M,
	failed map[string]string, err error) {
	h := w.H
	have, failed = map[string]tracker.M{}, map[string]string{}
	type read struct{ te, program string }
	reads := map[read][]string{} // the objects each read answers for
	var alone []*tracker.Obj
	for _, o := range objs {
		switch {
		case o.Kind == store.KindTrackedEntity:
			programs := map[string]bool{}
			for _, en := range h.Index.EnrollmentsOf(o.UID) {
				if en.Program != "" {
					programs[en.Program] = true
				}
			}
			if len(programs) == 0 {
				programs[""] = true
			}
			for p := range programs {
				reads[read{o.UID, p}] = append(reads[read{o.UID, p}], o.UID)
			}
		case o.Kind == store.KindEnrollment && o.TE != "",
			o.Kind == store.KindEvent && o.Enrollment != "" && o.TE != "":
			reads[read{o.TE, o.Program}] = append(reads[read{o.TE, o.Program}], o.UID)
		default:
			alone = append(alone, o)
		}
	}
	get := func(path string, out *tracker.M) (int, error) {
		err := h.Central.JSON(ctx, "GET", path, nil, auth, out)
		if st := central.Status(err); st != 0 {
			return st, nil
		}
		if err != nil {
			return 0, err
		}
		return http.StatusOK, nil
	}
	for r, uids := range reads {
		q := url.Values{"fields": {"*,enrollments[*,events[*]],relationships[*]"}}
		if r.program != "" {
			q.Set("program", r.program)
		}
		var te tracker.M
		st, err := get("/api/tracker/trackedEntities/"+r.te+"?"+q.Encode(), &te)
		if err != nil {
			return nil, nil, err
		}
		switch st {
		case http.StatusOK:
			for _, it := range tracker.Flatten(tracker.M{"trackedEntities": []any{te}}) {
				if it.Kind == store.KindTrackedEntity && have[it.UID] != nil {
					have[it.UID] = tracker.Merge(have[it.UID], it.Data) // another programme's attributes
				} else {
					have[it.UID] = it.Data
				}
			}
		case http.StatusNotFound:
		default:
			for _, u := range uids {
				failed[u] = fmt.Sprintf("central answered %d to reading person %s", st, r.te)
			}
		}
	}
	for _, o := range alone {
		path := "/api/tracker/events/" + o.UID
		if o.Kind == store.KindRelationship {
			path = "/api/tracker/relationships/" + o.UID
		}
		var m tracker.M
		st, err := get(path, &m)
		if err != nil {
			return nil, nil, err
		}
		switch st {
		case http.StatusOK:
			if items := tracker.Flatten(tracker.M{map[string]string{store.KindEvent: "events",
				store.KindRelationship: "relationships"}[o.Kind]: []any{m}}); len(items) > 0 {
				have[o.UID] = items[0].Data
			}
		case http.StatusNotFound:
		default:
			failed[o.UID] = fmt.Sprintf("central answered %d", st)
		}
	}
	return have, failed, nil
}
