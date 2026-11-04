package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// F4: Android stamps its records with the device's clock (createdAtClient,
// updatedAtClient). The hub's page warns about each user whose device ran ahead of
// central's clock, in central's time zone, at their latest upload, and the heartbeat
// counts them. A device a day ahead also dates its enrollment in central's future,
// which the hub refuses offline, as central does (E1020).
func TestDeviceClockAheadWarns(t *testing.T) {
	e := newEnv(t)
	e.central.TimeZone = "Africa/Kampala" // UTC+3: device times read in central's zone
	e.setup()
	right, hour, day := e.android("nurse_a"), e.android("nurse_b"), e.android("hub_admin")
	for _, c := range []*client{right, hour, day} {
		c.json("GET", "/api/me", nil, 200)
	}
	e.sync() // the hub learns central's zone
	kampala, _ := time.LoadLocation(e.central.TimeZone)
	n := 0
	// upload sends one registration from a device whose clock is off by ahead, saved two
	// minutes before it is sent. A device a day ahead dates it on its own today; the
	// others on central's, so that the test does not depend on the hour it runs.
	upload := func(c *client, ahead time.Duration, want int) map[string]any {
		t.Helper()
		n++
		uid := func(p string) string { return p + strings.Repeat("0", 10-len(p)) + string(rune('a'+n)) }
		device := time.Now().In(kampala).Add(ahead - 2*time.Minute)
		p := newPatient(uid("ClkTe"), uid("ClkEn"), "Clock", "", nil)
		te := p["trackedEntities"].([]any)[0].(map[string]any)
		enr := te["enrollments"].([]any)[0].(map[string]any)
		stamp := device.Format("2006-01-02T15:04:05.000")
		for _, o := range []map[string]any{te, enr} {
			o["createdAtClient"], o["updatedAtClient"] = stamp, stamp
		}
		date := time.Now().In(kampala)
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
// no network starts at its image's date, docs/NO-RTC.md) would refuse today's
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
