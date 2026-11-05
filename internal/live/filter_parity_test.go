package live

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Filter operators the hub answers offline must mean what central's do: every
// operator of DHIS2's tracker API, on text, number and date attributes and on
// number and text data values, compared on probe records with chosen values (case,
// accents, numbers written as text, missing values, the separators ; and %). Capture
// web sends like, sw, ew and eq on text, a range (ge, le) on numbers and dates, and
// eq on option sets and unique attributes; custom apps may send any operator.
// Central is asked as the same user, scoped to the probe records.
func TestFilterOperatorParity(t *testing.T) {
	e := tk.Start(t)
	if !e.Live() {
		t.Skip("needs a live central (HUB_MANIFEST): central's operators are the reference")
	}
	m, p := e.M, slowProgram(e.M)
	if p.SearchAttr == "" || len(p.Stages) == 0 {
		t.Skip("the program has no text search attribute or no stage")
	}
	numAttr := probeAttribute(t, e, p, "HUB_PROBE_NUMBER", "Hub probe number", "NUMBER")
	dateAttr := probeAttribute(t, e, p, "HUB_PROBE_DATE", "Hub probe date", "DATE")
	intAttr := probeAttribute(t, e, p, "HUB_PROBE_INTEGER", "Hub probe integer", "INTEGER")
	colours := probeOptionSet(t, e)
	multiAttr := probeAttribute(t, e, p, "HUB_PROBE_MULTI", "Hub probe multi", "MULTI_TEXT", colours)
	stage, numDE, textDE := probeDataElements(t, e, p)
	multiDE := probeMultiDataElement(t, e, stage, colours)
	// Per probe, in order: an INTEGER attribute value, and a MULTI_TEXT value (option
	// codes Red, Blue, Green, abc) on the attribute and on the data element.
	ints := []string{"9", "10", "100", "", "-1", "", "0", "1000", "", "", "", "7", ""}
	multis := []string{"Red,Blue", "Blue", "Green", "Red", "abc", "", "Blue,Green", "", "Red", "", "", "Green", ""}

	type probe struct{ text, num, date, dvNum, dvText string }
	probes := []probe{
		{"Abc", "9", "2024-01-05", "9", "Abc"}, {"abc", "10", "2024-01-15", "10", "abc"},
		{"ABC def", "100", "2023-12-31", "100", "ABC def"}, {"xabcx", "2.5", "", "2.5", "xabcx"},
		{"Åsa", "-1", "2024-02-29", "-1", "Åsa"}, {"åsa", "", "", "", "åsa"}, {"a;b", "10.5", "2024-01-05", "10.5", "a;b"},
		{"10", "0", "", "0", "10"}, {"9", "", "", "", "9"}, {"a%b", "", "", "", "a%b"}, {"a_b", "", "", "", "a_b"},
		{"", "7", "", "7", ""}, {"Ab C", "1000", "2025-06-30", "1000", "Ab C"},
	}
	// Where the search attribute is mandatory (the synthetic profile's first name), the
	// probe without a value gets one; the other attributes still have probes without.
	var mand struct {
		PTEAs []struct {
			Mandatory bool                `json:"mandatory"`
			TEA       struct{ ID string } `json:"trackedEntityAttribute"`
		} `json:"programTrackedEntityAttributes"`
	}
	if err := e.Central.JSON("GET", "/api/programs/"+p.ID+"?fields=programTrackedEntityAttributes[mandatory,trackedEntityAttribute[id]]", nil, &mand); err != nil {
		t.Fatal(err)
	}
	for _, a := range mand.PTEAs {
		if a.TEA.ID == p.SearchAttr && a.Mandatory {
			for i := range probes {
				if probes[i].text == "" {
					probes[i].text = "Zz"
				}
			}
		}
	}
	var tes, evs, enrs []string
	byTE, byEv := map[string]string{}, map[string]string{}
	for i, pr := range probes {
		person := m.NewPerson(p, fmt.Sprintf("Probe%02d", i), stage)
		setAttr(person.Payload, p.SearchAttr, pr.text)
		setAttr(person.Payload, numAttr, pr.num)
		setAttr(person.Payload, dateAttr, pr.date)
		setAttr(person.Payload, intAttr, ints[i])
		setAttr(person.Payload, multiAttr, multis[i])
		ev := person.Payload["trackedEntities"].([]any)[0].(map[string]any)["enrollments"].([]any)[0].(map[string]any)["events"].([]any)[0].(map[string]any)
		var dvs []any
		for _, d := range ev["dataValues"].([]any) {
			if de := d.(map[string]any)["dataElement"]; de != numDE && de != textDE && de != multiDE {
				dvs = append(dvs, d)
			}
		}
		for de, v := range map[string]string{numDE: pr.dvNum, textDE: pr.dvText, multiDE: multis[i]} {
			if v != "" && de != "" {
				dvs = append(dvs, map[string]any{"dataElement": de, "value": v})
			}
		}
		ev["dataValues"] = dvs
		e.CentralComplete(p, person)
		if st, b, err := e.Central.Do("POST", tk.SyncImport, person.Payload); err != nil || st != 200 {
			t.Fatalf("probe %d: %d %v %.600s", i, st, err, b)
		}
		tes = append(tes, person.TE)
		evs = append(evs, person.Events[0])
		enrs = append(enrs, person.Enrollment)
		byTE[person.TE] = fmt.Sprintf("%q", pr.text)
		byEv[person.Events[0]] = fmt.Sprintf("%q/%q", pr.dvNum, pr.dvText)
	}
	e.SetupAs(tk.RoleNurse)
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()
	e.Offline()
	defer e.Online()
	central := e.Central.As(nurse.User, m.Password)

	// The tracker API's operators, and !eq and !in, which Android's SDK sends on events.
	ops := []string{"eq", "ieq", "ne", "neq", "nieq", "like", "ilike", "nlike", "nilike", "sw", "ew", "in", "gt", "ge", "lt", "le", "!eq", "!in"}
	operands := map[string][]string{
		"text":    {"abc", "ABC", "Abc", "åsa", "ÅSA", "a", "c", "10", "9", "a;b", "abc;10", "%", "_", "ab"},
		"number":  {"10", "10.0", "9.5", "100", "-1", "1e1", "abc"},
		"date":    {"2024-01-05", "2024-01-10", "2024-01-05T00:00:00", "2024", "abc"},
		"integer": {"10", "10.0", "1e1", "+10", "9", "-1", "abc"},
		"multi":   {"red", "BLUE", "red;blue", "re", "ue", "Red,Blue", "abc", "green"},
	}
	type check struct{ kind, field, typ, param string }
	checks := []check{{"trackedEntities", p.SearchAttr, "text", "filter"}, {"trackedEntities", numAttr, "number", "filter"},
		{"trackedEntities", dateAttr, "date", "filter"}, {"events", numDE, "number", "filter"}, {"events", textDE, "text", "filter"},
		// Android filters events by their person's attributes too.
		{"events", p.SearchAttr, "text", "filterAttributes"}, {"events", numAttr, "number", "filterAttributes"},
		{"trackedEntities", intAttr, "integer", "filter"}, {"trackedEntities", multiAttr, "multi", "filter"},
		{"events", multiDE, "multi", "filter"}}
	var diffs, table []string
	needConn := 0
	ask := func(kind, param, filter string) (string, string, string, string) {
		v := url.Values{"program": {p.ID}, "orgUnit": {m.Facility.ID}, "orgUnits": {m.Facility.ID},
			"orgUnitMode": {"SELECTED"}, "paging": {"false"}, param: {filter}}
		names := byTE
		if kind == "events" {
			v.Del("orgUnits")
			v.Set("enrollments", strings.Join(enrs, ",")) // central refuses events= with a filter (E1003)
			v.Set("fields", "event")
			names = byEv
		} else {
			v.Del("orgUnit")
			v.Set("trackedEntities", strings.Join(tes, ","))
			v.Set("fields", "trackedEntity")
		}
		path := "/api/tracker/" + kind + "?" + v.Encode()
		cst, cb, err := central.Do("GET", path, nil)
		if err != nil {
			t.Fatal(err)
		}
		hst, hb := nurse.Do("GET", path, nil)
		return answer(cst, cb, kind, names), answer(hst, hb, kind, names), string(cb), string(hb)
	}
	for _, c := range checks {
		if c.field == "" {
			continue
		}
		// A bare filter=<uid>: central reads it as !null.
		if want, got, cb, hb := ask(c.kind, c.param, c.field); want != got {
			diffs = append(diffs, fmt.Sprintf("%s %s bare filter\n    central %s\n    hub     %s\n    (central %.200s; hub %.200s)", c.kind, c.typ, want, got, cb, hb))
		}
		for _, op := range append(ops, "null", "!null") {
			list := operands[c.typ]
			if op == "null" || op == "!null" {
				list = []string{""}
			}
			for _, val := range list {
				filter := c.field + ":" + op
				if val != "" {
					filter += ":" + val
				}
				want, got, cb, hb := ask(c.kind, c.param, filter)
				table = append(table, fmt.Sprintf("%s %s %s %-8s %-22q central %s", c.kind, c.param, c.typ, op, val, want))
				if (c.typ == "text" || c.typ == "multi") && (op == "gt" || op == "ge" || op == "lt" || op == "le") && got == "HTTP 503" {
					// By design: central orders text (and 2.42's multi-text, which is
					// text there) by its database's collation, which the hub cannot
					// know; offline these need a connection.
					needConn++
					continue
				}
				if want != got {
					diffs = append(diffs, fmt.Sprintf("%s %s %s %s:%s\n    central %s\n    hub     %s\n    (central %.200s; hub %.200s)",
						c.kind, c.param, c.typ, op, val, want, got, cb, hb))
				}
			}
		}
		// A range, as Capture web sends it for numbers and dates.
		if c.typ == "number" || c.typ == "integer" || c.typ == "date" {
			lo, hi := "9.5", "100"
			if c.typ == "integer" {
				lo = "9"
			}
			if c.typ == "date" {
				lo, hi = "2024-01-01", "2024-01-31"
			}
			filter := c.field + ":ge:" + lo + ":le:" + hi
			want, got, cb, hb := ask(c.kind, c.param, filter)
			table = append(table, fmt.Sprintf("%s %s range %s..%s central %s", c.kind, c.typ, lo, hi, want))
			if want != got {
				diffs = append(diffs, fmt.Sprintf("%s %s range %s\n    central %s\n    hub     %s\n    (central %.200s; hub %.200s)", c.kind, c.typ, filter, want, got, cb, hb))
			}
		}
	}
	// Central refuses event UIDs with a filter; the hub must too.
	if numDE != "" {
		path := "/api/tracker/events?" + url.Values{"program": {p.ID}, "events": {strings.Join(evs, ",")},
			"filter": {numDE + ":gt:0"}, "fields": {"event"}}.Encode()
		cst, _, _ := central.Do("GET", path, nil)
		if hst, hb := nurse.Do("GET", path, nil); hst != cst {
			diffs = append(diffs, fmt.Sprintf("events= with a filter: central %d, hub %d %.200s", cst, hst, hb))
		}
	}
	if f := os.Getenv("HUB_REPORT"); f != "" {
		os.WriteFile(f+".filters.txt", []byte(strings.Join(table, "\n")+"\n"), 0o644)
	}
	t.Logf("%d filters compared; %d text ranges need a connection offline, by design", len(table), needConn)
	if len(diffs) > 0 {
		t.Errorf("%d of %d filters answer differently offline:\n%s", len(diffs), len(table), strings.Join(diffs, "\n"))
	}
}

