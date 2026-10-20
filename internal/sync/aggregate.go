package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// aggFullEvery is how often every held form is fetched again, for lock and min-max
// changes that leave values alone (approval, lock exceptions).
const aggFullEvery = 24 * time.Hour

type aggGroup struct {
	key    string
	reader int64
	auth   central.Auth
	acc    *aggregate.Access
}

// pullAggregate fetches the forms of each data set's window that changed on central
// since the last pull: values (dataValueSets, by lastUpdated) and completions
// (completeDataSetRegistrations). Users whose metadata grants the same data sets and
// options are one group, read as one of them. Forms that left every window are dropped
// unless edits for them wait.
func (w *Worker) pullAggregate(ctx context.Context) error {
	h := w.H
	users, err := h.Store.Users()
	if err != nil {
		return err
	}
	groups := map[string]*aggGroup{}
	for _, u := range users {
		if u.CannotPush() || !h.Access(u.ID).InFacility(h.FacilityPath()) {
			continue
		}
		acc := h.AggAccess(u.ID)
		k := acc.Key()
		if k == "" || groups[k] != nil {
			continue
		}
		auth, err := h.UpstreamAuth(u.ID)
		if err != nil {
			continue
		}
		groups[k] = &aggGroup{key: k, reader: u.ID, auth: auth, acc: acc}
	}
	now, ret, facility := h.CentralNow(), h.AggRetention(), h.Facility()
	held := map[string]bool{}
	fetched := map[string]bool{}
	pulled := map[string]bool{} // data sets some group pulled completely this cycle
	var errs []error
	for _, g := range groups {
		if err := w.pullAggGroup(ctx, g, now, ret, facility, held, fetched, pulled); err != nil {
			if central.IsNetwork(err) {
				return err
			}
			errs = append(errs, err)
		}
	}
	forms, err := h.Store.AggForms()
	if err != nil {
		return err
	}
	// Only forms of data sets pulled this cycle are dropped: a failed pull, or a group
	// whose users cannot be read as now, keeps what the hub holds.
	for _, f := range forms {
		if pulled[f.DataSet] && !held[f.Key] && !h.Store.HasPendingOutboxFor(f.Key, 0) {
			h.Store.DeleteAggForm(f.Key)
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) pullAggGroup(ctx context.Context, g *aggGroup, now time.Time, ret aggregate.Retention, facility string,
	held, fetched, pulled map[string]bool) error {
	h := w.H
	windows := map[string][]aggregate.Period{}
	var start, end time.Time
	for id, ds := range g.acc.DataSets {
		if !ds.Assigned(facility) {
			continue
		}
		win := aggregate.Window(ds, now, ret.For(id))
		if len(win) == 0 {
			continue
		}
		windows[id] = win
		if start.IsZero() || win[0].Start.Before(start) {
			start = win[0].Start
		}
		if last := win[len(win)-1].End; last.After(end) {
			end = last
		}
	}
	if len(windows) == 0 {
		return nil
	}
	// The cursor is central's time: the newest lastUpdated central listed, so the
	// hub's own clock never decides what is new.
	stateKey := "agg_pull:" + g.key
	cursor := h.Store.Private(stateKey + ":cursor")
	full := time.Since(store.ParseTime(h.Store.Private(stateKey+":full"))) > aggFullEvery

	byPeriod := func(ds, pe string) bool {
		for _, p := range windows[ds] {
			if p.ID == pe {
				return true
			}
		}
		return false
	}
	changed := map[aggregate.FormKey]string{} // form -> registration fingerprint ("" if none)
	mark := func(k aggregate.FormKey) {
		if _, ok := changed[k]; !ok {
			changed[k] = ""
		}
	}
	dsIDs := make([]string, 0, len(windows))
	for id := range windows {
		dsIDs = append(dsIDs, id)
	}
	q := url.Values{"dataSet": dsIDs, "orgUnit": {facility}, "startDate": {start.Format(time.DateOnly)},
		"endDate": {end.Format(time.DateOnly)}, "includeDeleted": {"true"}}
	if !full && cursor != "" {
		q.Set("lastUpdated", cursor)
	}
	// 2.43 moves the fields every value shares (period, attribute option combination,
	// org unit) to the top of the answer instead of repeating them on each value.
	var vs struct {
		Period               string `json:"period"`
		AttributeOptionCombo string `json:"attributeOptionCombo"`
		DataValues           []struct {
			DataElement          string `json:"dataElement"`
			Period               string `json:"period"`
			AttributeOptionCombo string `json:"attributeOptionCombo"`
			LastUpdated          string `json:"lastUpdated"`
		} `json:"dataValues"`
	}
	if err := h.Central.JSON(ctx, "GET", "/api/dataValueSets?"+q.Encode(), nil, g.auth, &vs); err != nil {
		return err
	}
	newest := cursor
	for _, v := range vs.DataValues {
		if v.Period == "" {
			v.Period = vs.Period
		}
		if v.AttributeOptionCombo == "" {
			v.AttributeOptionCombo = vs.AttributeOptionCombo
		}
		if lu := centralTime(v.LastUpdated); lu > newest {
			newest = lu
		}
		for id, ds := range g.acc.DataSets {
			if _, in := ds.Elements[v.DataElement]; in && byPeriod(id, v.Period) {
				mark(aggregate.FormKey{DataSet: id, Period: v.Period, OrgUnit: facility, AOC: v.AttributeOptionCombo})
			}
		}
	}
	q.Del("lastUpdated")
	q.Del("includeDeleted")
	var regs struct {
		Registrations []struct {
			Period               string `json:"period"`
			DataSet              string `json:"dataSet"`
			AttributeOptionCombo string `json:"attributeOptionCombo"`
			Date                 string `json:"date"`
			StoredBy             string `json:"storedBy"`
			Completed            bool   `json:"completed"`
		} `json:"completeDataSetRegistrations"`
	}
	if err := h.Central.JSON(ctx, "GET", "/api/completeDataSetRegistrations?"+q.Encode(), nil, g.auth, &regs); err != nil {
		return err
	}
	regOf := map[aggregate.FormKey]string{}
	for _, r := range regs.Registrations {
		if !byPeriod(r.DataSet, r.Period) {
			continue
		}
		k := aggregate.FormKey{DataSet: r.DataSet, Period: r.Period, OrgUnit: facility, AOC: r.AttributeOptionCombo}
		regOf[k] = fmt.Sprintf("%v|%s|%s", r.Completed, r.StoredBy, r.Date)
	}
	forms, err := h.Store.AggForms()
	if err != nil {
		return err
	}
	stored := map[string]*store.AggForm{}
	for _, f := range forms {
		stored[f.Key] = f
	}
	for k, reg := range regOf {
		if f := stored[k.String()]; f == nil || f.Reg != reg {
			mark(k)
		}
	}
	for _, f := range forms {
		k := aggregate.FormKey{DataSet: f.DataSet, Period: f.Period, OrgUnit: f.OrgUnit, AOC: f.AOC}
		if windows[f.DataSet] == nil || !byPeriod(f.DataSet, f.Period) {
			continue
		}
		held[f.Key] = true
		if f.Stale || full {
			mark(k)
		}
	}
	if full { // each data set's current form, for its min-max values
		for id, win := range windows {
			ds := g.acc.DataSets[id]
			cur, _ := aggregate.Current(ds.PeriodType, now)
			if aoc, ok := g.acc.AOC(ds, "", nil); ok && byPeriod(id, cur.ID) && win != nil {
				mark(aggregate.FormKey{DataSet: id, Period: cur.ID, OrgUnit: facility, AOC: aoc})
			}
		}
	}
	for k := range changed {
		ds := g.acc.DataSets[k.DataSet]
		if ds == nil || !g.acc.MayUse(ds, k.AOC) {
			continue
		}
		held[k.String()] = true
		if fetched[k.String()] {
			continue
		}
		if err := w.fetchForm(ctx, g, ds, k, regOf[k]); err != nil {
			return err
		}
		fetched[k.String()] = true
	}
	h.Store.SetPrivate(stateKey+":cursor", newest)
	if full {
		h.Store.SetPrivate(stateKey+":full", store.Now())
	}
	for id := range windows {
		pulled[id] = true
		if h.Store.Private(core.AggPulledKey(id)) == "" {
			h.Store.SetPrivate(core.AggPulledKey(id), store.Now())
		}
	}
	return nil
}

// fetchForm stores central's dataEntry/dataValues answer for a form. A form central
// refuses to show (409: locked, or its "could not extract ResultSet" for expired
// periods nobody used) is stored as locked and empty.
func (w *Worker) fetchForm(ctx context.Context, g *aggGroup, ds *aggregate.DataSet, k aggregate.FormKey, reg string) error {
	h := w.H
	q := url.Values{"ds": {k.DataSet}, "pe": {k.Period}, "ou": {k.OrgUnit}}
	if combo, opts, ok := g.acc.Attribute(ds, k.AOC); ok && !g.acc.Combos[ds.CategoryCombo].Default {
		q.Set("cc", combo)
		q.Set("cp", strings.Join(opts, ";"))
	}
	resp, err := h.Central.Do(ctx, "GET", "/api/"+h.APIMinor()+"/dataEntry/dataValues?"+q.Encode(), nil, g.auth, nil)
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusConflict:
		f := aggregate.EmptyForm()
		f["lockStatus"] = aggregate.Locked
		b, _ = json.Marshal(f)
	case resp.StatusCode != http.StatusOK:
		return &central.HTTPError{Status: resp.StatusCode, Body: b}
	}
	return h.Store.PutAggForm(&store.AggForm{Key: k.String(), DataSet: k.DataSet, Period: k.Period, OrgUnit: k.OrgUnit,
		AOC: k.AOC, Body: b, Reg: reg, FetchedAt: store.Now()})
}

// pushAggregate sends one aggregate edit as its author. Central's refusal (a 4xx)
// makes it an issue that keeps the edit; the stored form shows the edit until the
// next pull fetches the form again.
func (w *Worker) pushAggregate(ctx context.Context, e *store.OutboxEntry) (pushResult, error) {
	h := w.H
	// The entry may have been replaced by a later edit of its cell since the push
	// read the outbox: send what it holds now.
	payload, err := h.Store.OutboxPayload(e.Seq)
	if err != nil {
		return pushResult{}, err
	}
	var ed aggregate.Edit
	if err := json.Unmarshal(payload, &ed); err != nil {
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, "corrupt payload: "+err.Error())
	}
	// Edits of one cell, and a form's completion after its edits, go in the order
	// they were made: wait while an earlier one waits (its author must log in).
	order := ed.Form.String()
	if ed.Op != aggregate.OpComplete {
		order += "|" + ed.DataElement + "|" + ed.COC
	}
	if h.Store.EarlierPendingFor(e.Seq, order) {
		return pushResult{}, nil
	}
	auth, err := h.UpstreamAuth(e.UserID)
	if err != nil {
		h.Store.SetNeedsLogin(e.UserID, true)
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{userBlocked: true}, nil
	}
	method, path, body := ed.Request()
	var hdr http.Header
	if body != nil {
		hdr = http.Header{"Content-Type": {"application/json"}}
	}
	resp, err := h.Central.Do(ctx, method, path, body, auth, hdr)
	if err != nil {
		h.Store.NoteAttempt(e.Seq, err.Error())
		return pushResult{stop: true}, err
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		h.Store.SetNeedsLogin(e.UserID, true)
		h.Store.NoteAttempt(e.Seq, "401 from central: needs online login")
		return pushResult{userBlocked: true}, nil
	case resp.StatusCode >= 500:
		h.Store.NoteAttempt(e.Seq, fmt.Sprintf("central returned %d", resp.StatusCode))
		return pushResult{stop: true}, nil
	}
	var msg struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	json.Unmarshal(rb, &msg)
	// Clearing a value central never had (entered and cleared offline) is done.
	gone := ed.Op == aggregate.OpDelete && resp.StatusCode == http.StatusConflict && strings.Contains(msg.Message, "does not exist")
	if resp.StatusCode/100 == 2 || gone {
		w.applyToStoredForm(&ed)
		_, err := h.Store.FinishOutboxIfUnchanged(e.Seq, store.OutboxSynced, "", payload)
		return pushResult{}, err
	}
	if msg.Message == "" {
		msg.Message = fmt.Sprintf("central returned %d", resp.StatusCode)
	}
	if cur, err := h.Store.OutboxPayload(e.Seq); err != nil || !bytes.Equal(cur, payload) {
		return pushResult{}, err // replaced by a later edit meanwhile: push that one
	}
	issue, _ := json.Marshal(map[string]any{"message": msg.Message,
		"errorReports": []map[string]string{{"errorCode": msg.ErrorCode, "message": msg.Message}}, "edit": ed})
	if err := h.Store.AddIssue(e.Seq, ed.CellKey(), store.OutboxAggregate, e.UserID, issue, ""); err != nil {
		slog.Error("add issue", "err", err)
	}
	slog.Warn("aggregate edit refused", "seq", e.Seq, "cell", ed.CellKey(), "code", msg.ErrorCode)
	h.Store.MarkAggFormStale(ed.Form.String())
	return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, refusalNote(msg.Message))
}

