package testkit

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// The ledger: every save the hub confirmed to a client, checked after a scenario
// against central and the hub's refused changes (docs/ADVERSARIAL-TESTING.md, I1, I2).

// Save is one tracker object the hub confirmed to a client.
type Save struct {
	User   string // username of the author
	Kind   string // trackedEntities, enrollments or events
	UID    string
	Parent string // the event's enrollment
	Stage  string // the event's program stage
	// Program a tracked entity was saved enrolled in: central lists the program's
	// attributes only when read with it.
	Program string
	Status  string            // the event's or enrollment's status
	Values  map[string]string // attributes (tracked entities) or data values (events)
	Online  bool
}

// Outcomes of a save, per the plan.
const (
	OnCentral        = "on central as saved"
	ChangedOnCentral = "on central but different"
	RefusedKept      = "refused and kept on the hub"
	Waiting          = "waiting on the hub" // not sent yet, or sent and to be sent again
	Lost             = "missing"
	Duplicated       = "duplicated"
	// Closed: deleted on central, and the hub holds a tombstone with no open issue; a
	// change central refused as "already deleted" closes so (sync.closeMoot).
	Closed = "deleted on central, closed on the hub"
	// SavedAgain is the detail of a save the client saved again later.
	SavedAgain = "(saved again later)"
)

// Line is the check of one save.
type Line struct {
	Save
	Outcome string
	Detail  string // central's reason for a refusal, or what differs
}

func (l Line) String() string {
	mode := "offline"
	if l.Online {
		mode = "online"
	}
	return fmt.Sprintf("%s %s by %s (%s): %s %s", l.Kind, l.UID, l.User, mode, l.Outcome, l.Detail)
}

type Ledger struct {
	e     *Env
	Saves []Save
}

func (e *Env) Ledger() *Ledger { return &Ledger{e: e} }

// Confirmed records every object of a tracker payload the hub accepted from user
// (nested or flat, as clients send them).
func (l *Ledger) Confirmed(user string, payload map[string]any) {
	online := !l.e.Net.Down()
	var walk func(kind, parent string, list any)
	walk = func(kind, parent string, list any) {
		for _, x := range asAnyList(list) {
			o, _ := x.(map[string]any)
			s := Save{User: user, Kind: kind, Online: online, Values: map[string]string{}}
			switch kind {
			case "trackedEntities":
				s.UID, _ = o["trackedEntity"].(string)
				for _, a := range asAnyList(o["attributes"]) {
					am := a.(map[string]any)
					s.Values[am["attribute"].(string)] = fmt.Sprint(am["value"])
				}
				for _, en := range asAnyList(o["enrollments"]) {
					if s.Program == "" {
						s.Program, _ = en.(map[string]any)["program"].(string)
					}
				}
				walk("enrollments", s.UID, o["enrollments"])
			case "enrollments":
				s.UID, _ = o["enrollment"].(string)
				s.Status, _ = o["status"].(string)
				walk("events", s.UID, o["events"])
			case "events":
				s.UID, _ = o["event"].(string)
				s.Parent, _ = o["enrollment"].(string)
				s.Stage, _ = o["programStage"].(string)
				s.Status, _ = o["status"].(string)
				for _, d := range asAnyList(o["dataValues"]) {
					dm := d.(map[string]any)
					s.Values[dm["dataElement"].(string)] = fmt.Sprint(dm["value"])
				}
			}
			if s.Parent == "" && kind == "enrollments" {
				s.Parent = parent
			}
			l.Saves = append(l.Saves, s)
		}
	}
	for _, k := range []string{"trackedEntities", "enrollments", "events"} {
		walk(k, "", payload[k])
	}
}

// Check reads central (as the superuser) and the hub's refused changes, and judges
// every save. A refusal counts as kept only if the hub holds the object as rejected
// and lists it as an open issue, as the Facility Hub app shows it.
func (l *Ledger) Check() []Line {
	e := l.e
	e.T.Helper()
	open := map[string]string{}
	issues, err := e.App.Hub.Store.UnresolvedIssues(100000)
	if err != nil {
		e.T.Fatal(err)
	}
	for _, is := range issues {
		var stored struct {
			Message      string
			ErrorReports []struct{ ErrorCode string }
		}
		json.Unmarshal(is.Error, &stored)
		open[is.ObjectUID] = stored.Message
		for _, r := range stored.ErrorReports {
			open[is.ObjectUID] += " [" + r.ErrorCode + "]"
		}
		if !e.issueOnCentral(fmt.Sprintf("%s-%d", e.App.Hub.HubID(), is.ID)) {
			open[is.ObjectUID] += " (not in facility-hub-issues on central)"
		}
	}
	last := map[string]int{}
	for i, s := range l.Saves {
		last[s.UID] = i
	}
	var out []Line
	for i, s := range l.Saves {
		ln := Line{Save: s}
		obj := e.App.Hub.Index.Get(s.UID)
		if last[s.UID] != i {
			// An earlier version of an object saved again: what counts is that it is
			// there; its values are the later save's.
			ln.Detail = SavedAgain
			if l.closed(s, obj, open) {
				ln.Outcome = Closed
			} else if l.central(s) != nil {
				ln.Outcome = OnCentral
			} else if obj != nil && (obj.SyncState == store.SyncRejected || obj.SyncState == store.SyncSuperseded) && open[s.UID] != "" {
				ln.Outcome = RefusedKept
			} else if obj != nil && obj.SyncState != store.SyncSynced {
				ln.Outcome = Waiting
			} else {
				ln.Outcome = Lost
			}
		} else if obj != nil && (obj.SyncState == store.SyncRejected || obj.SyncState == store.SyncSuperseded) && open[s.UID] != "" {
			// Central may hold an earlier version: this save is the one refused.
			ln.Outcome, ln.Detail = RefusedKept, open[s.UID]
		} else if l.closed(s, obj, open) {
			ln.Outcome = Closed
		} else if o := l.central(s); o != nil {
			ln.Outcome, ln.Detail = OnCentral, l.differs(s, o)
			if ln.Detail != "" {
				ln.Outcome = ChangedOnCentral
			} else if d := l.duplicates(s); d != "" {
				ln.Outcome, ln.Detail = Duplicated, d
			}
		} else if obj != nil && obj.SyncState == store.SyncPending {
			ln.Outcome = Waiting
		} else {
			ln.Outcome = Lost
			if obj != nil {
				ln.Detail = "hub state " + obj.SyncState
			}
		}
		out = append(out, ln)
	}
	return out
}

