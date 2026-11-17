package tracker

import (
	"strings"
	"testing"
	"time"
)

// I8c: a number value central refuses at the push is refused offline, with central's
// code: E1007 for an attribute, E1302 for a data value. Before, the hub confirmed a
// million-digit NUMBER and central refused it later.
func TestNumberValuesAsCentral(t *testing.T) {
	f := newInputFixture(t)
	f.cat.ValueTypes = map[string]string{inAttrA: "NUMBER", "DeNumber001": "NUMBER", "DeInteger01": "INTEGER"}
	v := f.viewer()
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	codes := func(out *ImportOutcome) []string {
		var c []string
		for _, e := range out.Errors {
			c = append(c, e.ErrorCode)
		}
		return c
	}
	te := func(v string) M {
		return M{"trackedEntities": []any{M{"trackedEntity": "TeInput0001", "trackedEntityType": inTET, "orgUnit": inOU,
			"attributes": []any{M{"attribute": inAttrA, "value": v}}}}}
	}
	ev := func(de, v string) M {
		return M{"events": []any{M{"event": "EvInput0001", "enrollment": "EnInput0001", "programStage": inStA1, "orgUnit": inOU,
			"occurredAt": "2026-11-01", "dataValues": []any{M{"dataElement": de, "value": v}}}}}
	}
	for _, c := range []struct {
		name    string
		payload M
		want    string
	}{
		{"attribute 5", te("5"), ""},
		{"attribute 1e5", te("1e5"), "E1007"},
		{"attribute cleared", te(""), ""},
		{"data value 12.5", ev("DeNumber001", "12.5"), ""},
		{"data value of a million digits", ev("DeNumber001", strings.Repeat("9", 1000000)), "E1302"},
		{"data value +5", ev("DeNumber001", "+5"), "E1302"},
		{"integer 2147483648", ev("DeInteger01", "2147483648"), "E1302"},
		{"text type unchecked", ev("DeText00001", "anything"), ""},
	} {
		got := codes(f.importAs(v, at, c.payload))
		if (c.want == "" && len(got) > 0) || (c.want != "" && (len(got) != 1 || got[0] != c.want)) {
			t.Errorf("%s: %v, want %q", c.name, got, c.want)
		}
	}
}
