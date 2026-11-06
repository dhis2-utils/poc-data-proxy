package trackerapi

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

// Answering staff from the hub while online. For the programs a country lists in
// facility-hub-config/localReads, the hub answers tracker reads from its own data
// although central is reachable, so screens do not wait on the uplink (stalls and
// lost packets included; docs/CONNECTIVITY.md). Saves still go to central, and the
// hub learns from central's answer (learn), so the next screen shows them. Changes
// made elsewhere reach staff at the hub's next pull.
//
// A read is answered here only when its program is listed, and the hub has finished a
// full pull of that program for the user:
//   - a list names the program (program=);
//   - a single object: an enrollment's or event's own program; a person's every
//     program the hub holds them in (or program= when given);
//   - relationships: the person, enrollment or event they are asked for.
//
// Searches over the user's whole scope (orgUnitMode ACCESSIBLE or CAPTURE) stay with
// central unless that scope is this facility alone. Whatever the hub cannot answer
// (not held, owned elsewhere, a filter it leaves to central) goes on to central.

// localWhileOnline reports whether this read is one the hub answers from its data.
func (a *API) localWhileOnline(sess *core.Session, r *http.Request) bool {
	if r.Method != http.MethodGet || sess == nil || sess.UserID == 0 {
		return false
	}
	set := a.H.LocalReads()
	if len(set.Programs) == 0 {
		return false
	}
	// Listed, and the hub has finished a full pull of it for this user: until then its
	// copy may lack what central would show them (a first pull still running, a
	// program added later).
	listed := func(p string) bool {
		if !set.Has(p) {
			return false
		}
		_, lastFull := a.H.Store.PullState(sess.UserID, p)
		return lastFull != ""
	}
	coll, uid := split(r.URL.Path)
	if collections[coll] == "" {
		return false
	}
	q := r.URL.Query()
	switch strings.ToUpper(q.Get("orgUnitMode") + q.Get("ouMode")) {
	case "ACCESSIBLE", "CAPTURE":
		if !a.H.Access(sess.UserID).FacilityOnly(a.H.Facility()) {
			return false
		}
	case "ALL":
		return false
	}
	if p := q.Get("program"); p != "" {
		return listed(p)
	}
	if uid == "" && coll == "relationships" {
		uid = q.Get("trackedEntity") + q.Get("enrollment") + q.Get("event")
	}
	if uid == "" {
		return false // a list without a program
	}
	return a.programsLocal(uid, listed)
}

// programsLocal: the object's programs are all listed (a person's, every enrollment
// the hub holds of them; at least one).
func (a *API) programsLocal(uid string, listed func(string) bool) bool {
	o := a.H.Index.Get(uid)
	if o == nil {
		return false
	}
	if o.Kind != store.KindTrackedEntity {
		return listed(o.Program)
	}
	enrs := a.H.Index.EnrollmentsOf(uid)
	for _, e := range enrs {
		if !listed(e.Program) {
			return false
		}
	}
	return len(enrs) > 0
}

// serveLocalFirst answers a read from the hub's data; false, with nothing written,
// when the hub's answer is not one it may give for central (anything but 200 or 404),
// so the caller asks central.
func (a *API) serveLocalFirst(w http.ResponseWriter, r *http.Request, sess *core.Session) bool {
	buf := &bufferedResponse{header: http.Header{}, status: 200}
	a.serveLocal(buf, r, sess, nil)
	if buf.status != http.StatusOK && buf.status != http.StatusNotFound {
		return false
	}
	for k, v := range buf.header {
		w.Header()[k] = v
	}
	w.Header().Set("X-Hub-Mode", "local")
	w.WriteHeader(buf.status)
	w.Write(buf.body.Bytes())
	return true
}

type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header         { return b.header }
func (b *bufferedResponse) WriteHeader(s int)           { b.status = s }
func (b *bufferedResponse) Write(p []byte) (int, error) { return b.body.Write(p) }