// answer is a filter's result as the probes it returned (sorted), or its error class.
func answer(st int, body []byte, kind string, names map[string]string) string {
	if st != 200 {
		return fmt.Sprintf("HTTP %d", st)
	}
	var r map[string][]map[string]any
	json.Unmarshal(body, &r)
	key := map[string]string{"trackedEntities": "trackedEntity", "events": "event"}[kind]
	var out []string
	for _, x := range r[kind] {
		if s, _ := x[key].(string); names[s] != "" {
			out = append(out, names[s])
		}
	}
	slices.Sort(out)
	return "[" + strings.Join(out, " ") + "]"
}

func setAttr(payload map[string]any, attr, value string) {
	te := payload["trackedEntities"].([]any)[0].(map[string]any)
	var keep []any
	for _, a := range te["attributes"].([]any) {
		if a.(map[string]any)["attribute"] != attr {
			keep = append(keep, a)
		}
	}
	if value != "" {
		keep = append(keep, map[string]any{"attribute": attr, "value": value})
	}
	te["attributes"] = keep
}

// probeAttribute finds or creates a searchable attribute of valueType on the program
// (on the disposable test instance; its code names it).
func probeAttribute(t *testing.T, e *tk.Env, p tk.Program, code, name, valueType string, optionSet ...string) string {
	t.Helper()
	var r struct {
		Attrs []struct{ ID string } `json:"trackedEntityAttributes"`
	}
	if err := e.Central.JSON("GET", "/api/trackedEntityAttributes?filter=code:eq:"+code+"&fields=id", nil, &r); err != nil {
		t.Fatal(err)
	}
	id := tk.UID()
	if len(r.Attrs) > 0 {
		id = r.Attrs[0].ID
	} else {
		a := map[string]any{"id": id, "code": code, "name": name, "shortName": name, "valueType": valueType, "aggregationType": "NONE"}
		if len(optionSet) > 0 {
			a["optionSet"] = map[string]any{"id": optionSet[0]}
		}
		if st, b, err := e.Central.Do("POST", "/api/metadata", map[string]any{"trackedEntityAttributes": []any{a}}); err != nil || st != 200 {
			t.Fatalf("creating %s: %d %v %.400s", code, st, err, b)
		}
	}
	var prog struct {
		PTEAs []struct {
			TEA struct{ ID string } `json:"trackedEntityAttribute"`
		} `json:"programTrackedEntityAttributes"`
	}
	if err := e.Central.JSON("GET", "/api/programs/"+p.ID+"?fields=programTrackedEntityAttributes[trackedEntityAttribute[id]]", nil, &prog); err != nil {
		t.Fatal(err)
	}
	for _, a := range prog.PTEAs {
		if a.TEA.ID == id {
			return id
		}
	}
	patch := []any{map[string]any{"op": "add", "path": "/programTrackedEntityAttributes/-", "value": map[string]any{
		"trackedEntityAttribute": map[string]any{"id": id}, "searchable": true, "displayInList": false, "mandatory": false}}}
	if st, b, err := e.Central.Do("PATCH", "/api/programs/"+p.ID, patch); err != nil || st >= 300 {
		t.Fatalf("adding %s to %s: %d %v %.400s", code, p.ID, st, err, b)
	}
	return id
}

