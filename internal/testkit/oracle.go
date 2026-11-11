package testkit

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The differential oracle (docs/TESTING.md): central, asked directly as the user,
// decides what the hub should answer offline for the same request. The hub holds only
// the facility's data, so central's answer is first narrowed to objects whose tracked
// entity the facility owns for the program.

// Verdicts, from worst to fine.
const (
	Leak    = "LEAK"    // the hub shows something central does not show this user
	Missing = "MISSING" // central shows a facility object the hub does not
	Differs = "DIFFERS" // same object, different content
	Lenient = "LENIENT" // central rejects the request as invalid (400/409), the hub answers it
	Refused = "REFUSED" // the hub answers "needs connection" or refuses the user: safe, a coverage gap
	Match   = "MATCH"
)

// Query is one request, sent to central and to the hub.
type Query struct {
	Kind    string // trackedEntities, enrollments, events
	Program string
	Path    string // path and query, without the base URL
	Single  bool   // a single-object read
	// Unfiltered is the same request without updatedAfter, set on an incremental
	// query (Android's sync). The hub may answer such a query with more than central:
	// what it wrote within a day of the watermark (W1). Judge it with JudgeSince.
	Unfiltered string
}

// Outcome is the verdict for one user and query.
type Outcome struct {
	User    string   `json:"user"`
	Path    string   `json:"path"`
	Verdict string   `json:"verdict"`
	Central int      `json:"central"`
	Hub     int      `json:"hub"`
	Objects []string `json:"objects,omitempty"` // the UIDs the verdict is about
	Detail  string   `json:"detail,omitempty"`
}

// Truth is what the facility owns, read from central as the superuser.
type Truth struct {
	Owned map[string]map[string]bool // program -> tracked entity UIDs owned by the facility
}

// ReadTruth asks central which tracked entities the facility owns, per program, and,
// under the key "tet:<uid>", which of a type are registered there (no program).
//
// For event programs, "owned" is the facility's events (keyed by event UID).
func ReadTruth(c *Central, facility string, programs, tets, eventPrograms []string) (*Truth, error) {
	t := &Truth{Owned: map[string]map[string]bool{}}
	for _, p := range eventPrograms {
		var res struct {
			Events []struct {
				Event string `json:"event"`
			} `json:"events"`
		}
		q := url.Values{"program": {p}, "orgUnit": {facility}, "paging": {"false"}, "fields": {"event"}}
		if err := c.JSON("GET", "/api/tracker/events?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		t.Owned[p] = map[string]bool{}
		for _, ev := range res.Events {
			t.Owned[p][ev.Event] = true
		}
	}
	scopes := append([]string{}, programs...)
	for _, tet := range tets {
		scopes = append(scopes, "tet:"+tet)
	}
	for _, p := range scopes {
		var res struct {
			TEs []struct {
				TrackedEntity string `json:"trackedEntity"`
			} `json:"trackedEntities"`
		}
		q := url.Values{"program": {p}, "orgUnits": {facility}, "paging": {"false"}, "fields": {"trackedEntity"}}
		if tet, ok := strings.CutPrefix(p, "tet:"); ok {
			q.Del("program")
			q.Set("trackedEntityType", tet)
		}
		if err := c.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &res); err != nil {
			return nil, err
		}
		t.Owned[p] = map[string]bool{}
		for _, te := range res.TEs {
			t.Owned[p][te.TrackedEntity] = true
		}
	}
	return t, nil
}