// closed reports whether the hub holds a tombstone of the save, with no open issue,
// and central has the object deleted or not at all.
func (l *Ledger) closed(s Save, obj *tracker.Obj, open map[string]string) bool {
	if obj == nil || !obj.Deleted || obj.SyncState != store.SyncSynced || open[s.UID] != "" {
		return false
	}
	o := l.central(s)
	return o == nil || o["deleted"] == true
}

// central reads the saved object from central as the superuser; a tracked entity
// with the program it was saved in, so its program attributes are listed too, or
// without when central does not have it in that program (its enrollment refused).
func (l *Ledger) central(s Save) map[string]any {
	if s.Kind != "trackedEntities" || s.Program == "" || !l.e.Live() {
		return l.e.CentralObject(s.Kind, s.UID)
	}
	st, b, err := l.e.Central.Do("GET", "/api/tracker/trackedEntities/"+s.UID+"?"+
		url.Values{"program": {s.Program}, "fields": {"*"}}.Encode(), nil)
	if err != nil {
		l.e.T.Fatal(err)
	}
	if st != 200 {
		return l.e.CentralObject(s.Kind, s.UID)
	}
	var o map[string]any
	json.Unmarshal(b, &o)
	return o
}

// issueOnCentral reports whether central's facility-hub-issues holds key, where the
// Facility Hub app lists refused changes.
func (e *Env) issueOnCentral(key string) bool {
	if e.Fake != nil {
		return e.Fake.DataStoreValue("facility-hub-issues", key) != nil
	}
	st, _, err := e.Central.Do("GET", "/api/dataStore/facility-hub-issues/"+key, nil)
	return err == nil && st == 200
}

// differs says what of a save is not on central as saved: values and the author.
func (l *Ledger) differs(s Save, o map[string]any) string {
	var d []string
	got := map[string]string{}
	key, list := "dataElement", o["dataValues"]
	if s.Kind == "trackedEntities" {
		key, list = "attribute", o["attributes"]
	}
	for _, x := range asAnyList(list) {
		m := x.(map[string]any)
		got[fmt.Sprint(m[key])] = fmt.Sprint(m["value"])
	}
	if st, _ := o["status"].(string); s.Status != "" && st != s.Status {
		d = append(d, fmt.Sprintf("status %s, central %s", s.Status, st))
	}
	for k, v := range s.Values {
		if got[k] != v {
			d = append(d, fmt.Sprintf("%s=%q, central %q", k, v, got[k]))
		}
	}
	if l.e.Live() {
		by, _ := o["createdBy"].(map[string]any)
		if u, _ := by["username"].(string); u != s.User {
			d = append(d, fmt.Sprintf("createdBy %q", u))
		}
	}
	slices.Sort(d)
	return strings.Join(d, "; ")
}

// duplicates finds another event on central in the same enrollment and stage with
// the same values, not in the ledger. Live only: the fake does not list by enrollment.
func (l *Ledger) duplicates(s Save) string {
	if s.Kind != "events" || !l.e.Live() || len(s.Values) == 0 {
		return ""
	}
	var res struct {
		Events []struct {
			Event      string
			DataValues []struct{ DataElement, Value string }
		} `json:"events"`
	}
	q := url.Values{"enrollments": {s.Parent}, "programStage": {s.Stage}, "fields": {"event,dataValues"}, "paging": {"false"}}
	if err := l.e.Central.JSON("GET", "/api/tracker/events?"+q.Encode(), nil, &res); err != nil {
		return ""
	}
	var dups []string
	for _, ev := range res.Events {
		if ev.Event == s.UID || slices.ContainsFunc(l.Saves, func(x Save) bool { return x.UID == ev.Event }) {
			continue
		}
		same := true
		for _, dv := range ev.DataValues {
			if v, ok := s.Values[dv.DataElement]; !ok || v != dv.Value {
				same = false
			}
		}
		if same && len(ev.DataValues) == len(s.Values) {
			dups = append(dups, ev.Event)
		}
	}
	if len(dups) == 0 {
		return ""
	}
	return "also as " + strings.Join(dups, ", ")
}

// Report logs every line and returns the lines whose outcome is not in allowed: the
// ones that break I1 or I2 for this scenario.
func (l *Ledger) Report(allowed ...string) (bad []Line) {
	l.e.T.Helper()
	for _, ln := range l.Check() {
		l.e.T.Logf("ledger: %s", ln)
		if !slices.Contains(allowed, ln.Outcome) {
			bad = append(bad, ln)
		}
	}
	return bad
}
