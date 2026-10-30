package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dhis2/facility-hub/internal/access"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/compat"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// --- pull (spec 11.3) ----------------------------------------------------------------

func (w *Worker) Pull(ctx context.Context) error {
	h := w.H
	users, err := h.Store.Users()
	if err != nil {
		return err
	}
	var failed []error
	readers := map[string]*puller{} // program -> a user whose pull of it succeeded
	// Users central answers alike share one listing (access.List.GroupKey): listing
	// per user was most of what a hub asked of central (docs/PERFORMANCE.md).
	groups := map[string][]*puller{}
	var order []string
	for _, u := range users {
		if u.CannotPush() {
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
		k := acc.GroupKey()
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], &puller{u: u, acc: acc, auth: auth})
	}
	for _, k := range order {
		if err := w.pullGroup(ctx, groups[k], readers, &failed); err != nil {
			return err
		}
	}
	if err := w.checkOwnership(ctx, h.API(), h.Facility(), readers); err != nil {
		if central.IsNetwork(err) {
			return err
		}
		failed = append(failed, fmt.Errorf("ownership check: %w", err))
	}
	now := store.Now()
	h.Stats.Set(func(s *core.SyncStats) { s.LastPull = now })
	h.Store.SetPrivate("last_pull", now)
	return errors.Join(failed...)
}

// puller is a user whose view the hub pulls.
type puller struct {
	u    *store.User
	acc  *access.List
	auth central.Auth
}

func (p *puller) accKey() string { return "pull_access:" + strconv.FormatInt(p.u.ID, 10) }

func (p *puller) accJSON() string {
	b, _ := json.Marshal(p.acc)
	return string(b)
}

// pullGroup pulls the view of users with the same access, listing as the first of
// them and applying the result to all. It returns only network errors; others are
// added to failed.
func (w *Worker) pullGroup(ctx context.Context, g []*puller, readers map[string]*puller, failed *[]error) error {
	h := w.H
	cat := h.Catalog()
	acc := g[0].acc
	// A user whose access changed since their last pull gets a full pull, so what
	// they gained or lost shows at this sync rather than the next weekly one. The
	// group pulls in full if any member needs it.
	full := false
	for _, m := range g {
		full = full || h.Store.Private(m.accKey()) != m.accJSON()
	}
	failedBefore := len(*failed)
	var programs []string
	var unreadable []string // programs the users may not read: a full pull settles them empty
	for _, p := range cat.ProgramIDs() {
		pm := cat.Programs[p]
		if !acc.Program(p).Read || (pm != nil && pm.TET != "" && !acc.TET(pm.TET).Read) {
			unreadable = append(unreadable, p) // central would refuse the search (E1006 without tracked entity type access)
			continue
		}
		programs = append(programs, p)
		for _, m := range g {
			_, lastFull := h.Store.PullState(m.u.ID, p)
			if t := store.ParseTime(lastFull); t.IsZero() || time.Since(t) > h.Cfg.FullPullInterval {
				full = true
			}
		}
	}
	// Also each readable tracked entity type, for people enrolled in nothing the
	// users can read (found by the differential oracle, docs/TESTING.md).
	for _, tet := range cat.TETs() {
		if acc.TET(tet).Read {
			programs = append(programs, "tet:"+tet)
		}
	}
	// The listing's start, in central's time: a full pull keeps visible what the hub
	// wrote or passed on to central after this moment, even though the listing (taken
	// from here) did not include it (S2).
	fullSince := ""
	if full {
		fullSince = h.CentralNow().Format(tracker.DHIS2Layout)
	}
	shared := map[uint32]bool{} // slots listed for the group
	var settled []string        // programs whose visibility this pull may decide, for all
	own := map[int64]map[uint32]bool{}
	ownSettled := map[int64][]string{}
	stopped := false // the listing user can no longer pull: the group's pull is not complete
	for _, p := range programs {
		if pm := cat.Programs[p]; len(g) > 1 && pm != nil && (pm.AttributeCombo || pm.EnrollmentAttributeCombo) {
			// Its events also depend on each user's access to category options, which
			// the group key does not cover: listed per user.
			for _, m := range g {
				if own[m.u.ID] == nil {
					own[m.u.ID] = map[uint32]bool{}
				}
				ok, stop, err := w.pullFor(ctx, []*puller{m}, p, full, own[m.u.ID], readers, failed)
				if err != nil {
					return err
				}
				if ok {
					ownSettled[m.u.ID] = append(ownSettled[m.u.ID], p)
				}
				if stop {
					stopped = true
					break
				}
			}
			continue
		}
		ok, stop, err := w.pullFor(ctx, g, p, full, shared, readers, failed)
		if err != nil {
			return err
		}
		if ok {
			settled = append(settled, p)
		}
		if stop {
			stopped = true
			break
		}
	}
	rep := g[0]
	// What was listed as the first member is visible to all of them.
	if len(g) > 1 && len(shared) > 0 {
		slots := make([]uint32, 0, len(shared))
		for s := range shared {
			slots = append(slots, s)
		}
		for _, m := range g[1:] {
			if err := h.MarkVisible(m.u.ID, slots); err != nil {
				*failed = append(*failed, fmt.Errorf("visibility for %s: %w", m.u.Username, err))
			}
		}
	}
	if len(*failed) == failedBefore && !stopped { // the group's pull completed
		for _, m := range g {
			h.Store.SetPrivate(m.accKey(), m.accJSON())
		}
	}
	if full {
		for _, m := range g {
			decided := slices.Concat(settled, unreadable, ownSettled[m.u.ID])
			if len(decided) == 0 {
				continue
			}
			// Tracked entities belong to no program: only a pull of every program decides
			// them, else a person listed only by a failed one would vanish.
			if len(settled)+len(ownSettled[m.u.ID]) == len(programs) {
				decided = append(decided, "")
			}
			seen := shared
			if len(own[m.u.ID]) > 0 {
				seen = maps.Clone(shared)
				maps.Copy(seen, own[m.u.ID])
			}
			if n := h.RemoveVisibilityExcept(m.u.ID, decided, seen, fullSince); n > 0 {
				slog.Info("full pull removed visibility", "user", m.u.Username, "objects", n)
			}
		}
	}
	if len(g) > 1 {
		slog.Info("pulled for an access group", "users", len(g), "listedAs", rep.u.Username, "full", full)
	}
	return nil
}

