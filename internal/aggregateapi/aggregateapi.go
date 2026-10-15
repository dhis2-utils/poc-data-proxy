// Package aggregateapi serves the Aggregate Data Entry app's data endpoints: forms
// (dataEntry/dataValues), cell edits (dataValues), completion
// (dataEntry/dataSetCompletion), and refuses file values and min-max edits offline.
// Online with an empty outbox it passes them to central; otherwise it answers from
// the store and queues edits as their authors (core.PassThrough).
package aggregateapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/proxy"
)

type API struct {
	H     *core.Hub
	Auth  *auth.Auth
	Proxy *proxy.Proxy
}

// IsAggregatePath reports whether p (unversioned) is one of the endpoints served here.
func IsAggregatePath(p string) bool {
	switch p {
	case "/api/dataEntry/dataValues", "/api/dataValues", "/api/dataValues/file", "/api/dataEntry/dataSetCompletion",
		"/api/dataEntry/minMaxValues":
		return true
	}
	return false
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sess, f := a.Auth.Identify(r)
	if f != nil {
		auth.WriteFailure(w, f)
		return
	}
	body, err := core.ReadBody(r, core.MaxBody)
	if err != nil {
		a.H.WebError(w, http.StatusRequestEntityTooLarge, "request body too large or unreadable", "")
		return
	}
	if sess == nil || a.H.PassThrough() {
		var up central.Auth
		if sess != nil {
			up = a.Proxy.Upstream(sess)
		}
		res, err := a.Proxy.Forward(r.Context(), r, body, up)
		if err == nil {
			proxy.Write(w, a.Proxy.Localize(r, res))
			return
		}
		if !central.IsNetwork(err) {
			a.H.WebError(w, http.StatusBadGateway, err.Error(), "")
			return
		}
		if sess == nil {
			a.H.WebError(w, http.StatusUnauthorized, "Unauthorized", "")
			return
		}
	}
	core.MarkOffline(w)
	acc := a.H.AggAccess(sess.UserID)
	if sess.UserID == 0 || acc == nil || !a.H.Access(sess.UserID).InFacility(a.H.FacilityPath()) {
		a.H.WebError(w, http.StatusConflict, "needs connection: this user has no aggregate data at this hub", "")
		return
	}
	p := r.URL.Path
	switch {
	case p == "/api/dataEntry/dataValues" && r.Method == http.MethodGet:
		a.form(w, r.URL.Query(), acc)
	case p == "/api/dataValues" && (r.Method == http.MethodPost || r.Method == http.MethodDelete):
		a.edit(w, r, body, sess, acc)
	case p == "/api/dataEntry/dataSetCompletion" && r.Method == http.MethodPost:
		a.complete(w, body, sess, acc)
	default:
		// File values, min-max edits: refused with a message the app shows. Never a
		// 503: the app would queue the request in the browser.
		a.H.WebError(w, http.StatusConflict, "needs connection: "+r.Method+" "+p+" is not available offline", "")
	}
}

// formOf finds the data set, window period and attribute option combination a
// request is about, or writes central's refusal.
func (a *API) formOf(w http.ResponseWriter, acc *aggregate.Access, ds, pe, ou, cc string, cp []string) (
	*aggregate.DataSet, aggregate.FormKey, aggregate.Period, bool, bool) {
	d := acc.DataSets[ds]
	if d == nil || ou != a.H.Facility() || !d.Assigned(ou) {
		a.H.WebError(w, http.StatusConflict, "Data set not found or not accessible: `"+ds+"`", "E1100")
		return nil, aggregate.FormKey{}, aggregate.Period{}, false, false
	}
	aoc, ok := acc.AOC(d, cc, cp)
	if !ok || !acc.MayUse(d, aoc) {
		a.H.WebError(w, http.StatusConflict, "User does not have write access for attribute option combo", "E2010")
		return nil, aggregate.FormKey{}, aggregate.Period{}, false, false
	}
	k := aggregate.FormKey{DataSet: ds, Period: pe, OrgUnit: ou, AOC: aoc}
	per, inWindow := aggregate.InWindow(d, a.H.CentralNow(), a.H.AggRetention().For(ds), pe)
	return d, k, per, inWindow, true
}

func splitCP(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ";")
}

// current is the form as the user sees it: central's answer (or an empty form), with
// every pending edit on top, and the lock for this user now.
func (a *API) current(acc *aggregate.Access, d *aggregate.DataSet, k aggregate.FormKey, per aggregate.Period) (aggregate.Form, error) {
	form := aggregate.EmptyForm()
	centralLock := ""
	if f, err := a.H.Store.GetAggForm(k.String()); err != nil {
		return nil, err
	} else if f != nil {
		if err := json.Unmarshal(f.Body, &form); err != nil {
			return nil, err
		}
		centralLock, _ = form["lockStatus"].(string)
	} else if any, err := a.H.Store.AnyAggForm(k.DataSet); err == nil && any != nil {
		var other aggregate.Form
		if json.Unmarshal(any.Body, &other) == nil {
			form["minMaxValues"] = other["minMaxValues"]
		}
	}
	combo, opts, _ := acc.Attribute(d, k.AOC)
	pending, err := a.H.Store.PendingAggregate(k.String())
	if err != nil {
		return nil, err
	}
	for _, e := range pending {
		var ed aggregate.Edit
		if json.Unmarshal(e.Payload, &ed) == nil {
			form.Apply(&ed, combo, opts)
		}
	}
	form["lockStatus"] = aggregate.LockStatus(d, per, a.H.CentralNow(), acc.Bypass, centralLock)
	return form, nil
}

