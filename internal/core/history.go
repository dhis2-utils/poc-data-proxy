package core

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// The heartbeat's daily history (docs/DEPLOYMENT.md, "Monitoring"): the latest status
// says how a hub is now, this how it has lived, for the last historyDays days.
const (
	historyKey     = "status_history"
	offlineSince   = "offline_since"
	historyDays    = 90
	historyDayForm = time.DateOnly
)

// Day is one day of a hub's history (UTC days).
type Day struct {
	Date  string `json:"date"`
	Syncs int    `json:"syncs"`
	// Outages ended this day, and the longest of them (all of it, also the part
	// before midnight); OfflineMinutes is this day's share of any outage.
	Outages        int    `json:"outages"`
	OfflineMinutes int    `json:"offlineMinutes"`
	LongestOffline int    `json:"longestOfflineMinutes"`
	PendingPeak    int    `json:"pendingPeak"`
	DiskFreeMin    uint64 `json:"diskFreeMinBytes,omitempty"`
}

// History is the hub's daily history, oldest first.
func (h *Hub) History() []Day {
	var days []Day
	if b, err := h.Store.State(historyKey); err == nil && b != nil {
		json.Unmarshal(b, &days)
	}
	return days
}

// updateHistory applies fn to the day of t and saves the last historyDays days.
func (h *Hub) updateHistory(t time.Time, fn func(*Day)) {
	h.histMu.Lock()
	defer h.histMu.Unlock()
	if !h.Store.Unlocked() {
		return
	}
	days := h.History()
	date := t.UTC().Format(historyDayForm)
	i := slices.IndexFunc(days, func(d Day) bool { return d.Date == date })
	if i < 0 {
		days = append(days, Day{Date: date})
		slices.SortFunc(days, func(a, b Day) int { return strings.Compare(a.Date, b.Date) })
		i = slices.IndexFunc(days, func(d Day) bool { return d.Date == date })
	}
	fn(&days[i])
	oldest := time.Now().UTC().AddDate(0, 0, -historyDays+1).Format(historyDayForm)
	days = slices.DeleteFunc(days, func(d Day) bool { return d.Date < oldest })
	b, _ := json.Marshal(days)
	h.Store.SetState(historyKey, b)
}

// NoteCycle records a sync cycle with the pending changes and free disk it found.
func (h *Hub) NoteCycle(pending int, diskFree uint64) {
	h.updateHistory(time.Now(), func(d *Day) {
		d.Syncs++
		d.PendingPeak = max(d.PendingPeak, pending)
		if diskFree > 0 && (d.DiskFreeMin == 0 || diskFree < d.DiskFreeMin) {
			d.DiskFreeMin = diskFree
		}
	})
}

// NoteConnectivity records switches of the uplink: the start of an outage is kept in
// the store, so one spanning a restart still counts.
func (h *Hub) NoteConnectivity(online bool) {
	if !h.Store.Unlocked() {
		return
	}
	if !online {
		h.Store.SetMeta(offlineSince, time.Now().UTC().Format(time.RFC3339Nano))
		return
	}
	since, err := time.Parse(time.RFC3339Nano, h.Store.Meta(offlineSince))
	h.Store.SetMeta(offlineSince, "")
	if err == nil {
		h.NoteOffline(since, time.Now())
	}
}

// NoteOffline records an outage: each day gets its share of minutes; the day it ended
// counts it, with its whole length.
func (h *Hub) NoteOffline(from, to time.Time) {
	from, to = from.UTC(), to.UTC()
	for day := from.Truncate(24 * time.Hour); day.Before(to); day = day.Add(24 * time.Hour) {
		start, end := from, to
		if day.After(start) {
			start = day
		}
		if next := day.Add(24 * time.Hour); next.Before(end) {
			end = next
		}
		if start.After(end) {
			continue
		}
		mins := int(end.Sub(start).Minutes())
		h.updateHistory(day, func(d *Day) { d.OfflineMinutes += mins })
	}
	h.updateHistory(to, func(d *Day) {
		d.Outages++
		d.LongestOffline = max(d.LongestOffline, int(to.Sub(from).Minutes()))
	})
}