// pullFor pulls one program for users with the same access, listing as the first of
// them; incremental pulls start from the oldest of their last pulls, so none misses a
// change. ok: the pull decided the program's visibility. stop: the listing user can
// no longer pull.
func (w *Worker) pullFor(ctx context.Context, g []*puller, p string, full bool, seen map[uint32]bool,
	readers map[string]*puller, failed *[]error) (ok, stop bool, err error) {
	h := w.H
	rep := g[0]
	after := ""
	if !full {
		for i, m := range g {
			last, _ := h.Store.PullState(m.u.ID, p)
			if i == 0 || last < after {
				after = last
			}
		}
	}
	var maxUpdated string
	var n int
	var perr error
	pm := h.Catalog().Programs[p]
	if pm != nil && pm.Event {
		maxUpdated, n, perr = w.pullEvents(ctx, h.API(), g, h.Facility(), p, after, seen)
	} else {
		api, facility := h.API(), h.Facility()
		lists := []func(page int) string{func(page int) string { return api.ListPath(facility, p, after, page, listPageSize) }}
		cutoff, windowed := w.window(p)
		if full && windowed {
			// Retention: a full pull lists the people with an active enrollment and those
			// changed since the cut-off (a type: only the latter), not everyone ever.
			lists = []func(page int) string{func(page int) string { return api.ListPath(facility, p, cutoff, page, listPageSize) }}
			if !strings.HasPrefix(p, "tet:") {
				lists = append(lists, func(page int) string { return api.ActivePath(facility, p, page, listPageSize) })
			}
		}
		for _, list := range lists {
			var mu string
			var k int
			mu, k, perr = w.pullProgram(ctx, api, rep.auth, rep.u.ID, facility, p, list, seen)
			maxUpdated, n = max(maxUpdated, mu), n+k
			if perr != nil {
				break
			}
		}
		if full && windowed && maxUpdated == "" {
			maxUpdated = cutoff // nothing listed: the next pull still starts at the window
		}
	}
	if perr != nil {
		st := central.Status(perr)
		switch {
		case central.IsNetwork(perr):
			return false, true, fmt.Errorf("pull %s/%s: %w", rep.u.Username, p, perr)
		case st == http.StatusUnauthorized:
			h.Store.SetNeedsLogin(rep.u.ID, true)
			return false, true, nil
		case st >= 400 && st < 500 && !onTheWay(st):
			// Central refuses this access the program: they lose what they saw of it,
			// and the other pulls go on.
			slog.Warn("pull refused by central", "user", rep.u.Username, "program", p, "err", perr)
			return true, false, nil
		default:
			// A server error, or a rate limit on the way, says nothing about access: keep
			// what the users see.
			slog.Warn("pull failed", "user", rep.u.Username, "program", p, "err", perr)
			*failed = append(*failed, fmt.Errorf("pull %s/%s: %w", rep.u.Username, p, perr))
			return false, false, nil
		}
	}
	if _, ok := readers[p]; !ok && !strings.HasPrefix(p, "tet:") && (pm == nil || !pm.Event) {
		readers[p] = rep
	}
	if maxUpdated == "" {
		maxUpdated = after
	}
	for _, m := range g {
		if full {
			// Full pulls record their own time so the weekly check works.
			h.Store.SetPullState(m.u.ID, p, maxUpdated, true)
			h.Store.SetFullPullTime(m.u.ID, p)
		} else {
			h.Store.SetPullState(m.u.ID, p, maxUpdated, false)
		}
	}
	slog.Info("pulled", "user", rep.u.Username, "users", len(g), "program", p, "full", full, "objects", n)
	return true, false, nil
}