// Queries is the generator: for each program, the searches the Capture apps make and
// variations on them. samples gives a value of the program's search attribute and of
// its unique attribute found in the data, and a tracked entity to read singly.
func Queries(m *Manifest, p Program, sample map[string]string) []Query {
	te := "trackedEntity,orgUnit,attributes[attribute,value],enrollments[enrollment,program,status,orgUnit]"
	en := "enrollment,trackedEntity,program,status,orgUnit"
	ev := "event,trackedEntity,enrollment,programStage,orgUnit,status,dataValues[dataElement,value]"
	enc := func(kind string, v url.Values) string { return "/api/tracker/" + kind + "?" + v.Encode() }
	var qs []Query
	add := func(kind string, v url.Values) {
		v.Set("program", p.ID)
		if _, ok := v["paging"]; !ok {
			v.Set("paging", "false")
		}
		qs = append(qs, Query{Kind: kind, Program: p.ID, Path: enc(kind, v)})
	}
	add("trackedEntities", url.Values{"orgUnits": {m.Facility.ID}, "fields": {te}})
	add("trackedEntities", url.Values{"orgUnitMode": {"ACCESSIBLE"}, "fields": {te}})
	add("trackedEntities", url.Values{"orgUnitMode": {"CAPTURE"}, "fields": {te}})
	if p.SearchAttr != "" {
		// Ordered by the search attribute: persons sharing a first name tie on it, so
		// the UID sequences expose a different tie order.
		for _, ord := range []string{p.SearchAttr + ":asc", p.SearchAttr + ":desc"} {
			add("trackedEntities", url.Values{"orgUnits": {m.Facility.ID}, "fields": {te}, "order": {ord}})
		}
	}
	for _, ord := range []string{"createdAt:asc", "createdAt:desc"} {
		add("trackedEntities", url.Values{"orgUnits": {m.Facility.ID}, "fields": {te}, "order": {ord}})
	}
	if v := sample["search"]; v != "" {
		add("trackedEntities", url.Values{"orgUnitMode": {"ACCESSIBLE"}, "fields": {te},
			"filter": {p.SearchAttr + ":like:" + strings.ToLower(v[:min(3, len(v))])}})
		add("trackedEntities", url.Values{"orgUnits": {m.Facility.ID}, "fields": {te},
			"filter": {p.SearchAttr + ":eq:" + v}})
	}
	if v := sample["unique"]; v != "" {
		add("trackedEntities", url.Values{"orgUnitMode": {"ACCESSIBLE"}, "fields": {te}, "filter": {p.UniqueAttr + ":eq:" + v}})
	}
	// Filters and orders on attributes of other programs only (review R-03): a user who
	// cannot read that program must learn nothing of its values from this one's lists.
	// sample["only:<attribute>"] is a value of it found in the data.
	for _, q := range m.Programs {
		if q.ID == p.ID || q.TET != p.TET {
			continue
		}
		for _, a := range q.OnlyAttrs {
			if slices.Contains(p.OnlyAttrs, a) {
				continue
			}
			add("events", url.Values{"orgUnit": {m.Facility.ID}, "fields": {ev}, "filterAttributes": {a + ":!null"}})
			if v := sample["only:"+a]; v != "" {
				add("events", url.Values{"orgUnit": {m.Facility.ID}, "fields": {ev}, "filterAttributes": {a + ":sw:" + v[:1]}})
			}
			add("trackedEntities", url.Values{"orgUnits": {m.Facility.ID}, "fields": {te}, "order": {a + ":desc"}})
		}
	}
	add("enrollments", url.Values{"orgUnits": {m.Facility.ID}, "fields": {en}})
	add("events", url.Values{"orgUnit": {m.Facility.ID}, "fields": {ev}})
	for _, st := range p.Stages {
		add("events", url.Values{"orgUnit": {m.Facility.ID}, "programStage": {st}, "fields": {ev}})
	}
	// Without a program: by tracked entity type, as Android's and Capture's lookups do.
	scope := "tet:" + p.TET
	for _, v := range []url.Values{
		{"trackedEntityType": {p.TET}, "orgUnits": {m.Facility.ID}, "fields": {te}, "paging": {"false"}},
	} {
		qs = append(qs, Query{Kind: "trackedEntities", Program: scope, Path: enc("trackedEntities", v)})
	}
	if uid := sample["owned"]; uid != "" {
		qs = append(qs, Query{Kind: "trackedEntities", Program: scope, Single: true,
			Path: "/api/tracker/trackedEntities/" + uid + "?" + url.Values{"fields": {te}}.Encode()})
	}
	for _, key := range []string{"owned", "foreign"} {
		if uid := sample[key]; uid != "" {
			qs = append(qs, Query{Kind: "trackedEntities", Program: p.ID, Single: true,
				Path: "/api/tracker/trackedEntities/" + uid + "?" + url.Values{"program": {p.ID}, "fields": {te}}.Encode()})
		}
	}
	return qs
}

