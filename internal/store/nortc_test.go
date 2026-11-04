package store

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Reserved values are dropped from the pool by the hub's clock when handed out
// (docs/NO-RTC.md). Central reserves them for 60 days by default. A hub more than 60
// days ahead drops the whole pool at the first offline registration and hands out
// none; a hub behind hands out values whose reservation central has let go.
func TestNoRTCReservedValuesByHubClock(t *testing.T) {
	s := aggStore(t)
	right := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	const layout = "2006-01-02T15:04:05.000"
	var vals []ReservedValue
	for i := range 10 {
		// Reserved over the last 50 days: expiring in 10 to 60 days.
		exp := right.AddDate(0, 0, 10+5*i).Format(layout)
		vals = append(vals, ReservedValue{Attribute: "AtGenerated", Value: json.RawMessage(fmt.Sprintf(`{"value":"%03d","expiryDate":%q}`, i, exp)), ExpiresAt: exp})
	}
	if err := s.AddReservedValues("AtGenerated", vals); err != nil {
		t.Fatal(err)
	}
	if n := s.CountReservedValues("AtGenerated", right.AddDate(0, 0, 30).Format(layout)); n != 6 {
		t.Errorf("30 days ahead the pool counts %d of 10, want 6", n)
	}
	got, err := s.TakeReservedValues("AtGenerated", 1, right.AddDate(0, 0, 61).Format(layout))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || s.CountReservedValues("AtGenerated", right.Format(layout)) != 0 {
		t.Errorf("61 days ahead: took %d, %d left by the right clock; want none and none", len(got), s.CountReservedValues("AtGenerated", right.Format(layout)))
	}
}
