package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
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
		if u.NeedsLogin || (!u.HasPAT && !u.HasPassword) || !h.Access(u.ID).InFacility(h.FacilityPath()) {
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
	for _, g := range groups {
		if err := w.pullAggGroup(ctx, g, now, ret, facility, held, fetched); err != nil {
			if central.IsNetwork(err) {
				return err
			}
			slog.Warn("aggregate pull", "err", err)
		}
	}
	forms, err := h.Store.AggForms()
	if err != nil {
		return err
	}
	for _, f := range forms {
		if !held[f.Key] && !h.Store.HasPendingOutboxFor(f.Key, 0) {
			h.Store.DeleteAggForm(f.Key)
		}
	}
	return nil
}

func (w *Worker) pullAggGroup(ctx context.Context, g *aggGroup, now time.Time, ret aggregate.Retention, facility string,
	held, fetched map[string]bool) error {
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
	stateKey := "agg_pull:" + g.key
	since := h.Store.Private(stateKey)
	full := since == "" || time.Since(store.ParseTime(h.Store.Private(stateKey+":full"))) > aggFullEvery
	cycleStart := time.Now()

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
	if !full {
		q.Set("lastUpdated", h.CentralTime(store.ParseTime(since)))
	}
	var vs struct {
		DataValues []struct {
			DataElement          string `json:"dataElement"`
			Period               string `json:"period"`
			AttributeOptionCombo string `json:"attributeOptionCombo"`
		} `json:"dataValues"`
	}
	if err := h.Central.JSON(ctx, "GET", "/api/dataValueSets?"+q.Encode(), nil, g.auth, &vs); err != nil {
		return err
	}
	for _, v := range vs.DataValues {
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
	h.Store.SetPrivate(stateKey, cycleStart.UTC().Format(time.RFC3339Nano))
	if full {
		h.Store.SetPrivate(stateKey+":full", store.Now())
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
	var ed aggregate.Edit
	if err := json.Unmarshal(e.Payload, &ed); err != nil {
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, "corrupt payload: "+err.Error())
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
	case resp.StatusCode/100 == 2:
		w.applyToStoredForm(&ed)
		return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxSynced, "")
	}
	var msg struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	json.Unmarshal(rb, &msg)
	if msg.Message == "" {
		msg.Message = fmt.Sprintf("central returned %d", resp.StatusCode)
	}
	issue, _ := json.Marshal(map[string]any{"message": msg.Message,
		"errorReports": []map[string]string{{"errorCode": msg.ErrorCode, "message": msg.Message}}, "edit": ed})
	if err := h.Store.AddIssue(e.Seq, ed.CellKey(), store.OutboxAggregate, e.UserID, issue); err != nil {
		slog.Error("add issue", "err", err)
	}
	slog.Warn("aggregate edit refused", "seq", e.Seq, "cell", ed.CellKey(), "code", msg.ErrorCode)
	h.Store.MarkAggFormStale(ed.Form.String())
	return pushResult{}, h.Store.FinishOutbox(e.Seq, store.OutboxRejected, msg.Message)
}

// applyToStoredForm puts a pushed edit into the stored form, which no longer
// overlays it once the entry left the outbox, and marks the form for fetching.
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