// pullProgram pulls one user's view of one program (or "tet:<type>") in two steps:
// it lists what central shows the user, identifiers and update times only, then
// fetches in full only the tracked entities the hub lacks or holds older, or has not
// yet fetched for this program's attributes. Everything listed becomes visible to the
// user. So central still decides per user, while each record's content crosses the
// uplink once, whoever is first to see it (docs/PERFORMANCE.md). It returns the
// newest updatedAt listed (central's clock, used as the next updatedAfter).
func (w *Worker) pullProgram(ctx context.Context, api compat.API, auth central.Auth, userID int64,
	facility, program string, list func(page int) string, seen map[uint32]bool) (string, int, error) {
	h := w.H
	maxUpdated, total := "", 0
	covered := w.scopeCovers(program)
	cutoff, windowed := w.window(program)
	stale := &staleSet{}
	var current []uint32
	for page := 1; ; page++ {
		items, more, err := w.getPage(ctx, api, auth, list(page), page, listPageSize)
		if err != nil {
			return "", total, err
		}
		// What the hub holds of the page's objects, read in one go.
		flat := make([][]*tracker.Item, len(items))
		var uids []string
		for i, te := range items {
			flat[i] = tracker.Flatten(tracker.M{"trackedEntities": []any{te}})
			for _, it := range flat[i] {
				uids = append(uids, it.UID)
			}
		}
		held, err := h.Store.ObjectStates(uids)
		if err != nil {
			return "", total, err
		}
		for i, te := range items {
			uid := tracker.Str(te, "trackedEntity")
			isStale := !covered(uid)
			var listed []string
			var slots []uint32
			underStub := stubsUnchanged(flat[i], held, cutoff, windowed)
			for _, it := range flat[i] {
				upd := tracker.Str(it.Data, "updatedAt")
				maxUpdated = max(maxUpdated, upd)
				if underStub[it.Enrollment] {
					continue // pruned by retention: what is under an unchanged stub counts as held
				}
				have, ok := held[it.UID]
				if !ok || have.UpdatedAt < upd {
					isStale = true
				}
				listed = append(listed, it.UID)
				slots = append(slots, uint32(have.Slot))
			}
			total += len(listed)
			if isStale {
				stale.tes = append(stale.tes, uid)
				stale.listed = append(stale.listed, listed...)
			} else {
				for _, s := range slots {
					seen[s] = true
				}
				current = append(current, slots...)
			}
		}
		if !more {
			break
		}
	}
	if err := w.fetchStale(ctx, api, auth, userID, facility, program, stale, seen); err != nil {
		return "", total, err
	}
	if err := h.MarkVisible(userID, current); err != nil {
		return "", total, err
	}
	return maxUpdated, total, nil
}

