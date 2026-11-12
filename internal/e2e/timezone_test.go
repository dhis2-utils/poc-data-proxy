package e2e

import (
	"testing"
	"time"
)

// Central gives and expects times in its own time zone, without an offset. A person
// registered offline is stamped in central's zone, so they sort and compare with
// central's records (a UTC stamp looked three hours old on East Africa Time).
func TestOfflineRecordsUseCentralTimeZone(t *testing.T) {
	e := newEnv(t)
	e.central.TimeZone = "Africa/Kampala" // UTC+3, no daylight saving
	e.setup()
	nurse := e.web("nurse_a")
	nurse.login()
	e.sync() // the hub learns central's zone
	e.offline()
	nurse.json("POST", "/api/tracker?async=false", newPatient("ZoneTe00001", "ZoneEn00001", "Zone", "NAT-7001", nil), 200)
	te := nurse.json("GET", "/api/tracker/trackedEntities/ZoneTe00001?fields=createdAt", nil, 200)
	created, err := time.ParseInLocation("2006-01-02T15:04:05.000", te["createdAt"].(string), time.UTC)
	if err != nil {
		t.Fatalf("createdAt %v: %v", te["createdAt"], err)
	}
	// Read as a wall-clock time, it should be Kampala's now, not UTC's.
	kampala := time.Now().In(time.FixedZone("EAT", 3*3600))
	wall := time.Date(kampala.Year(), kampala.Month(), kampala.Day(), kampala.Hour(), kampala.Minute(), kampala.Second(), 0, time.UTC)
	if d := wall.Sub(created); d < -time.Minute || d > time.Minute {
		t.Fatalf("createdAt %s is %s from central's time now (%s): not stamped in %s", te["createdAt"], d.Round(time.Second),
			wall.Format(time.DateTime), e.central.TimeZone)
	}
}

// On a Pi image the hub hands central's zone to the system (netsetup.SetTimeZone), so
// the 03:00 update reboot falls in the facility's night.
func TestSystemZoneFollowsCentral(t *testing.T) {
	e := newEnv(t)
	e.central.TimeZone = "Asia/Vientiane"
	var got []string
	e.app.Hub.SystemZone = func(name string) { got = append(got, name) }
	e.setup()
	e.sync()
	if len(got) == 0 || got[len(got)-1] != "Asia/Vientiane" {
		t.Fatalf("system zone set to %v, want Asia/Vientiane", got)
	}
}