// EventQueries is what the Capture apps ask of an event program: the working list
// (newest first, paged), by stage, Android's download, and one event.
func EventQueries(m *Manifest, p Program, sampleEvent string) []Query {
	ev := "event,programStage,orgUnit,status,occurredAt,attributeOptionCombo,dataValues[dataElement,value]"
	enc := func(v url.Values) string { return "/api/tracker/events?" + v.Encode() }
	base := func() url.Values { return url.Values{"program": {p.ID}, "orgUnit": {m.Facility.ID}, "fields": {ev}} }
	var qs []Query
	add := func(v url.Values) { qs = append(qs, Query{Kind: "events", Program: p.ID, Path: enc(v)}) }
	v := base()
	v.Set("paging", "false")
	add(v)
	v = base()
	v.Set("order", "occurredAt:desc")
	v.Set("page", "1")
	v.Set("pageSize", "20")
	add(v)
	for _, ord := range []string{"scheduledAt:asc", "scheduledAt:desc", "createdAt:asc"} {
		v = base()
		v.Set("order", ord)
		v.Set("page", "1")
		v.Set("pageSize", "20")
		add(v)
	}
	for _, st := range p.Stages {
		v = base()
		v.Set("programStage", st)
		v.Set("paging", "false")
		add(v)
	}
	v = base()
	// Android's download parameters, compared over the whole download: Android reads
	// every page, and central's default order (newest first) is not the hub's, so a
	// single page would differ while the download does not.
	for k, x := range map[string]string{"orgUnitMode": "DESCENDANTS", "includeDeleted": "true", "paging": "true", "page": "1", "pageSize": "10000"} {
		v.Set(k, x)
	}
	add(v)
	if sampleEvent != "" {
		qs = append(qs, Query{Kind: "events", Program: p.ID, Single: true,
			Path: "/api/tracker/events/" + sampleEvent + "?" + url.Values{"fields": {ev}}.Encode()})
	}
	return qs
}

// SinceQueries is what Android's incremental sync asks of a tracker program: the
// people and the events changed after each watermark in cuts (central's own times).
func SinceQueries(m *Manifest, p Program, cuts []string) []Query {
	te := "trackedEntity,orgUnit,attributes[attribute,value],enrollments[enrollment,program,status,orgUnit]"
	ev := "event,trackedEntity,enrollment,programStage,orgUnit,status,dataValues[dataElement,value]"
	var qs []Query
	for _, cut := range cuts {
		for _, x := range []struct {
			kind string
			v    url.Values
		}{
			{"trackedEntities", url.Values{"orgUnits": {m.Facility.ID}, "fields": {te}}},
			{"events", url.Values{"orgUnit": {m.Facility.ID}, "fields": {ev}}},
		} {
			x.v.Set("program", p.ID)
			x.v.Set("paging", "false")
			qs = append(qs, since(x.kind, p.ID, x.v, cut))
		}
	}
	return qs
}

// EventSinceQueries is the same for an event program's events.
func EventSinceQueries(m *Manifest, p Program, cuts []string) []Query {
	ev := "event,programStage,orgUnit,status,occurredAt,attributeOptionCombo,dataValues[dataElement,value]"
	var qs []Query
	for _, cut := range cuts {
		v := url.Values{"program": {p.ID}, "orgUnit": {m.Facility.ID}, "fields": {ev}, "paging": {"false"}}
		qs = append(qs, since("events", p.ID, v, cut))
	}
	return qs
}