// stubsUnchanged returns the enrollments of a listed person that the hub holds as stubs
// (tracker retention) with the update time central lists, still before the program's
// cut-off: their events are not held, and fetching the person for them would only
// store the stub again, at every full pull. A stub the window no longer covers (it was
// widened or removed) is fetched again, in full.
func stubsUnchanged(items []*tracker.Item, held map[string]store.ObjectState, cutoff string, windowed bool) map[string]bool {
	out := map[string]bool{}
	if !windowed {
		return out
	}
	for _, it := range items {
		if have, ok := held[it.UID]; ok && it.Kind == store.KindEnrollment && have.Stub && have.UpdatedAt < cutoff &&
			have.UpdatedAt == tracker.Str(it.Data, "updatedAt") {
			out[it.UID] = true
		}
	}
	return out
}

// window is the retention cut-off of a tracker program, or of a tracked entity type
// for "tet:<type>"; ok is false when it keeps everything.
func (w *Worker) window(scope string) (string, bool) {
	ret, now := w.H.Retention(), w.H.CentralNow()
	if tet, ok := strings.CutPrefix(scope, "tet:"); ok {
		return ret.TypeCutoff(tet, now)
	}
	return ret.Cutoff(scope, now)
}

// staleSet is tracked entities to fetch in full, with what was listed under them.
type staleSet struct {
	tes, listed []string
}

// fetchStale fetches these tracked entities in full, 50 per request, and adds what is
// now stored of them to seen.
func (w *Worker) fetchStale(ctx context.Context, api compat.API, auth central.Auth, userID int64, facility, program string,
	s *staleSet, seen map[uint32]bool) error {
	h := w.H
	for start := 0; start < len(s.tes); start += fetchBatch {
		batch := s.tes[start:min(start+fetchBatch, len(s.tes))]
		items, _, err := w.getPage(ctx, api, auth, api.FetchPath(facility, program, batch), 1, len(batch))
		if err != nil {
			return err
		}
		anyItems := make([]any, len(items))
		for i, te := range items {
			anyItems[i] = te
		}
		if _, err := h.UpsertCentral(userID, tracker.M{"trackedEntities": anyItems}, true); err != nil {
			return err
		}
		if err := h.RecordScopes(batch, program); err != nil {
			return err
		}
	}
	// The fetched ones are stored now, with their slots.
	if len(s.listed) > 0 {
		st, err := h.Store.ObjectStates(s.listed)
		if err != nil {
			return err
		}
		for _, o := range st {
			seen[uint32(o.Slot)] = true
		}
	}
	return nil
}

// scopeCovers tells whether a tracked entity was fetched in full for scope. A type
// scope ("tet:<type>") is also covered by a fetch through any program of that type:
// central answers a program fetch with the same person fields, the type's attributes
// and relationships, plus that program's attributes and enrollments. Without this,
// the type pull fetched everyone already fetched through a program a second time.
func (w *Worker) scopeCovers(scope string) func(te string) bool {
	tet, isType := strings.CutPrefix(scope, "tet:")
	cat := w.H.Catalog()
	return func(te string) bool {
		if !isType || cat == nil {
			return w.H.Index.HasScope(te, scope)
		}
		for _, s := range w.H.Index.Scopes(te) {
			if s == scope {
				return true
			}
			if p := cat.Programs[s]; p != nil && p.TET == tet {
				return true
			}
		}
		return false
	}
}

// ownershipVerifyMax bounds the single reads of one ownership check.
const ownershipVerifyMax = 500

