package core

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// The device-clock warning compares each device's time with the hub's
// (docs/NO-RTC.md). A hub behind by more than half an hour lists every device with the
// right time as ahead; a hub ahead hides devices that are. Here the test's clock is the
// hub's: a device with the right time reads the hub's now plus how far the hub is
// behind.
func TestNoRTCDeviceClockWarning(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(config.Default(), "test", st) // central's zone unknown: UTC
	upload := func(user int64, device time.Time) {
		stamp := device.UTC().Format(tracker.DHIS2Layout)
		h.NoteClientClock(user, tracker.M{"trackedEntities": []any{tracker.M{"createdAtClient": stamp, "updatedAtClient": stamp}}})
	}
	now := time.Now()
	upload(1, now.Add(3*24*time.Hour))  // right device, hub three days behind
	upload(2, now.Add(2*time.Hour))     // right device, hub two hours behind
	upload(3, now.Add(10*time.Minute))  // right device, hub ten minutes behind
	upload(4, now.Add(-2*24*time.Hour)) // a device a day ahead, hub three days ahead
	var listed []int64
	for _, c := range h.ClientClocksAhead() {
		listed = append(listed, c.UserID)
	}
	if len(listed) != 2 || listed[0] != 1 || listed[1] != 2 {
		t.Errorf("listed %v, want [1 2]: the two right devices the hub is far behind, not the one a day ahead", listed)
	}
}