// refusalNote is the outbox entry's error for a refused aggregate edit, shown on the
// hub's admin page: central's message with quoted values hidden (it may quote what was
// entered), identifiers kept, as for tracker refusals (security review S7).
func refusalNote(msg string) string { return tracker.Redact(msg) }

// applyToStoredForm puts a pushed edit into the stored form, which no longer
// overlays it once the entry left the outbox, and marks the form for fetching.
// aggregateReplaced reports whether the cell a refused aggregate edit set has changed
// on central since the edit was made, by the hub's copy of the form: resending the
// edit would undo that change.
func (w *Worker) aggregateReplaced(is *store.Issue) bool {
	var issue struct {
		Edit aggregate.Edit `json:"edit"`
	}
	if json.Unmarshal(is.Error, &issue) != nil || issue.Edit.At == "" {
		return false
	}
	ed := issue.Edit
	f, err := w.H.Store.GetAggForm(ed.Form.String())
	if err != nil || f == nil {
		return false
	}
	var form aggregate.Form
	if json.Unmarshal(f.Body, &form) != nil {
		return false
	}
	at, _ := form.LastUpdated(ed.DataElement, ed.COC)
	return centralTime(at) > ed.At
}

func (w *Worker) applyToStoredForm(ed *aggregate.Edit) {
	h := w.H
	f, err := h.Store.GetAggForm(ed.Form.String())
	if err != nil || f == nil {
		return
	}
	var form aggregate.Form
	if json.Unmarshal(f.Body, &form) != nil {
		return
	}
	combo, opts := "", []string(nil)
	if ed.CC != "" {
		combo, opts = ed.CC, ed.CP
	}
	form.Apply(ed, combo, opts)
	f.Body, _ = json.Marshal(form)
	h.Store.PutAggForm(f)
	h.Store.MarkAggFormStale(f.Key)
}

// centralTime is a dataValueSets timestamp ("2026-10-15T08:15:46.399+0000") as the
// lastUpdated filter takes it: central's wall clock, without the offset.
func centralTime(s string) string {
	if len(s) > len(tracker.DHIS2Layout) {
		return s[:len(tracker.DHIS2Layout)]
	}
	return s
}
