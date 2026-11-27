package core

import (
	"cmp"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// ClientClockAhead is how far a device's clock may run ahead of central's before the
// hub's page warns about it. Half an hour, so that a tablet set one time zone ahead
// of central counts: its newest record is always saved a little before it is sent.
const ClientClockAhead = 30 * time.Minute

// ClientClock is the latest reading of one user's device clock: how far the newest
// client time in their last upload ran ahead of the hub's clock, both read in
// central's time zone (negative: behind, or saved earlier), and when.
type ClientClock struct {
	UserID int64
	Ahead  time.Duration
	At     time.Time
}

// clientClocks holds the latest reading per user, in memory only: a restart forgets
// them until each device sends again.
type clientClocks struct {
	mu sync.Mutex
	by map[int64]ClientClock
}

// NoteClientClock reads the device clock from an upload's createdAtClient and
// updatedAtClient (F4: a tablet a day ahead dates enrollments in central's future).
// A clock behind cannot be told from records saved earlier and sent late, so only a
// clock ahead warns (ClientClocksAhead).
func (h *Hub) NoteClientClock(userID int64, payload tracker.M) {
	newest, ok := tracker.NewestClientTime(payload)
	if !ok || userID == 0 || h.ClockBehind() > 0 {
		return
	}
	now := time.Now()
	c := now.In(h.centralZone())
	wall := time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), c.Minute(), c.Second(), c.Nanosecond(), time.UTC)
	h.clocks.mu.Lock()
	defer h.clocks.mu.Unlock()
	if h.clocks.by == nil {
		h.clocks.by = map[int64]ClientClock{}
	}
	h.clocks.by[userID] = ClientClock{UserID: userID, Ahead: newest.Sub(wall), At: now}
}

// ClientClocksAhead are the users whose device clock read more than ClientClockAhead
// ahead at their latest upload, the furthest ahead first.
func (h *Hub) ClientClocksAhead() []ClientClock {
	if h.ClockBehind() > 0 {
		return nil // every device would look ahead (docs/design/2026-11-04-no-rtc.md)
	}
	h.clocks.mu.Lock()
	defer h.clocks.mu.Unlock()
	var out []ClientClock
	for _, id := range slices.Sorted(maps.Keys(h.clocks.by)) {
		if c := h.clocks.by[id]; c.Ahead > ClientClockAhead {
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b ClientClock) int { return cmp.Compare(b.Ahead, a.Ahead) })
	return out
}
