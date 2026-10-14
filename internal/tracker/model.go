// Package tracker holds the hub's tracker data model: flattening nested payloads into
// stored objects, the index over the store (spec 9.3), visibility rules (spec 8.2), local
// queries (spec 10.1) and offline import validation (spec 10.2).
package tracker

import (
	"crypto/rand"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
)

// M is a decoded JSON object.
type M = map[string]any

// Obj is one tracker object in the index.
type Obj struct {
	UID, Kind, TE, Enrollment, Program, Stage, TET, OwnerOrgUnit string
	UpdatedAt                                                    string
	Origin, SyncState                                            string
	Data                                                         M      // payload without nested children
	Slot                                                         uint32 // in the visibility bitmaps (visibility.go)
	// slim: Data holds only what queries filter and sort on (slim.go); the full
	// payload is in the store (Index.Full).
	slim bool
	// raw: a search's row whose slim data is not decoded yet (Data is nil; withData).
	raw []byte
}

// Children keys that are stored as separate objects, never inside a payload.
var childKeys = []string{"enrollments", "events", "relationships"}

var uidRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]{10}$`)

func ValidUID(s string) bool { return uidRe.MatchString(s) }

func Str(m M, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func list(m M, k string) []M {
	arr, _ := m[k].([]any)
	var out []M
	for _, v := range arr {
		if o, ok := v.(M); ok {
			out = append(out, o)
		}
	}
	return out
}

// Ref is one side of a relationship.
type Ref struct{ Kind, UID string }

func RelRef(side M) Ref {
	for _, k := range []string{"trackedEntity", "enrollment", "event"} {
		switch v := side[k].(type) {
		case M:
			if u := Str(v, k); u != "" {
				return Ref{k, u}
			}
		case string:
			if v != "" {
				return Ref{k, v}
			}
		}
	}
	return Ref{}
}

// Item is one object of an import payload, with its parent references resolved from
// nesting where the client omitted them.
type Item struct {
	Kind string
	UID  string
	Data M
	// Parent references (from nesting or explicit fields).
	TE, Enrollment string
	From, To       Ref
}

// Flatten turns a nested or flat tracker payload into items in dependency order:
// tracked entities, enrollments, events, relationships.
func Flatten(payload M) []*Item {
	var tes, enrs, evs, rels []*Item
	var addRel func(r M)
	addRel = func(r M) {
		it := &Item{Kind: store.KindRelationship, UID: Str(r, "relationship"), Data: strip(r)}
		it.From, it.To = RelRef(asM(r["from"])), RelRef(asM(r["to"]))
		rels = append(rels, it)
	}
	addEvent := func(e M, te, enr string) {
		it := &Item{Kind: store.KindEvent, UID: Str(e, "event"), Data: strip(e)}
		it.Enrollment = firstNonEmpty(Str(e, "enrollment"), enr)
		it.TE = firstNonEmpty(Str(e, "trackedEntity"), te)
		if it.Enrollment != "" {
			it.Data["enrollment"] = it.Enrollment
		}
		evs = append(evs, it)
		for _, r := range list(e, "relationships") {
			addRel(r)
		}
	}
	addEnrollment := func(e M, te string) {
		it := &Item{Kind: store.KindEnrollment, UID: Str(e, "enrollment"), Data: strip(e)}
		it.TE = firstNonEmpty(Str(e, "trackedEntity"), te)
		if it.TE != "" {
			it.Data["trackedEntity"] = it.TE
		}
		enrs = append(enrs, it)
		for _, ev := range list(e, "events") {
			addEvent(ev, it.TE, it.UID)
		}
		for _, r := range list(e, "relationships") {
			addRel(r)
		}
	}
	for _, t := range list(payload, "trackedEntities") {
		it := &Item{Kind: store.KindTrackedEntity, UID: Str(t, "trackedEntity"), Data: strip(t)}
		tes = append(tes, it)
		for _, e := range list(t, "enrollments") {
			addEnrollment(e, it.UID)
		}
		for _, r := range list(t, "relationships") {
			addRel(r)
		}
	}
	for _, e := range list(payload, "enrollments") {
		addEnrollment(e, "")
	}
	for _, e := range list(payload, "events") {
		addEvent(e, "", "")
	}
	for _, r := range list(payload, "relationships") {
		addRel(r)
	}
	out := append(tes, enrs...)
	out = append(out, evs...)
	return append(out, rels...)
}

func asM(v any) M { m, _ := v.(M); return m }

func strip(m M) M {
	out := M{}
	for k, v := range m {
		out[k] = v
	}
	for _, k := range childKeys {
		delete(out, k)
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// Merge applies an incoming payload onto the stored one. Top-level keys are replaced;
// attributes, data values and notes are merged by their identifying key so a partial
// response (for example one program's attributes) does not drop the rest.
func Merge(old, incoming M) M {
	if old == nil {
		return incoming
	}
	out := M{}
	for k, v := range old {
		out[k] = v
	}
	for k, v := range incoming {
		switch k {
		case "attributes":
			out[k] = mergeList(old[k], v, "attribute")
		case "dataValues":
			out[k] = mergeList(old[k], v, "dataElement")
		case "notes":
			out[k] = mergeList(old[k], v, "note")
		default:
			out[k] = v
		}
	}
	return out
}

func mergeList(old, incoming any, key string) []any {
	oldArr, _ := old.([]any)
	newArr, _ := incoming.([]any)
	pos := map[string]int{}
	out := make([]any, 0, len(oldArr)+len(newArr))
	for _, v := range oldArr {
		if m, ok := v.(M); ok {
			pos[Str(m, key)] = len(out)
		}
		out = append(out, v)
	}
	for _, v := range newArr {
		m, ok := v.(M)
		if !ok {
			continue
		}
		if i, ok := pos[Str(m, key)]; ok && Str(m, key) != "" {
			out[i] = v
		} else {
			pos[Str(m, key)] = len(out)
			out = append(out, v)
		}
	}
	return out
}

// DHIS2Time formats t like DHIS2 does in tracker payloads.
func DHIS2Time(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000") }

// shallow copies the top level of a payload for rendering, which only replaces
// top-level fields: the payload belongs to this request, and nothing below the top
// level is changed. A deep copy (clone) of every rendered object was a large part of
// an Android download page's cost (docs/PERFORMANCE.md).
func shallow(m M) M {
	out := make(M, len(m)+4)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func clone(m M) M {
	b, _ := json.Marshal(m)
	var out M
	json.Unmarshal(b, &out)
	return out
}

var backticked = regexp.MustCompile("`([^`]*)`")

// Redact removes values from a DHIS2 error message, keeping UIDs. Central quotes the
// offending value in some messages (E1064 "Non-unique attribute value `...`"), and
// issue reports and the admin page must not carry patient values (spec 11.2, 12).
func Redact(msg string) string {
	return backticked.ReplaceAllStringFunc(msg, func(s string) string {
		inner := s[1 : len(s)-1]
		if ValidUID(inner) {
			return s
		}
		return "`***`"
	})
}

// OutgoingReports reduces error reports to their structured fields for anything that
// leaves the hub: central's messages are free text and can quote patient values in
// ways no pattern catches (review M6).
func OutgoingReports(reps []ErrorReport) []ErrorReport {
	out := make([]ErrorReport, len(reps))
	for i, r := range reps {
		out[i] = ErrorReport{ErrorCode: r.ErrorCode, TrackerType: r.TrackerType, UID: r.UID}
	}
	return out
}

// OutgoingMessage is an issue message safe to leave the hub: the error codes when
// central sent error reports, a message the hub wrote itself, or a fixed text.
func OutgoingMessage(msg string, reps []ErrorReport) string {
	if len(reps) > 0 {
		var codes []string
		for _, r := range reps {
			if r.ErrorCode != "" && !slices.Contains(codes, r.ErrorCode) {
				codes = append(codes, r.ErrorCode)
			}
		}
		if len(codes) > 0 {
			return "rejected by central: " + strings.Join(codes, ", ")
		}
	}
	for _, own := range []*regexp.Regexp{hubBlocked, hubNotPersisted, hubCentralStatus} {
		if own.MatchString(msg) {
			return msg
		}
	}
	return "rejected by central"
}

var (
	hubBlocked       = regexp.MustCompile(`^blocked by [A-Za-z][A-Za-z0-9]{10}$`)
	hubNotPersisted  = regexp.MustCompile(`^not persisted by central$`)
	hubCentralStatus = regexp.MustCompile(`^central returned [0-9]{3}( \(E[0-9]{4}\))?$`)
)

const uidChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// NewUID returns a random DHIS2-style UID (a letter followed by 10 alphanumerics).
func NewUID() string {
	out := make([]byte, 0, 11)
	b := make([]byte, 16)
	for len(out) < 11 {
		rand.Read(b)
		for _, c := range b {
			// Rejection sampling: 248 = 4*62 keeps every character equally likely.
			if c >= 248 || (len(out) == 0 && c%62 >= 52) || len(out) == 11 {
				continue
			}
			out = append(out, uidChars[c%62])
		}
	}
	return string(out)
}

// AssignUIDs gives every object in a nested or flat payload that has no UID a new one,
// as central does. Capture's registration payload has no enrollment UID.
func AssignUIDs(payload M) {
	var walk func(m M, key string)
	set := func(m M, key string) {
		if Str(m, key) == "" {
			m[key] = NewUID()
		}
	}
	walk = func(m M, key string) {
		set(m, key)
		for _, e := range list(m, "enrollments") {
			walk(e, "enrollment")
		}
		for _, e := range list(m, "events") {
			walk(e, "event")
		}
		for _, r := range list(m, "relationships") {
			set(r, "relationship")
		}
	}
	for coll, key := range map[string]string{"trackedEntities": "trackedEntity", "enrollments": "enrollment", "events": "event"} {
		for _, o := range list(payload, coll) {
			walk(o, key)
		}
	}
	for _, r := range list(payload, "relationships") {
		set(r, "relationship")
	}
}