func (a *API) form(w http.ResponseWriter, q url.Values, acc *aggregate.Access) {
	d, k, per, inWindow, ok := a.formOf(w, acc, q.Get("ds"), q.Get("pe"), q.Get("ou"), q.Get("cc"), splitCP(q.Get("cp")))
	if !ok {
		return
	}
	if !inWindow {
		f := aggregate.EmptyForm()
		f["lockStatus"] = aggregate.Locked
		core.WriteJSON(w, http.StatusOK, f)
		return
	}
	form, err := a.current(acc, d, k, per)
	if err != nil {
		a.H.WebError(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	core.WriteJSON(w, http.StatusOK, form)
}

// params merges the query with a multipart or urlencoded body, as central reads them.
func params(r *http.Request, body []byte) url.Values {
	q := r.URL.Query()
	ct, ps, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ct {
	case "multipart/form-data":
		if f, err := multipart.NewReader(bytes.NewReader(body), ps["boundary"]).ReadForm(1 << 20); err == nil {
			for k, v := range f.Value {
				q[k] = v
			}
		}
	case "application/x-www-form-urlencoded":
		if f, err := url.ParseQuery(string(body)); err == nil {
			for k, v := range f {
				q[k] = v
			}
		}
	}
	return q
}

func (a *API) edit(w http.ResponseWriter, r *http.Request, body []byte, sess *core.Session, acc *aggregate.Access) {
	q := params(r, body)
	d, k, per, inWindow, ok := a.formOf(w, acc, q.Get("ds"), q.Get("pe"), q.Get("ou"), q.Get("cc"), splitCP(q.Get("cp")))
	if !ok {
		return
	}
	de, coc := q.Get("de"), q.Get("co")
	if _, in := d.Elements[de]; !in {
		a.H.WebError(w, http.StatusConflict, "Data element not found or not accessible: `"+de+"`", "E1100")
		return
	}
	if !inWindow || a.lock(acc, d, k, per) != aggregate.Open {
		a.H.WebError(w, http.StatusConflict, "Data set is locked", "E2017")
		return
	}
	ed := &aggregate.Edit{Op: aggregate.OpValue, Form: k, DataElement: de, COC: coc, By: sess.Username,
		At: a.H.CentralTime(a.H.CentralNow())}
	if q.Get("cc") != "" {
		ed.CC, ed.CP = q.Get("cc"), splitCP(q.Get("cp"))
	}
	switch {
	case r.Method == http.MethodDelete:
		ed.Op = aggregate.OpDelete
	case q.Has("value"):
		v := q.Get("value")
		if e := acc.Elements[de]; e != nil && v != "" && !aggregate.ValidValue(e.ValueType, v) {
			a.H.WebError(w, http.StatusConflict, "Data value must match data element value type: `"+e.ValueType+"`", "E2030")
			return
		}
		ed.Value = &v
	case q.Has("comment"):
		c := q.Get("comment")
		ed.Comment = &c
	case q.Has("followUp"):
		f := q.Get("followUp") == "true"
		ed.FollowUp = &f
	default:
		a.H.WebError(w, http.StatusConflict, "value, comment or followUp is required", "")
		return
	}
	if !a.queue(w, sess, ed, false) {
		return
	}
	if r.Method == http.MethodDelete {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// lock is the form's lock for the user now (central's answer, if fetched, then
// expiry and input periods).
func (a *API) lock(acc *aggregate.Access, d *aggregate.DataSet, k aggregate.FormKey, per aggregate.Period) string {
	centralLock := ""
	if f, _ := a.H.Store.GetAggForm(k.String()); f != nil {
		var form aggregate.Form
		if json.Unmarshal(f.Body, &form) == nil {
			centralLock, _ = form["lockStatus"].(string)
		}
	}
	return aggregate.LockStatus(d, per, a.H.CentralNow(), acc.Bypass, centralLock)
}

// queue appends the edit to the outbox as the session's user.
func (a *API) queue(w http.ResponseWriter, sess *core.Session, ed *aggregate.Edit, completion bool) bool {
	if !a.H.BeginLocalWrite() {
		a.H.UpgradingError(w)
		return false
	}
	defer a.H.EndLocalWrite()
	b, _ := json.Marshal(ed)
	err := a.H.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := a.H.Store.AppendAggregate(tx, sess.UserID, b, ed.CellKey(), ed.Form.String(), completion)
		return err
	})
	if err != nil {
		slog.Error("queue aggregate edit", "err", err)
		a.H.WebError(w, http.StatusInternalServerError, "could not save the edit", "")
		return false
	}
	return true
}
func (a *API) complete(w http.ResponseWriter, body []byte, sess *core.Session, acc *aggregate.Access) {
	a.H.WebError(w, http.StatusConflict, "Completing needs a connection", "")
}
