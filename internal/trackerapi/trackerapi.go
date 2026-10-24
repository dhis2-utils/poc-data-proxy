// Package trackerapi serves /api/tracker/* and generated attribute values (spec 10).
// Online it passes requests through to central with the user's PAT and updates the
// store from the answers; offline it answers from the store.
package trackerapi

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/proxy"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

type API struct {
	H     *core.Hub
	Auth  *auth.Auth
	Proxy *proxy.Proxy

	jobsMu sync.Mutex
	jobs   map[string]*job // offline async imports, kept in memory
	// onlineJobs holds payloads of async imports passed to central, until the client
	// fetches the job report; the hub then stores what central persisted.
	onlineJobs map[string]onlineJob
}

type onlineJob struct {
	userID  int64
	payload []byte
	at      time.Time
}

type job struct {
	userID int64
	status int
	report any
	stats  tracker.Stats
	state  string
	at     string
	made   time.Time
}

// jobTTL is how long an offline job report is kept for the client to fetch. Reports
// can quote submitted values, so they do not stay in memory until a restart.
const jobTTL = time.Hour

var collections = map[string]string{"trackedEntities": store.KindTrackedEntity, "enrollments": store.KindEnrollment,
	"events": store.KindEvent, "relationships": store.KindRelationship}

// ServeHTTP handles /api/tracker and /api/tracker/*.
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sess, f := a.Auth.Identify(r)
	if f != nil {
		auth.WriteFailure(w, f)
		return
	}
	rec := &recorder{ResponseWriter: w, status: 200}
	served := "central"
	defer func() { a.H.ReqLog.Record(r, core.ClientKind(r), served, rec.status) }()
	w = rec

	if sess == nil {
		if a.H.Online() {
			a.Proxy.ServeHTTP(w, r) // central answers 302/401
			return
		}
		a.H.WebError(w, 401, "Unauthorized", "")
		return
	}
	body, err := core.ReadBody(r, core.MaxBody)
	if err != nil {
		a.H.WebError(w, http.StatusRequestEntityTooLarge, "request body too large or unreadable", "")
		return
	}

	// Online writes go to central even during an upgrade: central holds them, and
	// what the hub learns from them comes back with the next pull. After an outage the
	// hub keeps answering until its outbox is pushed (core.PassThrough).
	if a.H.PassThrough() {
		res, err := a.Proxy.Forward(r.Context(), r, body, a.Proxy.Upstream(sess))
		if err == nil {
			a.learn(sess, r, body, res)
			proxy.Write(w, a.Proxy.Localize(r, res))
			return
		}
		if !central.IsNetwork(err) {
			a.H.WebError(w, 502, err.Error(), "")
			return
		}
		slog.Info("central unreachable, serving tracker request locally", "path", r.URL.Path)
	}
	served = "hub"
	core.MarkOffline(w)
	a.serveLocal(w, r, sess, body)
}

