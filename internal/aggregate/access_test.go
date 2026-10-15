package aggregate

import (
	"os"
	"testing"
)

func metadataFixture(t *testing.T) *Access {
	t.Helper()
	b, err := os.ReadFile("../../testdata/2.42/dataEntry_metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	a, err := ParseMetadata(b, []string{"M_dhis-web-aggregate-data-entry"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestParseMetadata(t *testing.T) {
	a := metadataFixture(t)
	ds := a.DataSets["BfMAe6Itzgt"]
	if ds == nil || ds.PeriodType != "Monthly" || ds.OpenFuturePeriods != 10 || !ds.Assigned("DiszpKrYNg8") {
		t.Fatalf("Child Health: %+v", ds)
	}
	if len(ds.Compulsory) != 1 || ds.Compulsory[0] != (Operand{"O05mAByOgAv", "V6L425pT3A0"}) {
		t.Errorf("compulsory: %+v", ds.Compulsory)
	}
	if ds.Elements["fClA2Erf6IO"] != "dzjKKQq0cSO" || a.Elements["fClA2Erf6IO"].ValueType != "NUMBER" {
		t.Errorf("Penta1: %q %+v", ds.Elements["fClA2Erf6IO"], a.Elements["fClA2Erf6IO"])
	}
	aoc, ok := a.AOC(ds, "", nil)
	if !ok || aoc != "HllvX50cXC0" {
		t.Errorf("default attribute option combo: %q %v", aoc, ok)
	}
	if combo, opts, ok := a.Attribute(ds, aoc); !ok || combo != "bjDvmb4bfuf" || len(opts) != 1 || opts[0] != "xYerKDKCefk" {
		t.Errorf("default attribute: %q %v %v", combo, opts, ok)
	}
	if !a.MayUse(ds, aoc) || a.MayUse(ds, "NoSuchAoc01") {
		t.Error("MayUse")
	}
	if a.COCName("V6L425pT3A0") != "Outreach, <1y" {
		t.Errorf("COCName: %q", a.COCName("V6L425pT3A0"))
	}
	if a.Bypass {
		t.Error("no F_EDIT_EXPIRED, no bypass")
	}
	if b, _ := ParseMetadataFile(t, "F_EDIT_EXPIRED"); !b.Bypass {
		t.Error("F_EDIT_EXPIRED bypasses expiry")
	}
}

func ParseMetadataFile(t *testing.T, auth string) (*Access, error) {
	b, _ := os.ReadFile("../../testdata/2.42/dataEntry_metadata.json")
	return ParseMetadata(b, []string{auth})
}

func TestAccessKeyIgnoresOrder(t *testing.T) {
	a, b := metadataFixture(t), metadataFixture(t)
	if a.Key() != b.Key() || a.Key() == "" {
		t.Fatal("same metadata, same key")
	}
	delete(b.Options, "xYerKDKCefk")
	if a.Key() == b.Key() {
		t.Fatal("other options, other key")
	}
}

func TestValidValue(t *testing.T) {
	for _, c := range []struct {
		typ, v string
		ok     bool
	}{
		{"NUMBER", "1.5", true}, {"NUMBER", "x1", false}, {"INTEGER", "-3", true}, {"INTEGER", "1.5", false},
		{"INTEGER_POSITIVE", "0", false}, {"INTEGER_ZERO_OR_POSITIVE", "0", true}, {"INTEGER_NEGATIVE", "-1", true},
		{"PERCENTAGE", "101", false}, {"PERCENTAGE", "50", true}, {"BOOLEAN", "true", true}, {"BOOLEAN", "yes", false},
		{"TRUE_ONLY", "true", true}, {"TRUE_ONLY", "false", false}, {"DATE", "2026-02-30", false},
		{"DATE", "2026-02-28", true}, {"TEXT", "anything", true}, {"UNIT_INTERVAL", "0.5", true}, {"UNIT_INTERVAL", "2", false},
	} {
		if got := ValidValue(c.typ, c.v); got != c.ok {
			t.Errorf("%s %q: %v, want %v", c.typ, c.v, got, c.ok)
		}
	}
}

func TestRetention(t *testing.T) {
	r := Retention{Default: 12, DataSets: map[string]int{"A": 3}}
	if r.For("A") != 3 || r.For("B") != 12 || (Retention{}).For("B") != 12 {
		t.Fatal("retention")
	}
}
