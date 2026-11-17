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
// (docs/DEPLOYMENT.md, appendix A). A hub behind by more than half an hour lists every device with the
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

// The hub's page warns when the hub's clock is more than ten minutes ahead of the
// Date of central's latest answer (a Windows PC's clock set ahead by hand). An answer
// with the right time clears it; central's older answers do not raise it.
func TestClockAhead(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(config.Default(), "test", st)
	h.noteCentralDate(time.Now().Add(-5 * time.Minute))
	if d := h.ClockAhead(); d != 0 {
		t.Errorf("five minutes ahead: ClockAhead %s, want 0", d)
	}
	h.noteCentralDate(time.Now().Add(-30 * time.Minute))
	if d := h.ClockAhead(); d < 29*time.Minute || h.ClockBehind() != 0 {
		t.Errorf("half an hour ahead: ClockAhead %s, ClockBehind %s", d, h.ClockBehind())
	}
	h.noteCentralDate(time.Now())
	if d := h.ClockAhead(); d != 0 {
		t.Errorf("after an answer with the right time: ClockAhead %s, want 0", d)
	}
}