func since(kind, program string, v url.Values, cut string) Query {
	base := "/api/tracker/" + kind + "?"
	unfiltered := base + v.Encode()
	v.Set("updatedAfter", cut)
	return Query{Kind: kind, Program: program, Path: base + v.Encode(), Unfiltered: unfiltered}
}

// Judge compares central's and the hub's answers for one user and query.
func Judge(truth *Truth, q Query, user string, cStatus int, cBody []byte, hStatus int, hBody []byte) Outcome {
	return judge(truth, q, user, cStatus, cBody, hStatus, hBody, nil)
}

// JudgeSince judges an incremental query (Query.Unfiltered). visible is what central
// shows the same user for the unfiltered request. An object the hub returns that
// central's incremental answer leaves out is not a leak when the user sees it anyway:
// the hub errs towards re-sending (W1). Everything central returns the hub must return
// too, as for any list: a sync that misses a change loses it on the device.
func JudgeSince(truth *Truth, q Query, user string, cStatus int, cBody []byte, hStatus int, hBody []byte, visible map[string]bool) Outcome {
	return judge(truth, q, user, cStatus, cBody, hStatus, hBody, visible)
}

// VisibleIn returns the UIDs of a list answer, for JudgeSince.
func VisibleIn(q Query, body []byte) map[string]bool {
	its, err := items(q, body)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for uid := range its {
		out[uid] = true
	}
	return out
}

func judge(truth *Truth, q Query, user string, cStatus int, cBody []byte, hStatus int, hBody []byte, visible map[string]bool) Outcome {
	o := Outcome{User: user, Path: q.Path, Central: cStatus, Hub: hStatus}
	hItems, herr := items(q, hBody)
	cItems, cerr := items(q, cBody)
	switch {
	case hStatus == 503 || hStatus == 401 || hStatus == 403:
		// The hub refuses: safe. Where central shows this user facility data, it is a
		// coverage gap; the detail says why the hub refused.
		o.Verdict = Refused
		var msg struct {
			Message string `json:"message"`
		}
		json.Unmarshal(hBody, &msg)
		o.Detail = msg.Message
		if cStatus == 200 && cerr == nil {
			for uid, it := range cItems {
				if truth.Owned[q.Program][teOf(q.Kind, uid, it)] {
					o.Objects = append(o.Objects, uid)
				}
			}
			sort.Strings(o.Objects)
		}
		return o
	case hStatus != 200:
		o.Verdict, o.Detail = Differs, fmt.Sprintf("hub answered %d", hStatus)
		return o
	case herr != nil:
		o.Verdict, o.Detail = Differs, "hub body: "+herr.Error()
		return o
	case (cStatus == 400 || cStatus == 409) && len(hItems) > 0:
		// Not a leak: central did not refuse the user, it refused the request.
		o.Verdict, o.Objects = Lenient, keys(hItems)
		o.Detail = fmt.Sprintf("central answered %d: %.200s", cStatus, cBody)
		return o
	case cStatus != 200 || cerr != nil:
		if len(hItems) > 0 {
			o.Verdict, o.Objects = Leak, keys(hItems)
			o.Detail = fmt.Sprintf("central answered %d", cStatus)
			return o
		}
		o.Verdict = Match
		return o
	}
	owned := truth.Owned[q.Program]
	expected := map[string]map[string]any{}
	for uid, it := range cItems {
		if owned[teOf(q.Kind, uid, it)] {
			expected[uid] = truth.narrow(q.Kind, uid, it)
		}
	}
	var leaks, extra, missing, differs, margin []string
	for uid, it := range hItems {
		switch {
		case cItems[uid] == nil && visible[uid]:
			margin = append(margin, uid)
		case cItems[uid] == nil:
			leaks = append(leaks, uid)
		case expected[uid] == nil:
			extra = append(extra, uid) // central shows it, but the facility does not own it
		case canon(truth.narrow(q.Kind, uid, it)) != canon(expected[uid]):
			differs = append(differs, uid)
		}
	}
	for uid := range expected {
		if hItems[uid] == nil {
			missing = append(missing, uid)
		}
	}
	if field, value, ok := pageBoundary(q, cBody); ok {
		// A full page of an ordered list ends inside a group of objects with the same
		// order value, and central breaks such ties by creation time, the hub by UID: the
		// two pages may hold different members of the group. Only those are excused;
		// whether the hub shows the user any of them is judged by the unpaged lists.
		tie := func(it map[string]any) bool { return fmt.Sprint(it[field]) == value }
		leaks = slices.DeleteFunc(leaks, func(uid string) bool { return tie(hItems[uid]) })
		missing = slices.DeleteFunc(missing, func(uid string) bool { return tie(cItems[uid]) })
	}
	sort.Strings(leaks)
	sort.Strings(missing)
	sort.Strings(differs)
	switch {
	case len(leaks) > 0:
		o.Verdict, o.Objects = Leak, leaks
	case len(missing) > 0:
		o.Verdict, o.Objects = Missing, missing
	case len(differs) > 0:
		o.Verdict, o.Objects = Differs, differs
		o.Detail = "hub: " + canon(hItems[differs[0]]) + "\ncentral: " + canon(expected[differs[0]])
	default:
		o.Verdict = Match
	}
	if o.Verdict == Match {
		// Membership says nothing about order: when the request carries an order
		// parameter, the two item lists' UID sequences must match too (the full list
		// when unpaged, the page when paged). This is what catches a different tie
		// order, which the pageBoundary excuse above deliberately ignores.
		if detail, ok := orderMismatch(q, cBody, hBody, expected); ok {
			o.Verdict = Differs
			o.Detail = detail
		}
	}
	if len(margin) > 0 {
		sort.Strings(margin)
		o.Detail = strings.TrimSpace(o.Detail + fmt.Sprintf("\nhub also re-sends %d object(s) the user sees, unchanged since the watermark on central: %v", len(margin), margin))
	}
	if len(extra) > 0 {
		o.Detail = strings.TrimSpace(o.Detail + fmt.Sprintf("\nhub also has %d object(s) the facility does not own: %v", len(extra), extra))
	}
	return o
}