// learn updates the store from a successful online answer (spec 10, online behaviour).
func (a *API) learn(sess *core.Session, r *http.Request, body []byte, res *proxy.Result) {
	if sess.UserID == 0 || !a.H.Store.Unlocked() || a.H.API() == nil {
		return
	}
	acc := a.H.Access(sess.UserID)
	if !acc.InFacility(a.H.FacilityPath()) {
		return
	}
	switch {
	case r.Method == http.MethodPut && res.Status == http.StatusOK && strings.HasSuffix(r.URL.Path, "/tracker/ownership/transfer"):
		// A transfer out through the hub takes effect offline at once.
		q := r.URL.Query()
		ou := q.Get("ou") // 2.42's name, if the client used it
		if api := a.H.API(); api != nil {
			ou = cmp.Or(q.Get(api.TransferOrgUnit()), ou)
		}
		if err := a.H.ApplyTransfer(q.Get("trackedEntity"), q.Get("program"), ou); err != nil {
			slog.Warn("apply ownership transfer", "err", err)
		}
	case r.Method == http.MethodGet && res.Status == http.StatusOK && !strings.HasPrefix(r.URL.Path, "/api/tracker/jobs/"):
		var payload tracker.M
		if json.Unmarshal(res.Body, &payload) != nil {
			return
		}
		coll, uid := split(r.URL.Path)
		if uid != "" && collections[coll] != "" {
			payload = tracker.M{coll: []any{payload}}
		}
		if coll == "relationships" && uid == "" {
			payload = tracker.M{"relationships": payload["relationships"]}
		}
		if _, err := a.H.UpsertCentral(sess.UserID, payload, false); err != nil {
			slog.Warn("store online read", "err", err)
		}
	case r.Method == http.MethodPost && strings.TrimSuffix(r.URL.Path, "/") == "/api/tracker" &&
		r.URL.Query().Get("async") != "false" && res.Status == http.StatusOK:
		var acc struct {
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if json.Unmarshal(res.Body, &acc) == nil && acc.Response.ID != "" {
			a.jobsMu.Lock()
			if a.onlineJobs == nil {
				a.onlineJobs = map[string]onlineJob{}
			}
			for id, j := range a.onlineJobs { // forget reports nobody fetched
				if time.Since(j.at) > time.Hour {
					delete(a.onlineJobs, id)
				}
			}
			a.onlineJobs[acc.Response.ID] = onlineJob{userID: sess.UserID, payload: body, at: time.Now()}
			a.jobsMu.Unlock()
		}
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/tracker/jobs/") &&
		strings.HasSuffix(r.URL.Path, "/report") && res.Status == http.StatusOK:
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/tracker/jobs/"), "/")[0]
		a.jobsMu.Lock()
		j, ok := a.onlineJobs[id]
		delete(a.onlineJobs, id)
		a.jobsMu.Unlock()
		if !ok || j.userID != sess.UserID {
			return
		}
		a.storePersisted(sess.UserID, j.payload, 200, res.Body)
	case r.Method == http.MethodPost && strings.TrimSuffix(r.URL.Path, "/") == "/api/tracker" && (res.Status == 200 || res.Status == 409):
		if r.URL.Query().Get("importStrategy") == "DELETE" {
			return
		}
		a.storePersisted(sess.UserID, body, res.Status, res.Body)
	}
}

// storePersisted stores the objects of a payload that central's import report says
// were persisted, so data entered just before an outage is visible offline.
func (a *API) storePersisted(userID int64, body []byte, status int, report []byte) {
	persisted, _, err := a.H.API().TrackerResult(status, report)
	if err != nil {
		return
	}
	var payload tracker.M
	if json.Unmarshal(body, &payload) != nil {
		return
	}
	uids, err := a.H.UpsertCentral(userID, filterPayload(payload, persisted), false)
	if err != nil {
		slog.Warn("store online write", "err", err)
		return
	}
	a.H.ShareByRules(uids)
}

// filterPayload keeps only persisted objects (flattened).
func filterPayload(payload tracker.M, persisted map[string]bool) tracker.M {
	out := tracker.M{"trackedEntities": []any{}, "enrollments": []any{}, "events": []any{}, "relationships": []any{}}
	for _, it := range tracker.Flatten(payload) {
		if !persisted[it.UID] {
			continue
		}
		key := map[string]string{store.KindTrackedEntity: "trackedEntities", store.KindEnrollment: "enrollments",
			store.KindEvent: "events", store.KindRelationship: "relationships"}[it.Kind]
		out[key] = append(out[key].([]any), it.Data)
	}
	return out
}

// split turns /api/tracker/events/UID into ("events", "UID").
func split(p string) (coll, uid string) {
	rest := strings.Trim(strings.TrimPrefix(p, "/api/tracker"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) > 0 {
		coll = parts[0]
	}
	if len(parts) == 2 {
		uid = parts[1]
	}
	if len(parts) > 2 && coll != "jobs" {
		coll = "-" // sub-resources (files, ownership, ...) are not handled offline
	}
	return
}

// generatedReadable reports whether attr is a generated attribute in the facility's
// programs that the user can read through a tracked entity type or a program.
func (a *API) generatedReadable(userID int64, attr string) bool {
	c := a.H.Catalog()
	if !slices.ContainsFunc(c.GeneratedAttributes(), func(m access.AttrMeta) bool { return m.ID == attr }) {
		return false
	}
	acc := a.H.Access(userID)
	if c.ReadableAttribute(acc, "", attr) {
		return true
	}
	for tet := range c.TETAttributes {
		if c.ReadableAttribute(acc, tet, attr) {
			return true
		}
	}
	return false
}

// usable checks the hub-local preconditions for a user (spec 8.1).
func (a *API) usable(w http.ResponseWriter, sess *core.Session) bool {
	switch {
	case a.H.API() == nil:
		a.H.NeedsConnection(w, "hub needs upgrade for DHIS2 "+a.H.ServerVersion())
	case a.H.Retired():
		a.H.NeedsConnection(w, "this hub was replaced by another hub for the facility; use the new hub")
	case !a.H.Store.Unlocked():
		a.H.NeedsConnection(w, "first login must be online")
	case sess.UserID == 0:
		// Online login registers only facility staff and hub admins (REVIEW-2 F5).
		a.H.WebError(w, http.StatusForbidden, "needs connection: user is not assigned to the hub facility", "E1000")
	case a.H.Facility() == "":
		a.H.NeedsConnection(w, "hub is not set up")
	case !a.H.Access(sess.UserID).InFacility(a.H.FacilityPath()):
		a.H.WebError(w, http.StatusForbidden, "needs connection: user is not assigned to the hub facility", "E1000")
	default:
		return true
	}
	return false
}

func (a *API) serveLocal(w http.ResponseWriter, r *http.Request, sess *core.Session, body []byte) {
	if !a.usable(w, sess) {
		return
	}
	v := a.H.Viewer(sess.UserID)
	v.Ctx = r.Context()
	api := a.H.API()
	coll, uid := split(r.URL.Path)
	switch {
	case r.Method == http.MethodGet && collections[coll] != "" && uid == "":
		res, err := a.H.Index.Search(v, coll, r.URL.Query())
		if err != nil {
			a.writeErr(w, r, err)
			return
		}
		if m := strings.ToUpper(r.URL.Query().Get("orgUnitMode")); m == "ACCESSIBLE" || m == "CAPTURE" {
			// Online this covers the user's whole scope; offline only this facility.
			w.Header().Set("X-Hub-Scope", "facility")
		}
		core.WriteJSON(w, 200, api.ListResponse(res, a.selfURL(r)))
	case r.Method == http.MethodGet && collections[coll] != "":
		obj, err := a.H.Index.GetOne(v, coll, uid, r.URL.Query())
		if err != nil {
			a.writeErr(w, r, err)
			return
		}
		core.WriteJSON(w, 200, obj)
	case r.Method == http.MethodPost && coll == "":
		if _, err := a.H.UpstreamAuth(sess.UserID); err != nil {
			// Nothing to push it with later: no PAT, and no stored password.
			a.H.NeedsConnection(w, "the hub holds no token for this user; changes need a connection")
			return
		}
		if !a.H.BeginLocalWrite() {
			a.H.UpgradingError(w)
			return
		}
		defer a.H.EndLocalWrite()
		a.importLocal(w, r, sess, v, body)
	case r.Method == http.MethodGet && coll == "jobs":
		a.serveJob(w, r, sess)
	default:
		a.H.NeedsConnection(w, r.Method+" "+r.URL.Path+" is not available offline")
	}
}

func (a *API) writeErr(w http.ResponseWriter, r *http.Request, err *tracker.APIError) {
	if err.Param != "" {
		a.H.ReqLog.Unsupported(r.URL.Path, err.Param)
	}
	a.H.WebError(w, err.Status, err.Message, err.Code)
}

func (a *API) selfURL(r *http.Request) string { return proxy.Origin(r) + r.URL.RequestURI() }

func (a *API) importLocal(w http.ResponseWriter, r *http.Request, sess *core.Session, v *tracker.Viewer, body []byte) {
	q := r.URL.Query()
	// Central defaults to async=true. Offline the hub imports synchronously either way
	// and, for async, keeps the report for GET /api/tracker/jobs/{id}[/report].
	async := q.Get("async") != "false"
	strategy := q.Get("importStrategy")
	switch strategy {
	case "", "CREATE", "UPDATE", "CREATE_AND_UPDATE":
	default:
		a.H.NeedsConnection(w, "importStrategy "+strategy+" is not available offline")
		return
	}
	for k := range q {
		switch k {
		case "async", "importStrategy", "atomicMode", "reportMode", "validationMode", "skipRuleEngine",
			"skipSideEffects", "skipPatternValidation", "flushMode", "importMode", "idScheme":
		default:
			a.H.WebError(w, 400, "not supported offline: "+k, "")
			a.H.ReqLog.Unsupported(r.URL.Path, k)
			return
		}
	}
	if s := q.Get("idScheme"); s != "" && s != "UID" {
		a.H.WebError(w, 400, "not supported offline: idScheme="+s, "")
		return
	}
	var payload tracker.M
	if err := json.Unmarshal(body, &payload); err != nil {
		a.H.WebError(w, 400, "Invalid JSON payload: "+err.Error(), "")
		return
	}
	u, _ := a.H.Store.UserByID(sess.UserID)
	rt := a.H.Runtime()
	im := &tracker.Importer{Ix: a.H.Index, V: v, Username: u.Username, UserUID: u.UserUID, DefaultAOC: rt.DefaultAOC,
		Strategy: strategy, Now: a.H.CentralNow(), AtomicAll: q.Get("atomicMode") != "OBJECT"}
	out := im.Import(payload)
	// Flags that only switch off checks or side effects on central: the hub runs
	// neither offline, and central must skip them on push as the client asked
	// (eRegisters sends skipPatternValidation and skipSideEffects).
	keep := url.Values{}
	for _, k := range []string{"skipPatternValidation", "skipSideEffects", "skipRuleEngine"} {
		if q.Get(k) == "true" {
			keep.Set(k, "true")
		}
	}
	if _, err := a.H.PersistImport(sess.UserID, out, keep.Encode()); err != nil {
		slog.Error("persist offline import", "err", err)
		a.H.WebError(w, 500, "could not store import: "+err.Error(), "")
		return
	}
	status, report := a.H.API().ImportReport(out)
	if !async {
		core.WriteJSON(w, status, report)
		return
	}
	id := core.NewUID()
	total, _ := out.Stats()
	st := "OK"
	if status != 200 {
		st = "ERROR"
	}
	a.jobsMu.Lock()
	if a.jobs == nil {
		a.jobs = map[string]*job{}
	}
	for k, j := range a.jobs {
		if time.Since(j.made) > jobTTL {
			delete(a.jobs, k)
		}
	}
	a.jobs[id] = &job{userID: sess.UserID, status: status, report: report, stats: total, state: st,
		at: a.H.CentralTime(time.Now()), made: time.Now()}
	a.jobsMu.Unlock()
	core.WriteJSON(w, 200, a.H.API().AsyncAccepted(id, a.selfBase(r)+"/tracker/jobs/"+id))
}

// serveJob answers /api/tracker/jobs/{id} and /api/tracker/jobs/{id}/report for
// offline async imports.
func (a *API) serveJob(w http.ResponseWriter, r *http.Request, sess *core.Session) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/tracker/jobs"), "/"), "/")
	a.jobsMu.Lock()
	j := a.jobs[parts[0]]
	a.jobsMu.Unlock()
	if j == nil || j.userID != sess.UserID || time.Since(j.made) > jobTTL {
		a.H.NeedsConnection(w, "tracker job "+parts[0]+" is not known to the hub")
		return
	}
	if len(parts) == 2 && parts[1] == "report" {
		core.WriteJSON(w, 200, j.report)
		return
	}
	core.WriteJSON(w, 200, a.H.API().JobNotifications(parts[0], j.state, j.stats, j.at))
}

func (a *API) selfBase(r *http.Request) string { return proxy.Origin(r) }

// --- generated attribute values (spec 10.3) ---------------------------------------

// ServeGenerated handles /api/trackedEntityAttributes/{uid}/{generate|generateAndReserve|requestValues}.
func (a *API) ServeGenerated(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || r.Method != http.MethodGet {
		a.Proxy.ServeHTTP(w, r)
		return
	}
	attr, op := parts[2], parts[3]
	if a.H.Online() {
		a.Proxy.ServeHTTP(w, r)
		return
	}
	sess, f := a.Auth.Identify(r)
	if f != nil {
		auth.WriteFailure(w, f)
		return
	}
	defer a.H.ReqLog.Record(r, core.ClientKind(r), "hub-pool", 200)
	core.MarkOffline(w)
	if sess == nil {
		a.H.WebError(w, 401, "Unauthorized", "")
		return
	}
	if !a.usable(w, sess) {
		return
	}
	if !a.generatedReadable(sess.UserID, attr) {
		a.H.WebError(w, http.StatusForbidden, "attribute "+attr+" is not a generated attribute this user can read", "")
		return
	}
	n := 1
	if op != "generate" {
		if s := r.URL.Query().Get("numberToReserve"); s != "" {
			n, _ = strconv.Atoi(s)
		}
	}
	// The pool is shared by the facility: one request may take at most half of
	// it, so one device cannot leave the others without values until the next sync.
	n = max(1, min(n, a.H.Cfg.ReservedPoolSize/2))
	if !a.H.BeginLocalWrite() {
		a.H.UpgradingError(w) // values handed out now would be handed out again after the swap
		return
	}
	vals, err := a.H.Store.TakeReservedValues(attr, n, a.H.CentralTime(time.Now()))
	a.H.EndLocalWrite()
	if err != nil || len(vals) == 0 {
		a.H.NeedsConnection(w, "no reserved values left for attribute "+attr)
		return
	}
	slog.Info("handed out reserved values", "attribute", attr, "n", len(vals), "user", sess.Username)
	if op == "generate" {
		w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		w.Write(vals[0].Value)
		return
	}
	arr := make([]json.RawMessage, 0, len(vals))
	for _, v := range vals {
		arr = append(arr, v.Value)
	}
	core.WriteJSON(w, 200, arr)
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(s int) {
	r.status = s
	r.ResponseWriter.WriteHeader(s)
}
