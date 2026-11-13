package tracker

import (
	"net/url"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	got := Redact("Non-unique attribute value `886649902` for attribute `AuPLng5hLbE`.")
	if got != "Non-unique attribute value `***` for attribute `AuPLng5hLbE`." {
		t.Fatal(got)
	}
}

func TestParseFields(t *testing.T) {
	f := ParseFields(":all,!relationships,programOwners[orgUnit,program],enrollments[*,events[*]]")
	if !f.Has("enrollments") || f.Has("relationships") || !f.Sub("enrollments").Has("events") || !f.Has("attributes") {
		t.Fatal("fields parse")
	}
	d := FieldsOrDefault("trackedEntities", "")
	if d.Has("enrollments") || !d.Has("attributes") {
		t.Fatal("default fields")
	}
}

// A long or deeply nested fields value is refused before it is parsed (review R-20);
// what clients send passes.
func TestFieldsLimits(t *testing.T) {
	for _, s := range []string{defaultFields["relationships"], ":all,enrollments[*,events[*,dataValues[*]]]"} {
		if err := checkParams("trackedEntities", url.Values{"fields": {s}}); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	for _, s := range []string{strings.Repeat("a[", 11), strings.Repeat("a,", 5000)} {
		if err := checkParams("trackedEntities", url.Values{"fields": {s}}); err == nil || err.Status != 400 {
			t.Fatalf("%.20s…: %v", s, err)
		}
	}
}

func FuzzParseFields(f *testing.F) {
	for _, s := range []string{"*", ":all,!relationships,programOwners[orgUnit,program],enrollments[*,events[*]]",
		defaultFields["relationships"], "a[b(c]d)", "]]][[[", "name~rename(x),b::size"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if checkFields(s) != nil {
			return
		}
		fs := ParseFields(s)
		fs.Has("events")
		fs.Sub("enrollments").Has("events")
	})
}

func TestFlattenNested(t *testing.T) {
	items := Flatten(M{"trackedEntities": []any{M{"trackedEntity": "AAAAAAAAAA1", "enrollments": []any{
		M{"enrollment": "BBBBBBBBBB1", "events": []any{M{"event": "CCCCCCCCCC1"}}}}}}})
	if len(items) != 3 || items[1].TE != "AAAAAAAAAA1" || items[2].Enrollment != "BBBBBBBBBB1" || items[0].Data["enrollments"] != nil {
		t.Fatalf("%+v", items)
	}
}

func TestMergeKeepsOtherAttributes(t *testing.T) {
	old := M{"attributes": []any{M{"attribute": "a", "value": "1"}, M{"attribute": "b", "value": "2"}}}
	got := Merge(old, M{"attributes": []any{M{"attribute": "b", "value": "3"}}})
	vals := attrValues(got, "attributes", "attribute")
	if vals["a"] != "1" || vals["b"] != "3" {
		t.Fatal(vals)
	}
}

// Review M6: free text from central never leaves the hub, however a value is quoted.
func TestOutgoingMessage(t *testing.T) {
	reps := []ErrorReport{{Message: "Value 'Amina Otieno' is not valid", ErrorCode: "E1007", TrackerType: "TRACKED_ENTITY", UID: "AAAAAAAAAA1"}}
	if got := OutgoingMessage(reps[0].Message, reps); got != "rejected by central: E1007" {
		t.Fatal(got)
	}
	if got := OutgoingReports(reps, nil); got[0].Message != "" || got[0].UID != "AAAAAAAAAA1" || got[0].ErrorCode != "E1007" {
		t.Fatalf("%+v", got)
	}
	for msg, want := range map[string]string{
		"blocked by AAAAAAAAAA1":                  "blocked by AAAAAAAAAA1",
		"central returned 409 (E1064)":            "central returned 409 (E1064)",
		"central returned 400: {\"x\":\"Amina\"}": "rejected by central",
		"blocked by Amina Otieno":                 "rejected by central",
	} {
		if got := OutgoingMessage(msg, nil); got != want {
			t.Errorf("%q: got %q", msg, got)
		}
	}
}