// checkOwnership finds enrollments the hub holds as the facility's that central no
// longer lists as owned by it: a transfer out does not show in incremental pulls, and
// waiting for the daily full pull would leave the person visible offline. Per program
// it lists the owned tracked entities' identifiers (as a user who just pulled it), and
// each held one missing from the list is read once to learn its owner. The other way
// round, people the hub holds as owned elsewhere that central lists as the facility's
// again (transferred back) are fetched again, which makes the facility their owner,
// and shown to the users the local rules allow: no pull would fetch them, once another
// scope's fetch has taken the updatedAt the transfer gave them (C4, FINDINGS.md). Runs
// at most every ownership_check_interval.
func (w *Worker) checkOwnership(ctx context.Context, api compat.API, facility string, readers map[string]*puller) error {
	h := w.H
	if last := store.ParseTime(h.Store.Private("last_ownership_check")); !last.IsZero() && time.Since(last) < h.Cfg.OwnershipCheckInterval {
		return nil
	}
	verified := 0
	for program, r := range readers {
		auth := r.auth
		owned := map[string]bool{}
		for page := 1; ; page++ {
			items, more, err := w.getPage(ctx, api, auth, api.OwnedPath(facility, program, page, ownedPageSize), page, ownedPageSize)
			if err != nil {
				return err
			}
			for _, te := range items {
				owned[tracker.Str(te, "trackedEntity")] = true
			}
			if !more {
				break
			}
		}
		suspects := map[string]bool{}
		for _, te := range h.Index.EnrolledTEs(program, facility) {
			if !owned[te] {
				suspects[te] = true
			}
		}
		for te := range suspects {
			if verified >= ownershipVerifyMax {
				slog.Warn("ownership check: more suspects than one check reads; the rest wait", "program", program)
				break
			}
			verified++
			owner, err := w.ownerOf(ctx, auth, te, program)
			if err != nil {
				return err
			}
			if owner != facility {
				if err := h.ApplyTransfer(te, program, owner); err != nil {
					return err
				}
			}
		}
		if err := w.transferredBack(ctx, api, r, facility, program, owned); err != nil {
			return err
		}
	}
	h.Store.SetPrivate("last_ownership_check", store.Now())
	return nil
}

const ownedPageSize = 10000

// transferredBack fetches again, as r, the people the hub holds as owned elsewhere in
// program that central lists as owned by the facility, and shares them by the local
// rules (checkOwnership).
func (w *Worker) transferredBack(ctx context.Context, api compat.API, r *puller, facility, program string, owned map[string]bool) error {
	h := w.H
	back := slices.DeleteFunc(h.Index.EnrolledElsewhere(program, facility), func(te string) bool { return !owned[te] })
	if len(back) == 0 {
		return nil
	}
	if err := w.fetchStale(ctx, api, r.auth, r.u.ID, facility, program, &staleSet{tes: back}, map[uint32]bool{}); err != nil {
		return err
	}
	var uids []string
	for _, te := range back {
		for _, e := range h.Index.EnrollmentsOf(te) {
			if e.Program == program && e.OwnerOrgUnit == facility {
				uids = append(uids, e.UID)
				for _, ev := range h.Index.EventsOf(e.UID) {
					uids = append(uids, ev.UID)
				}
			}
		}
	}
	h.ShareByRules(uids)
	slog.Info("ownership moved back", "program", program, "trackedEntities", len(back), "objects", len(uids))
	return nil
}

// ownerOf reads who owns te in program: "" when the user may not open it there
// (owned where they have no access) or it is gone.
func (w *Worker) ownerOf(ctx context.Context, auth central.Auth, te, program string) (string, error) {
	var res struct {
		ProgramOwners []struct {
			OrgUnit string `json:"orgUnit"`
			Program string `json:"program"`
		} `json:"programOwners"`
	}
	path := central.Q("/api/tracker/trackedEntities/"+te, "program", program, "fields", "programOwners")
	if err := w.H.Central.JSON(ctx, "GET", path, nil, auth, &res); err != nil {
		if st := central.Status(err); st == http.StatusForbidden || st == http.StatusNotFound {
			return "", nil
		}
		return "", err
	}
	for _, po := range res.ProgramOwners {
		if po.Program == program {
			return po.OrgUnit, nil
		}
	}
	return w.H.Facility(), nil // no owner recorded: still the facility's (registered here)
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
