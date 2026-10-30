package adversarial

import (
	"math/rand/v2"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// donorProgram is the manifest's event program with an attribute category
// combination, and the nurse's option and its option combination in it.
func (s *scene) donorProgram() (p tk.Program, option, aoc string) {
	s.t.Helper()
	ag := s.M.Aggregate
	if ag == nil || ag.DonorOptionNurse == "" {
		s.t.Skip("this setup has no category option only the nurse may use")
	}
	for _, x := range s.M.EventPrograms {
		for _, a := range x.AOCs {
			if a == ag.DonorAOCNurse {
				return x, ag.DonorOptionNurse, a
			}
		}
	}
	s.t.Skip("no event program with the nurse's donor option")
	return
}

// withAOC sets the attribute option combination of a payload's events.
func withAOC(payload map[string]any, aoc string) map[string]any {
	for _, ev := range payload["events"].([]any) {
		ev.(map[string]any)["attributeOptionCombo"] = aoc
	}
	return payload
}

// donorCell is a donor data set cell under the nurse's option, as the Data Entry app
// posts it.
func (a *aggScene) donorCell(pe, value string) map[string]string {
	return map[string]string{"ds": a.ag.DonorDataSet, "pe": pe, "ou": a.M.Facility.ID, "de": a.ag.Number, "co": a.ag.COC,
		"cc": a.ag.DonorCC, "cp": a.ag.DonorOptionNurse, "value": value}
}

// A9: a category option of an attribute combination changes while data under it waits
// offline: its end date passes, then its sharing is taken from the nurse's group. Event
// program events and aggregate values. Must hold: refused or accepted as central;
// refusals kept. Measured: what the hub does between the sharing change and its next
// access refresh (hourly; FINDINGS.md said category option sharing waited for the weekly
// full pull).
func TestA9CategoryOptionChanges(t *testing.T) {
	a := beginAggregate(t)
	if a.ag.DonorDataSet == "" {
		t.Skip("no data set with an attribute combination")
	}
	s := a.scene
	p, option, aoc := s.donorProgram()
	els := s.stageElements(p.Stages[0])
	if len(els) == 0 {
		t.Skip("the donor program's stage has no data element")
	}
	de := els[0].ID // the values below are digits: fine for text and numbers
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	web := s.Web(tk.RoleNurse)
	web.Login()
	e0 := tk.UID()
	if st, _ := s.save(nurse, "an event under the option, online", withAOC(s.programEvent(p, e0, map[string]string{de: uniq()}), aoc)); st != 200 {
		t.Fatal("the hub refused a valid event online")
	}
	s.Central.Do("DELETE", "/api/dataValues?"+url.Values{"ds": {a.ag.DonorDataSet}, "pe": {a.pe}, "ou": {s.M.Facility.ID},
		"de": {a.ag.Number}, "co": {a.ag.COC}, "cc": {a.ag.DonorCC}, "cp": {option}}.Encode(), nil) // earlier runs' value
	s.Sync()
	listing := "/api/tracker/events?" + url.Values{"program": {p.ID}, "orgUnit": {s.M.Facility.ID}, "fields": {"event"},
		"paging": {"false"}}.Encode()

	// The end date.
	s.Offline()
	e1 := tk.UID()
	if st, _ := s.save(nurse, "an event under the option, offline", withAOC(s.programEvent(p, e1, map[string]string{de: uniq()}), aoc)); st != 200 {
		t.Fatal("the hub refused a valid event offline")
	}
	if st, b := web.PostDataValue(a.values, a.donorCell(a.pe, "7")); st/100 != 2 {
		t.Fatalf("the hub refused a donor value offline: %d %.300s", st, b)
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	undoEnd := s.SetField("/api/categoryOptions/"+option, "endDate", yesterday)
	s.noRepeats(s.reconnect(3))
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
	v, by, _ := s.CentralDataValue(a.ag.DonorDataSet, a.pe, a.ag.Number, a.ag.COC, aoc)
	t.Logf("end date %s: the donor value on central: %q by %q; aggregate refusals kept: %v", yesterday, v, by, a.issues())
	if v == "" && len(a.issues()) == 0 {
		t.Error("I1: the donor value entered offline is neither on central nor kept as refused")
	}
	s.Offline()
	st, b := s.save(nurse, "an event under the option, after the sync", withAOC(s.programEvent(p, tk.UID(), map[string]string{de: uniq()}), aoc))
	t.Logf("offline after the sync, an event under the ended option: %d %v", st, errorCodes(b))
	st, b = web.PostDataValue(a.values, a.donorCell(a.pe, "8"))
	t.Logf("offline after the sync, a donor value under the ended option: %d %.200s", st, b)
	s.reconnect(2)
	undoEnd()
	s.expect(nil, tk.OnCentral, tk.RefusedKept)

	// The sharing.
	s.Offline()
	e2 := tk.UID()
	if st, _ := s.save(nurse, "an event under the option, offline", withAOC(s.programEvent(p, e2, map[string]string{de: uniq()}), aoc)); st != 200 {
		t.Fatal("the hub refused a valid event offline")
	}
	u := s.M.Users[tk.RoleNurse]
	s.RemoveSharing("categoryOption", option, s.writingGroup(u, "categoryOption", option))
	// Between the change and the hub's next access refresh, offline.
	shown := s.leaks(nurse, listing, "events", "event")
	st, b = s.save(nurse, "an event under the option, before the hub learns", withAOC(s.programEvent(p, tk.UID(), map[string]string{de: uniq()}), aoc))
	t.Logf("before the access refresh, offline: the hub lists %d events central no longer shows the nurse; a new one: %d %v",
		len(shown), st, errorCodes(b))
	s.noRepeats(s.reconnect(3))
	s.expect(map[string]string{e2: tk.RefusedKept}, tk.OnCentral, tk.RefusedKept)
	s.Offline()
	if l := s.leaks(nurse, listing, "events", "event"); len(l) > 0 {
		t.Errorf("I3: after the access refresh the hub lists %d events central does not show the nurse: %v", len(l), l)
	}
	if st, b := s.save(nurse, "an event under the option, after the sync", withAOC(s.programEvent(p, tk.UID(), map[string]string{de: uniq()}), aoc)); st == 200 {
		t.Error("after the access refresh the hub takes an event under an option the nurse may no longer write")
	} else {
		t.Logf("refused by the hub itself: %v", errorCodes(b))
	}
}

// uniq is a value of this run: the ledger's duplicate search on central matches
// events of event programs by their values alone.
func uniq() string { return strconv.Itoa(100000 + rand.IntN(900000)) }

// expiredDate is a date in last month: expired once a monthly expiry of one day applies.
func expiredDate() string {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month()-1, 2, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

// A10: expiry passes while offline. An event program gets expiry days (monthly, one
// day) while an event dated last month waits; the aggregate data set gets expiry days
// while a value for last month waits. Must hold: refused with central's reason and
// kept; the hub's own lock agrees with central's after the sync.
func TestA10ExpiryPassesOffline(t *testing.T) {
	a := beginAggregate(t)
	s := a.scene
	p := s.eventProgram()
	de := s.numberElement(p)
	if time.Now().UTC().Day() < 3 {
		t.Skip("last month ended less than two days ago")
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	web := s.Web(tk.RoleNurse)
	web.Login()
	for _, pe := range []string{a.pe, a.old} {
		a.DeleteCentralDataValue(a.ag.DataSet, pe, a.ag.Number, a.ag.COC)
	}
	s.Sync()

	s.Offline()
	old, cur := tk.UID(), tk.UID()
	oldEv := s.programEvent(p, old, map[string]string{de: uniq()})
	oldEv["events"].([]any)[0].(map[string]any)["occurredAt"] = expiredDate()
	for what, payload := range map[string]map[string]any{"an event dated " + expiredDate(): oldEv,
		"an event dated today": s.programEvent(p, cur, map[string]string{de: uniq()})} {
		if st, _ := s.save(nurse, what, payload); st != 200 {
			t.Fatal("the hub refused a valid event offline")
		}
	}
	a.enter(web, a.old, a.ag.Number, "31")
	a.enter(web, a.pe, a.ag.Number, "32")

	s.SetField("/api/programs/"+p.ID, "expiryPeriodType", "Monthly")
	s.SetField("/api/programs/"+p.ID, "expiryDays", 1)
	s.SetField("/api/dataSets/"+a.ag.DataSet, "expiryDays", 1)
	s.noRepeats(s.reconnect(3))
	lines := s.expect(map[string]string{old: tk.RefusedKept, cur: tk.OnCentral}, tk.OnCentral)
	for _, ln := range lines {
		if ln.UID == old {
			t.Logf("central's reason for the expired event: %s", ln.Detail)
		}
	}
	vOld, _ := a.onCentral(a.old, a.ag.Number)
	vCur, byCur := a.onCentral(a.pe, a.ag.Number)
	issues := a.issues()
	t.Logf("aggregate: last month on central %q, this month %q by %s; refusals kept: %v", vOld, vCur, byCur, issues)
	if vCur != "32" {
		t.Errorf("I1: this month's value is not on central: %q", vCur)
	}
	if vOld != "" || len(issues) != 1 || !strings.Contains(issues[0], `"value":"31"`) {
		t.Errorf("I1: want last month's value refused and kept: central %q, refusals %v", vOld, issues)
	}

	// The hub's own lock after the sync, against central's: aggregate here, events in
	// TestA10HubLockFollowsProgramExpiry.
	s.Offline()
	stEv, bEv := s.save(nurse, "an event dated last month, after the sync", s.programEventOn(p, expiredDate(), map[string]string{de: uniq()}))
	stAgg, bAgg := web.PostDataValue(a.values, map[string]string{"ds": a.ag.DataSet, "pe": a.old, "ou": a.M.Facility.ID,
		"de": a.ag.Number, "co": a.ag.COC, "value": "33"})
	t.Logf("offline after the sync: an expired event %d %v; an expired aggregate value %d %.200s", stEv, errorCodes(bEv), stAgg, bAgg)
	if stAgg/100 == 2 {
		t.Error("the hub's aggregate lock does not follow the data set's expiry days")
	}
}

// The hub reads the program's expiry fields into its catalog: after the sync it
// refuses an event dated in an expired period offline (E1047), as central does at
// push, unless the user may edit expired data (F_EDIT_EXPIRED). Before, it took such
// events offline and central refused them at push, kept as refused; nothing was lost,
// but staff learned of it late.
func TestA10HubLockFollowsProgramExpiry(t *testing.T) {
	s := begin(t)
	p := s.eventProgram()
	de := s.numberElement(p)
	if time.Now().UTC().Day() < 3 {
		t.Skip("last month ended less than two days ago")
	}
	nurse := s.connect(s.M.Users[tk.RoleNurse].Username)
	s.SetField("/api/programs/"+p.ID, "expiryPeriodType", "Monthly")
	s.SetField("/api/programs/"+p.ID, "expiryDays", 1)
	s.Sync()
	s.Offline()
	if st, b := s.save(nurse, "an event dated last month", s.programEventOn(p, expiredDate(), map[string]string{de: uniq()})); st == 200 {
		t.Error("the hub's lock disagrees with central's: an event in an expired period is taken offline")
	} else {
		t.Logf("refused by the hub itself: %v", errorCodes(b))
	}
	s.reconnect(1)
	s.expect(nil, tk.RefusedKept)
}

// programEventOn is programEvent dated on day.
func (s *scene) programEventOn(p tk.Program, day string, values map[string]string) map[string]any {
	pl := s.programEvent(p, tk.UID(), values)
	pl["events"].([]any)[0].(map[string]any)["occurredAt"] = day
	return pl
}
