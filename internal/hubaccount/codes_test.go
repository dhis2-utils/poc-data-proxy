package hubaccount

import (
	"bytes"
	"testing"
)

// The Facility Hub app on central computes the same codes (docs/FACILITY-HUB-APP.md):
// these values are its test vectors too.
func TestCodes(t *testing.T) {
	pub := bytes.Repeat([]byte{1}, 32)
	key := bytes.Repeat([]byte{2}, 32)
	if got := AccountCode(pub, "hub-Abc123XyZ90"); got != "OUMC-LEW6-EOCO-KJ6V" {
		t.Errorf("AccountCode = %q", got)
	}
	for _, addr := range []string{"https://new.example.org/dhis", " HTTPS://New.Example.org/dhis/ "} {
		if got := MoveCode(key, addr); got != "IJ3O2-BXZPR" {
			t.Errorf("MoveCode(%q) = %q", addr, got)
		}
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
