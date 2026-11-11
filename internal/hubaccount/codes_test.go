package hubaccount

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The Facility Hub app on central computes the same codes (docs/FACILITY-HUB-APP.md):
// these values are its test vectors too.
func TestCodes(t *testing.T) {
	pub := bytes.Repeat([]byte{1}, 32)
	key := bytes.Repeat([]byte{2}, 32)
	if got := AccountCode(pub, "hub-Abc123XyZ90", "DiszpKrYNg8"); got != "VMC4-PMVY-I5K4-UMUO" {
		t.Errorf("AccountCode = %q", got)
	}
	for _, addr := range []string{"https://new.example.org/dhis", " HTTPS://New.Example.org/dhis/ "} {
		if got := MoveCode(key, addr); got != "IJ3O2-BXZPR" {
			t.Errorf("MoveCode(%q) = %q", addr, got)
		}
	}
	if MoveCode(key, "https://new.example.org:443/dhis") != MoveCode(key, "https://new.example.org/dhis") {
		t.Error("the default port changes the code")
	}
	if MoveCode(key, "https://new.example.org:8443/dhis") == MoveCode(key, "https://new.example.org/dhis") {
		t.Error("the code does not depend on the port")
	}
	if pub := bytes.Repeat([]byte{1}, 32); AccountCode(pub, "hub-Abc123XyZ90", "DiszpKrYNg8") == AccountCode(pub, "hub-Abc123XyZ90", "g8upMTyEZGZ") {
		t.Error("the account code does not depend on the facility")
	}
	// Addresses a move code is never made for: the app and the hub must agree on the
	// form, and these have more than one.
	for _, bad := range []string{"https://bücher.example/dhis", "https://h.example/a b", "https://h.example/x?y=1", "https://h.example/x#f", "ftp://h.example", "https://h.example/a{b}", "https://h.example:/dhis", "https://h.example/a%20b"} {
		if CheckAddress(bad) == nil {
			t.Errorf("CheckAddress(%q) accepted", bad)
		}
	}
	if err := CheckAddress("https://new.example.org:8443/dhis"); err != nil {
		t.Error(err)
	}
	if MoveCode(key, "https://new.example.org/dhis") == MoveCode(key, "https://new.example.org") {
		t.Error("the code does not depend on the path")
	}
	for _, typed := range []string{"abcde-fghij", "ABCDE FGHIJ", "abcdefghij"} {
		if NormalizeCode(typed) != "ABCDEFGHIJ" {
			t.Errorf("NormalizeCode(%q) = %q", typed, NormalizeCode(typed))
		}
	}
}

// The table the Facility Hub app checks too (apps/facility-hub/src/hubaccount/codes.test.ts).
func TestAddressTable(t *testing.T) {
	b, err := os.ReadFile("testdata/addresses.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Accepted []struct{ Address, Normalized string }
		Refused  []string
	}
	if err := json.Unmarshal(b, &table); err != nil {
		t.Fatal(err)
	}
	for _, a := range table.Accepted {
		if err := CheckAddress(a.Address); err != nil {
			t.Errorf("CheckAddress(%q): %v", a.Address, err)
		}
		if got := NormalizeAddress(a.Address); got != a.Normalized {
			t.Errorf("NormalizeAddress(%q) = %q, want %q", a.Address, got, a.Normalized)
		}
	}
	for _, a := range table.Refused {
		if CheckAddress(a) == nil {
			t.Errorf("CheckAddress(%q) accepted", a)
		}
	}
}

// The Facility Hub app gives the same code for the same input
// (apps/facility-hub/src/hubaccount/codes.test.ts): key order, spaces and the codes
// field do not count; the hub, the kind and every value do.
func TestInstructionCodeVector(t *testing.T) {
	key := bytes.Repeat([]byte{2}, 32)
	const obj = `{"url":"https://new.example.org/dhis","from":"2026-11-12T08:00:00.000Z","issued":"2026-11-11T10:00:00.000Z",` +
		`"note":"Bø <&>   \"q\" \n","n":[10,1.5,true,null,{"b":1,"a":"x"}],"codes":{"hub-x":"zzz"}}`
	const want = "EiA0hT5/YySwjrCFD87XUg=="
	got, err := InstructionCode(key, "hub-Abc123XyZ90", "facility-hub-admin/central-move", json.RawMessage(obj))
	if err != nil || got != want {
		t.Fatalf("InstructionCode = %q, %v; want %q", got, err, want)
	}
	same := `{ "n": [10, 1.5, true, null, {"a": "x", "b": 1}], "codes": {}, "note": "Bø <&>   \"q\" \n",
		"issued": "2026-11-11T10:00:00.000Z", "from": "2026-11-12T08:00:00.000Z", "url": "https://new.example.org/dhis" }`
	if c, _ := InstructionCode(key, "hub-Abc123XyZ90", "facility-hub-admin/central-move", json.RawMessage(same)); c != want {
		t.Errorf("the same object written differently: %q", c)
	}
	for name, c := range map[string]func() (string, error){
		"hub": func() (string, error) {
			return InstructionCode(key, "hub-Other", "facility-hub-admin/central-move", json.RawMessage(obj))
		},
		"kind": func() (string, error) {
			return InstructionCode(key, "hub-Abc123XyZ90", "facility-hub-admin/release", json.RawMessage(obj))
		},
		"value": func() (string, error) {
			return InstructionCode(key, "hub-Abc123XyZ90", "facility-hub-admin/central-move", json.RawMessage(strings.Replace(obj, "10,", "11,", 1)))
		},
	} {
		if got, _ := c(); got == want {
			t.Errorf("the code does not depend on the %s", name)
		}
	}
}

// What Sign writes carries a code each hub accepts, for that hub only.
func TestSign(t *testing.T) {
	keys := map[string][]byte{"hub-a": bytes.Repeat([]byte{1}, 32), "hub-b": bytes.Repeat([]byte{2}, 32)}
	out, err := Sign(keys, "facility-hub-admin/release", json.RawMessage(`{"version":"0.2.0","codes":{"hub-z":"old"}}`), "2026-11-11T10:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Version, Issued string
		Codes           map[string]string
	}
	json.Unmarshal(out, &v)
	if v.Version != "0.2.0" || v.Issued != "2026-11-11T10:00:00.000Z" || len(v.Codes) != 2 {
		t.Fatalf("signed: %s", out)
	}
	for hub, key := range keys {
		if c, _ := InstructionCode(key, hub, "facility-hub-admin/release", out); c != v.Codes[hub] {
			t.Errorf("%s: code %q, want %q", hub, v.Codes[hub], c)
		}
	}
}