// orderMismatch compares the UID sequences of central's and the hub's item lists when
// the request carries an order parameter, and returns the detail of a mismatch with the
// first ~10 UIDs of each side. Central's sequence is narrowed to the objects the
// facility owns, as the membership verdict is.
func orderMismatch(q Query, cBody, hBody []byte, expected map[string]map[string]any) (string, bool) {
	u, err := url.Parse(q.Path)
	if q.Single || err != nil || u.Query().Get("order") == "" {
		return "", false
	}
	cSeq, err1 := uidSeq(q, cBody)
	hSeq, err2 := uidSeq(q, hBody)
	if err1 != nil || err2 != nil {
		return "", false
	}
	want := make([]string, 0, len(cSeq))
	for _, uid := range cSeq {
		if expected[uid] != nil {
			want = append(want, uid)
		}
	}
	if slices.Equal(want, hSeq) {
		return "", false
	}
	return fmt.Sprintf("order differs: hub %v, central %v",
		hSeq[:min(10, len(hSeq))], want[:min(10, len(want))]), true
}

// uidSeq decodes a list answer's UIDs in the order the server returned them.
func uidSeq(q Query, body []byte) ([]string, error) {
	key := map[string]string{"trackedEntities": "trackedEntity", "enrollments": "enrollment", "events": "event"}[q.Kind]
	if q.Single {
		var o map[string]any
		if err := json.Unmarshal(body, &o); err != nil {
			return nil, err
		}
		if uid, _ := o[key].(string); uid != "" {
			return []string{uid}, nil
		}
		return nil, nil
	}
	var res map[string]any
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	list, _ := res[q.Kind].([]any)
	if list == nil {
		list, _ = res["instances"].([]any)
	}
	var out []string
	for _, x := range list {
		if o, ok := x.(map[string]any); ok {
			if uid, _ := o[key].(string); uid != "" {
				out = append(out, uid)
			}
		}
	}
	return out, nil
}