// Central's messages leave the hub with only metadata references in them: what is
// quoted in backticks stays if it is a type name or an identifier, unless it is one of
// the values the hub sent; anything the hub sent is masked wherever it appears; a
// message without backticks is left out.
func TestOutgoingReportsMasked(t *testing.T) {
	payload := M{"event": "EvtAAAAAAA1", "program": "PrgAAAAAAA1", "programStage": "StgAAAAAAA1", "orgUnit": "OuAAAAAAAA1",
		"occurredAt": "2026-10-01", "dataValues": []any{
			M{"dataElement": "DeAAAAAAAA1", "value": "Amina Otieno"},
			M{"dataElement": "DeAAAAAAAA2", "value": "Nairobi7Rd9"}, // a value shaped like an identifier
			M{"dataElement": "DeAAAAAAAA3", "value": "0722123456"},
		}, "notes": []any{M{"value": "lives near the market"}}}
	sent := SentValues(payload)
	for msg, want := range map[string]string{
		"Mandatory DataElement `DeAAAAAAAA1` is not present in Event `EvtAAAAAAA1`.":     "Mandatory DataElement `DeAAAAAAAA1` is not present in Event `EvtAAAAAAA1`.",
		"User: `HubUsrNursB` has no data write access to ProgramStage: `StgAAAAAAA1`.":   "User: `HubUsrNursB` has no data write access to ProgramStage: `StgAAAAAAA1`.",
		"Value `Amina Otieno` is not a valid option for `DeAAAAAAAA1`.":                  "Value `***` is not a valid option for `DeAAAAAAAA1`.",
		"Value `Nairobi7Rd9` is not valid for `DeAAAAAAAA2`.":                            "Value `***` is not valid for `DeAAAAAAAA2`.",
		"Non-unique attribute value `886649902` for attribute `AuPLng5hLbE`.":            "Non-unique attribute value `***` for attribute `AuPLng5hLbE`.",
		"Value Amina Otieno for `DeAAAAAAAA1` is too long: lives near the market.":       "Value *** for `DeAAAAAAAA1` is too long: ***.",
		"Phone 0722123456 already used by `DeAAAAAAAA3`.":                                "Phone *** already used by `DeAAAAAAAA3`.",
		"Value `Person` of `DeAAAAAAAA1` is not allowed.":                                "Value `***` of `DeAAAAAAAA1` is not allowed.",
		"Either `program`, `trackedEntityType` or `trackedEntities` should be specified": "Either `program`, `trackedEntityType` or `trackedEntities` should be specified",
		"JSON parse error: problem: UID must be 11 characters, but was: Amina Otieno":    "",
		"Value 'Amina Otieno' is not valid":                                              "",
	} {
		got := OutgoingReports([]ErrorReport{{Message: msg, ErrorCode: "E1000", UID: "EvtAAAAAAA1"}}, sent)
		if got[0].Message != want {
			t.Errorf("%q:\n got %q\nwant %q", msg, got[0].Message, want)
		}
		for _, v := range []string{"Amina", "Otieno", "Nairobi7Rd9", "0722123456", "886649902", "market"} {
			if strings.Contains(got[0].Message, v) {
				t.Errorf("%q leaks %q", msg, v)
			}
		}
	}
}

// Review L4: huge page numbers neither overflow nor panic.
func TestPageOverflow(t *testing.T) {
	objs := []*Obj{{UID: "a"}, {UID: "b"}, {UID: "c"}}
	if got := page(objs, 1<<62, 2, true); got != nil {
		t.Fatal(got)
	}
	if got := page(objs, 2, 2, true); len(got) != 1 || got[0].UID != "c" {
		t.Fatal(got)
	}
	if got := page(objs, 2, 1<<62, true); got != nil {
		t.Fatal(got)
	}
}

func TestNewUID(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if u := NewUID(); !ValidUID(u) {
			t.Fatal(u)
		}
	}
}

// Central ignores parameters it does not know: pageNumber, sent by a client for page,
// is answered page 1 (2.42.8, 2.43.2.1). The hub ignores it too; other unknown names,
// which might filter, it refuses.
func TestIgnoredParams(t *testing.T) {
	for _, coll := range []string{"trackedEntities", "enrollments", "events"} {
		if err := checkParams(coll, url.Values{"pageNumber": {"2"}}); err != nil {
			t.Errorf("%s pageNumber: %v", coll, err)
		}
		if err := checkParams(coll, url.Values{"attributeFilter": {"x"}}); err == nil || err.Param != "attributeFilter" {
			t.Errorf("%s: an unknown name was not refused: %v", coll, err)
		}
	}
}
