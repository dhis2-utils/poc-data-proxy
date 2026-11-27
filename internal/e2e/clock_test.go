package e2e

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// F4: Android stamps its records with the device's clock (createdAtClient,
// updatedAtClient). The hub's page warns about each user whose device ran ahead of
// central's clock, in central's time zone, at their latest upload, and the heartbeat
// counts them. A device a day ahead also dates its enrollment in central's future,
// which the hub refuses offline, as central does (E1020).
func TestDeviceClockAheadWarns(t *testing.T) {
	e := newEnv(t)
	e.central.TimeZone = "Africa/Nairobi" // UTC+3: device times read in central's zone
	e.setup()
	right, hour, day := e.android("nurse_a"), e.android("nurse_b"), e.android("hub_admin")
	for _, c := range []*client{right, hour, day} {
		c.json("GET", "/api/me", nil, 200)
	}
	e.sync() // the hub learns central's zone
	eat, _ := time.LoadLocation(e.central.TimeZone)
	n := 0
	// upload sends one registration from a device whose clock is off by ahead, saved two
	// minutes before it is sent. A device a day ahead dates it on its own today; the
	// others on central's, so that the test does not depend on the hour it runs.
	upload := func(c *client, ahead time.Duration, want int) map[string]any {
		t.Helper()
		n++
		uid := func(p string) string { return p + strings.Repeat("0", 10-len(p)) + string(rune('a'+n)) }
		device := time.Now().In(eat).Add(ahead - 2*time.Minute)
		p := newPatient(uid("ClkTe"), uid("ClkEn"), "Clock", "", nil)
		te := p["trackedEntities"].([]any)[0].(map[string]any)
		enr := te["enrollments"].([]any)[0].(map[string]any)
		stamp := device.Format("2006-01-02T15:04:05.000")
		for _, o := range []map[string]any{te, enr} {
			o["createdAtClient"], o["updatedAtClient"] = stamp, stamp
		}
		date := time.Now().In(eat)
		if ahead >= 24*time.Hour {
			date = device
		}
		enr["enrolledAt"], enr["occurredAt"] = date.Format(time.DateOnly), date.Format(time.DateOnly)
		return c.json("POST", postSync, p, want)
	}
	clocks := func() string {
		t.Helper()
		admin := e.web("nurse_a")
		admin.login()
		_, page := admin.do("GET", "/hub/ui", nil)
		card, ok := strings.CutPrefix(string(page)[max(0, strings.Index(string(page), "Device clocks ahead")):], "Device clocks ahead")
		if !ok {
			return ""
		}
		return card[:strings.Index(card, "</div>")]
	}

	e.offline()
	upload(right, 0, 200)
	// An hour ahead, as a tablet in UTC+4 against central in UTC+3: its records
	// arrive a little less than an hour ahead.
	upload(hour, time.Hour, 200)
	// A day ahead (F4): the device's today is central's tomorrow.
	rep := upload(day, 24*time.Hour, 409)
	if codes := errorCodes(rep); !slices.Contains(codes, "E1020") {
		t.Errorf("a day-ahead device's enrollment: codes %v, want E1020 offline", codes)
	}
	card := clocks()
	t.Logf("the hub's page: %s", card)
	for _, want := range []string{"<td>nurse_b</td><td>1 h</td>", "<td>hub_admin</td><td>24 h</td>"} {
		if !strings.Contains(card, want) {
			t.Errorf("the hub's page lacks %q in its clock warning", want)
		}
	}
	if strings.Contains(card, "nurse_a") {
		t.Error("the hub's page warns about a device with the right time")
	}

	e.online()
	e.sync()
	if got := e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["clientClocksAhead"]; got != float64(2) {
		t.Errorf("heartbeat clientClocksAhead %v, want 2", got)
	}

	// The tablet's clock is set right: its next upload, online, clears its warning.
	upload(hour, 0, 200)
	if card := clocks(); strings.Contains(card, "nurse_b") || !strings.Contains(card, "hub_admin") {
		t.Errorf("after nurse_b's clock was set right: %s", card)
	}
}