// pageBoundary gives, for a paged list with an order whose page from central is full,
// the order field and its value on the last object of central's page.
func pageBoundary(q Query, cBody []byte) (field, value string, ok bool) {
	u, err := url.Parse(q.Path)
	if q.Single || err != nil {
		return "", "", false
	}
	v := u.Query()
	field, _, _ = strings.Cut(strings.Split(v.Get("order"), ",")[0], ":")
	size, err := strconv.Atoi(v.Get("pageSize"))
	if field == "" || err != nil || v.Get("paging") == "false" {
		return "", "", false
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(cBody, &res) != nil {
		return "", "", false
	}
	raw := res[q.Kind]
	if raw == nil {
		raw = res["instances"]
	}
	var list []map[string]any
	if json.Unmarshal(raw, &list) != nil || len(list) == 0 || len(list) < size {
		return "", "", false
	}
	last, ok := list[len(list)-1][field]
	if !ok {
		return "", "", false
	}
	return field, fmt.Sprint(last), true
}

// narrow drops from a tracked entity the enrollments of programs the facility does not
// own it for (the hub holds only the facility's data) and of programs outside the
// manifest. It is applied to both answers.
func (t *Truth) narrow(kind, uid string, o map[string]any) map[string]any {
	enrs, ok := o["enrollments"].([]any)
	if kind != "trackedEntities" || !ok {
		return o
	}
	out := map[string]any{}
	for k, v := range o {
		out[k] = v
	}
	kept := []any{}
	for _, e := range enrs {
		em, _ := e.(map[string]any)
		p, _ := em["program"].(string)
		// Only programs the oracle knows are judged: enrollments in others (for instance
		// programs a user reads only through data sharing, without their metadata) are
		// left out on both sides.
		if owned, known := t.Owned[p]; known && owned[uid] {
			kept = append(kept, e)
		}
	}
	out["enrollments"] = kept
	return out
}

// items decodes a list answer (or a single object) into UID -> object.
func items(q Query, body []byte) (map[string]map[string]any, error) {
	key := map[string]string{"trackedEntities": "trackedEntity", "enrollments": "enrollment", "events": "event"}[q.Kind]
	out := map[string]map[string]any{}
	if q.Single {
		var o map[string]any
		if err := json.Unmarshal(body, &o); err != nil {
			return nil, err
		}
		if uid, _ := o[key].(string); uid != "" {
			out[uid] = o
		}
		return out, nil
	}
	var res map[string]any
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	list, _ := res[q.Kind].([]any)
	if list == nil {
		list, _ = res["instances"].([]any)
	}
	for _, x := range list {
		if o, ok := x.(map[string]any); ok {
			if uid, _ := o[key].(string); uid != "" {
				out[uid] = o
			}
		}
	}
	return out, nil
}

func teOf(kind, uid string, o map[string]any) string {
	if kind == "trackedEntities" {
		return uid
	}
	s, _ := o["trackedEntity"].(string)
	if s == "" && kind == "events" { // an event program's event: owned as itself
		return uid
	}
	return s
}

// canon renders an object with its lists sorted, so field and list order do not count.
func canon(o map[string]any) string {
	var norm func(v any) any
	norm = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, vv := range x {
				out[k] = norm(vv)
			}
			return out
		case []any:
			var parts []string
			for _, vv := range x {
				b, _ := json.Marshal(norm(vv))
				parts = append(parts, string(b))
			}
			sort.Strings(parts)
			return parts
		}
		return v
	}
	b, _ := json.Marshal(norm(o))
	return string(b)
}

func keys(m map[string]map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