// probeDataElements picks a stage of the program with a NUMBER data element and a
// TEXT or LONG_TEXT one, neither with an option set.
func probeDataElements(t *testing.T, e *tk.Env, p tk.Program) (stage, num, text string) {
	t.Helper()
	var r struct {
		Stages []struct {
			ID   string `json:"id"`
			PSDE []struct {
				DE struct {
					ID        string `json:"id"`
					ValueType string `json:"valueType"`
					OptionSet bool   `json:"optionSetValue"`
				} `json:"dataElement"`
			} `json:"programStageDataElements"`
		} `json:"programStages"`
	}
	if err := e.Central.JSON("GET", "/api/programs/"+p.ID+"?fields=programStages[id,programStageDataElements[dataElement[id,valueType,optionSetValue]]]", nil, &r); err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Stages {
		num, text = "", ""
		for _, d := range s.PSDE {
			switch {
			case d.DE.OptionSet:
			case d.DE.ValueType == "NUMBER" && num == "":
				num = d.DE.ID
			case (d.DE.ValueType == "TEXT" || d.DE.ValueType == "LONG_TEXT") && text == "":
				text = d.DE.ID
			}
		}
		if num != "" && text != "" {
			return s.ID, num, text
		}
	}
	return p.Stages[0], "", ""
}

