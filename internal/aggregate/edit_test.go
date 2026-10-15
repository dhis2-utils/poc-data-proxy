package aggregate

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
)

func formFixture(t *testing.T) Form {
	b, err := os.ReadFile("../../testdata/2.42/dataEntry_dataValues.json")
	if err != nil {
		t.Fatal(err)
	}
	var f Form
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func ptr[T any](v T) *T { return &v }

func TestFormApply(t *testing.T) {
	f := formFixture(t)
	k := FormKey{"BfMAe6Itzgt", "202610", "DiszpKrYNg8", "HllvX50cXC0"}
	f.Apply(&Edit{Op: OpValue, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0", Value: ptr("44"), By: "nurse",
		At: "2026-10-15T10:00:00.000"}, "bjDvmb4bfuf", []string{"xYerKDKCefk"})
	if v, ok := f.Value("fClA2Erf6IO", "V6L425pT3A0"); !ok || v != "44" {
		t.Fatalf("value after edit: %q %v", v, ok)
	}
	f.Apply(&Edit{Op: OpValue, Form: k, DataElement: "NewDeAaaa01", COC: "V6L425pT3A0", Comment: ptr("seen"), By: "nurse",
		At: "2026-10-15T10:00:00.000"}, "bjDvmb4bfuf", []string{"xYerKDKCefk"})
	b, _ := json.Marshal(f)
	if !strings.Contains(string(b), `"comment":"seen"`) || !strings.Contains(string(b), `"options":["xYerKDKCefk"]`) {
		t.Fatalf("new cell with a comment: %s", b)
	}
	f.Apply(&Edit{Op: OpDelete, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0", By: "nurse"}, "", nil)
	if _, ok := f.Value("fClA2Erf6IO", "V6L425pT3A0"); ok {
		t.Fatal("deleted value still there")
	}
	f.Apply(&Edit{Op: OpComplete, Form: k, Completed: true, By: "clerk", At: "2026-10-15T11:00:00.000"}, "", nil)
	cs := f["completeStatus"].(map[string]any)
	if cs["complete"] != true || cs["lastUpdatedBy"] != "clerk" {
		t.Fatalf("completeStatus: %v", cs)
	}
}

func TestEditKeysAndRequest(t *testing.T) {
	k := FormKey{"BfMAe6Itzgt", "202610", "DiszpKrYNg8", "HllvX50cXC0"}
	v := &Edit{Op: OpValue, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0", Value: ptr("5")}
	c := &Edit{Op: OpValue, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0", Comment: ptr("x")}
	d := &Edit{Op: OpDelete, Form: k, DataElement: "fClA2Erf6IO", COC: "V6L425pT3A0"}
	if v.CellKey() == c.CellKey() || v.CellKey() != d.CellKey() {
		t.Fatalf("value and comment are separate fields; delete replaces the value: %s %s %s", v.CellKey(), c.CellKey(), d.CellKey())
	}
	m, p, body := v.Request()
	u, _ := url.Parse(p)
	q := u.Query()
	if m != "POST" || u.Path != "/api/dataValues" || q.Get("value") != "5" || q.Get("ds") != "BfMAe6Itzgt" || body != nil ||
		q.Has("cc") {
		t.Fatalf("value request: %s %s", m, p)
	}
	att := &Edit{Op: OpValue, Form: k, CC: "SynCcDonor1", CP: []string{"b", "a"}, DataElement: "x", COC: "y", Value: ptr("1")}
	_, p, _ = att.Request()
	u, _ = url.Parse(p)
	if u.Query().Get("cc") != "SynCcDonor1" || u.Query().Get("cp") != "b;a" {
		t.Fatalf("attribute request: %s", p)
	}
	if m, p, _ := d.Request(); m != "DELETE" || !strings.HasPrefix(p, "/api/dataValues?") {
		t.Fatalf("delete: %s %s", m, p)
	}
	m, p, body = (&Edit{Op: OpComplete, Form: k, Completed: true}).Request()
	if m != "POST" || p != "/api/dataEntry/dataSetCompletion" || !strings.Contains(string(body), `"completed":true`) {
		t.Fatalf("completion: %s %s %s", m, p, body)
	}
}