// A hub whose clock is behind central's (a Pi 4 without an RTC after a power cut with
// no network starts at its image's date, docs/design/2026-11-04-no-rtc.md) would refuse today's
// registrations as future dates and list every device as ahead. Once central has shown
// it a later time, the hub leaves the dates to central and says so on its page.
func TestHubClockBehindLeavesDatesToCentral(t *testing.T) {
	e := newEnv(t)
	e.central.ClockSkew = 3 * 24 * time.Hour // central three days ahead of the hub
	e.setup()
	tab := e.android("nurse_a")
	tab.json("GET", "/api/me", nil, 200)
	e.sync() // central's Date headers reach the hub
	if d := e.app.Hub.ClockBehind(); d < 71*time.Hour {
		t.Fatalf("ClockBehind %s, want about 72 h", d)
	}
	e.offline()
	// Central's today, the hub's day after tomorrow: the right date, by central's clock.
	today := time.Now().Add(e.central.ClockSkew).In(time.UTC)
	stamp := today.Add(-2 * time.Minute).Format("2006-01-02T15:04:05.000")
	p := newPatient("ClkBehTe001", "ClkBehEn001", "Behind", "", nil)
	te := p["trackedEntities"].([]any)[0].(map[string]any)
	enr := te["enrollments"].([]any)[0].(map[string]any)
	te["createdAtClient"], te["updatedAtClient"] = stamp, stamp
	enr["enrolledAt"], enr["occurredAt"] = today.Format(time.DateOnly), today.Format(time.DateOnly)
	tab.json("POST", postSync, p, 200)

	admin := e.web("nurse_a")
	admin.login()
	_, page := admin.do("GET", "/hub/ui", nil)
	if !strings.Contains(string(page), "The hub&#39;s clock is behind") && !strings.Contains(string(page), "The hub's clock is behind") {
		t.Error("the hub's page does not say its clock is behind")
	}
	if strings.Contains(string(page), "Device clocks ahead") {
		t.Error("the hub's page lists a device as ahead while its own clock is behind")
	}
}

// A hub whose clock is ahead of central's (a Windows PC set ahead by hand) stamps
// what is saved through it online later than central does. The pull used to replace
// a held record only when central's updatedAt was newer, so such a copy stayed as the
// hub stored it, missing later changes on central (review R-02). The pull now
// refreshes on any difference, and brings central's exact times; a local save still
// waiting to be pushed is kept. The hub's page says its clock is ahead.
func TestHubClockAheadPullRefreshes(t *testing.T) {
	e := newEnv(t)
	e.central.ClockSkew = -20 * time.Minute // the hub twenty minutes ahead of central
	centralNow := func() string { return time.Now().Add(e.central.ClockSkew).UTC().Format("2006-01-02T15:04:05.000") }
	for _, uid := range e.central.Order() {
		e.central.Backdate(uid, centralNow()) // seeded before the skew was set
	}
	e.setup()
	tab := e.android("nurse_a")
	tab.json("GET", "/api/me", nil, 200)
	e.sync()
	h := e.app.Hub

	// Registered online and pulled, then changed online: the hub's copy carries the
	// hub's time, central's its own.
	tab.json("POST", postSync, newPatient("AheadTe0001", "AheadEn0001", "Ahead", "", nil), 200)
	e.sync()
	tab.json("POST", postSync, newPatient("AheadTe0001", "AheadEn0001", "Ahead2", "", nil), 200)
	te := h.Index.Get("AheadTe0001")
	if te == nil {
		t.Fatal("the online save is not stored")
	}
	if at, err := time.Parse("2006-01-02T15:04:05.000", te.UpdatedAt); err != nil || time.Since(at).Abs() > time.Minute {
		t.Errorf("hub's copy updatedAt %q, want the hub's time %q", te.UpdatedAt, h.CentralTime(time.Now()))
	}

	// Changed on central, at central's time: still before the hub's stamp.
	e.central.SetStatus("AheadEn0001", "COMPLETED")
	e.central.Backdate("AheadEn0001", centralNow())
	e.sync()
	for _, uid := range []string{"AheadTe0001", "AheadEn0001"} {
		o, want := h.Index.Get(uid), tracker.Str(e.central.Object(uid), "updatedAt")
		if o == nil || o.UpdatedAt != want {
			t.Errorf("%s after the pull: %+v, want central's updatedAt %q", uid, o, want)
		}
	}
	if o := h.Index.Get("AheadEn0001"); o == nil || tracker.Str(h.Index.Full(o), "status") != "COMPLETED" {
		t.Error("the pull did not bring central's change of the enrollment")
	}

	// A local save waiting to be pushed is not replaced by central's other version.
	e.offline()
	tab.json("POST", postSync, newPatient("AheadTe0001", "AheadEn0001", "Ahead3", "", nil), 200)
	e.central.SetStatus("AheadEn0001", "CANCELLED")
	e.central.Backdate("AheadEn0001", centralNow())
	e.online()
	if err := e.app.Worker.Pull(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o := h.Index.Get("AheadEn0001"); o == nil || o.SyncState != store.SyncPending || tracker.Str(h.Index.Full(o), "status") != "ACTIVE" {
		t.Errorf("pending local save after a pull: %+v, want kept pending and ACTIVE", o)
	}

	admin := e.web("nurse_a")
	admin.login()
	_, page := admin.do("GET", "/hub/ui", nil)
	if !strings.Contains(string(page), "clock is ahead") {
		t.Error("the hub's page does not say its clock is ahead")
	}
}
