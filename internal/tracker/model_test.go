package tracker

import "testing"

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
	if got := OutgoingReports(reps); got[0].Message != "" || got[0].UID != "AAAAAAAAAA1" || got[0].ErrorCode != "E1007" {
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
