package valuetype

import (
	"strings"
	"testing"
)

// The values central 2.43.2.1 took and refused on 17 November 2026 (dry-run imports of
// an event data value and an attribute, the private notes, docs/sessions/i8c-2.43.2.1.txt).
func TestValidAsCentral(t *testing.T) {
	for _, c := range []struct {
		typ, v string
		ok     bool
	}{
		{"NUMBER", "5", true}, {"NUMBER", "-5", true}, {"NUMBER", "5.0", true}, {"NUMBER", "12345678901234567890", true},
		{"NUMBER", strings.Repeat("1", 40), true}, {"NUMBER", "+5", false}, {"NUMBER", "5.", false}, {"NUMBER", ".5", false},
		{"NUMBER", "1e5", false}, {"NUMBER", "1E5", false}, {"NUMBER", "1e-5", false}, {"NUMBER", "1.5e308", false},
		{"NUMBER", "Infinity", false}, {"NUMBER", "NaN", false}, {"NUMBER", "0x10", false}, {"NUMBER", "1_000", false},
		{"NUMBER", " 5", false}, {"NUMBER", "5 ", false}, {"NUMBER", "1,5", false}, {"NUMBER", "abc", false},
		{"NUMBER", strings.Repeat("1", 400), false}, {"NUMBER", strings.Repeat("1", 1000000), false},
		{"INTEGER", "5", true}, {"INTEGER", "05", false}, {"INTEGER", "2147483647", true}, {"INTEGER", "2147483648", false},
		{"INTEGER", "-2147483648", true}, {"INTEGER", "1.0", false},
		{"INTEGER_POSITIVE", "0", false}, {"INTEGER_ZERO_OR_POSITIVE", "0", true}, {"INTEGER_NEGATIVE", "-1", true},
		{"PERCENTAGE", "101", false}, {"PERCENTAGE", "50", true}, {"UNIT_INTERVAL", "0.5", true}, {"UNIT_INTERVAL", "2", false},
		{"TEXT", "anything", true},
	} {
		if got := Valid(c.typ, c.v); got != c.ok {
			t.Errorf("%s %.20q: %v, want %v", c.typ, c.v, got, c.ok)
		}
	}
}