// probeOptionSet finds or creates a MULTI_TEXT option set with the codes Red, Blue,
// Green and abc.
func probeOptionSet(t *testing.T, e *tk.Env) string {
	t.Helper()
	var r struct {
		Sets []struct{ ID string } `json:"optionSets"`
	}
	if err := e.Central.JSON("GET", "/api/optionSets?filter=code:eq:HUB_PROBE_COLOURS&fields=id", nil, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Sets) > 0 {
		return r.Sets[0].ID
	}
	id := tk.UID()
	var opts, refs []any
	for i, c := range []string{"Red", "Blue", "Green", "abc"} {
		oid := tk.UID()
		opts = append(opts, map[string]any{"id": oid, "code": c, "name": "Hub probe " + c, "sortOrder": i + 1, "optionSet": map[string]any{"id": id}})
		refs = append(refs, map[string]any{"id": oid})
	}
	if st, b, err := e.Central.Do("POST", "/api/metadata", map[string]any{
		"optionSets": []any{map[string]any{"id": id, "code": "HUB_PROBE_COLOURS", "name": "Hub probe colours", "valueType": "MULTI_TEXT", "options": refs}},
		"options":    opts}); err != nil || st != 200 {
		t.Fatalf("creating the option set: %d %v %.400s", st, err, b)
	}
	return id
}

