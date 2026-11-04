package core

import (
	"sync"
	"time"
)

// MetaCentralDateSeen is the newest time central's answers have carried (their Date
// header), stored so that it survives a restart without network.
const MetaCentralDateSeen = "central_date_seen"

// clockBehindMargin is how far the hub's clock may be behind the newest time central
// has shown it before the hub stops trusting its clock.
const clockBehindMargin = 10 * time.Minute

// centralSeen keeps the newest time central has shown, and when it was last stored.
type centralSeen struct {
	mu     sync.Mutex
	at     time.Time
	stored time.Time
	loaded bool
}

// noteCentralDate keeps the newest time central has shown. It is stored at most every
// ten minutes: a correct clock is never behind a time from the past, so a stale stored
// value only weakens the bound by those minutes.
func (h *Hub) noteCentralDate(t time.Time) {
	h.seen.mu.Lock()
	defer h.seen.mu.Unlock()
	h.loadSeen()
	if !t.After(h.seen.at) {
		return
	}
	h.seen.at = t
	if t.Sub(h.seen.stored) >= clockBehindMargin && h.Store != nil && h.Store.Unlocked() {
		h.Store.SetMeta(MetaCentralDateSeen, t.UTC().Format(time.RFC3339))
		h.seen.stored = t
	}
}

func (h *Hub) loadSeen() {
	if h.seen.loaded || h.Store == nil || !h.Store.Unlocked() {
		return
	}
	h.seen.loaded = true
	if t, err := time.Parse(time.RFC3339, h.Store.Meta(MetaCentralDateSeen)); err == nil && t.After(h.seen.at) {
		h.seen.at, h.seen.stored = t, t
	}
}

// ClockBehind reports how far the hub's clock is behind the newest time central has
// shown it, when by more than ten minutes, and zero otherwise. A Pi without an RTC
// starts at its image's date after a power cut with no network (docs/NO-RTC.md);
// checks that judge "today" by the hub's clock then let central decide at push.
func (h *Hub) ClockBehind() time.Duration {
	h.seen.mu.Lock()
	defer h.seen.mu.Unlock()
	h.loadSeen()
	if d := h.seen.at.Sub(time.Now()); d > clockBehindMargin {
		return d
	}
	return 0
}
