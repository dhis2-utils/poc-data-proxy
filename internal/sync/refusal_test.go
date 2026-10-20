package sync

import (
	"strings"
	"testing"
)

// Security review S7: an aggregate refusal is kept as the outbox entry's error and shown
// on the hub's admin page; central may quote the entered value in it. Values are
// hidden there as for tracker refusals; identifiers stay.
func TestAggregateRefusalHidesValues(t *testing.T) {
	got := refusalNote("Data value `12 abc` must match data element value type `NUMBER` for `Hz6sDTFMbhn`")
	if strings.Contains(got, "12 abc") || !strings.Contains(got, "Hz6sDTFMbhn") {
		t.Fatalf("%q", got)
	}
}