// probeMultiDataElement finds or creates a MULTI_TEXT data element with the option set
// and puts it in the stage.
func probeMultiDataElement(t *testing.T, e *tk.Env, stage, optionSet string) string {
	t.Helper()
	var r struct {
		DEs []struct{ ID string } `json:"dataElements"`
	}
	if err := e.Central.JSON("GET", "/api/dataElements?filter=code:eq:HUB_PROBE_MULTI_DE&fields=id", nil, &r); err != nil {
		t.Fatal(err)
	}
	id := tk.UID()
	if len(r.DEs) > 0 {
		id = r.DEs[0].ID
	} else if st, b, err := e.Central.Do("POST", "/api/metadata", map[string]any{"dataElements": []any{map[string]any{
		"id": id, "code": "HUB_PROBE_MULTI_DE", "name": "Hub probe multi", "shortName": "Hub probe multi", "valueType": "MULTI_TEXT",
		"domainType": "TRACKER", "aggregationType": "NONE", "optionSet": map[string]any{"id": optionSet}}}}); err != nil || st != 200 {
		t.Fatalf("creating the multi-text data element: %d %v %.400s", st, err, b)
	}
	var ps struct {
		PSDE []struct {
			DE struct{ ID string } `json:"dataElement"`
		} `json:"programStageDataElements"`
	}
	if err := e.Central.JSON("GET", "/api/programStages/"+stage+"?fields=programStageDataElements[dataElement[id]]", nil, &ps); err != nil {
		t.Fatal(err)
	}
	for _, d := range ps.PSDE {
		if d.DE.ID == id {
			return id
		}
	}
	patch := []any{map[string]any{"op": "add", "path": "/programStageDataElements/-", "value": map[string]any{
		"dataElement": map[string]any{"id": id}, "compulsory": false}}}
	if st, b, err := e.Central.Do("PATCH", "/api/programStages/"+stage, patch); err != nil || st >= 300 {
		t.Fatalf("adding the data element to %s: %d %v %.400s", stage, st, err, b)
	}
	return id
}
