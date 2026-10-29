package adversarial

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregate"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Aggregate forms while central's rules for them change or a period's time passes
// (docs/ADVERSARIAL-TESTING.md, F5 and J). Central's clock cannot be moved here (the
// instance runs on the host's clock; the broker has no control of it), so a month's
// boundary cannot be reached at will: data input periods, whose opening and closing
// are moments, stand in for it.

// dhis2Time is t as central's wall clock writes it.
func dhis2Time(t time.Time) string { return aggregate.Naive(t).Format("2006-01-02T15:04:05.000") }

// hubLock is a form's lock status through the hub, as the Data Entry app reads it
// ("" and the status when the hub does not answer 200), and whether the cell holds v.
func (a *aggScene) hubLock(c *tk.Client, ds, pe, de, v string) (string, int, bool) {
	a.t.Helper()
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {a.M.Facility.ID}}
	st, b := c.Do("GET", "/api/"+a.App.Hub.APIMinor()+"/dataEntry/dataValues?"+q.Encode(), nil)
	if st != 200 {
		return fmt.Sprintf("%.100s", b), st, false
	}
	var form map[string]any
	json.Unmarshal(b, &form)
	got, _ := formValue(form, de, a.ag.COC)
	return fmt.Sprint(form["lockStatus"]), st, v != "" && got == v
}

// centralLock is the same form's lock status on central, for the same user.
func (a *aggScene) centralLock(c *tk.Client, ds, pe string) (string, int) {
	a.t.Helper()
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {a.M.Facility.ID}}
	st, b, err := a.Central.As(c.User, a.M.Password).Do("GET", "/api/dataEntry/dataValues?"+q.Encode(), nil)
	if err != nil || st != 200 {
		return fmt.Sprintf("%.150s", b), st
	}
	var form map[string]any
	json.Unmarshal(b, &form)
	return fmt.Sprint(form["lockStatus"]), st
}

// centralTakes reports whether central takes a value in the form from this user now,
// and its answer: a value is written to a scratch cell (the integer) and deleted
// again. Central's lockStatus does not say: 2.42.7 answers OPEN for a period its data
// input periods have closed, and refuses the write (E2025).
func (a *aggScene) centralTakes(c *tk.Client, ds, pe string) (bool, string) {
	a.t.Helper()
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {a.M.Facility.ID}, "de": {a.ag.Integer}, "co": {a.ag.COC}, "value": {"5"}}
	st, b, err := a.Central.As(c.User, a.M.Password).Do("POST", "/api/dataValues?"+q.Encode(), nil)
	if err != nil {
		a.t.Fatal(err)
	}
	if st/100 == 2 {
		a.DeleteCentralDataValue(ds, pe, a.ag.Integer, a.ag.COC)
	}
	return st/100 == 2, fmt.Sprintf("%d %.120s", st, b)
}

// sameLock compares, for each form, whether the hub takes values offline (its lock
// status through the hub) with whether central takes them from the same user at this
// moment, and reports each difference (the hub must lock and open as central does).
func (a *aggScene) sameLock(c *tk.Client, when, ds string, pes ...string) (mismatches []string) {
	a.t.Helper()
	for _, pe := range pes {
		hl, hst, _ := a.hubLock(c, ds, pe, "", "")
		cl, _ := a.centralLock(c, ds, pe)
		takes, answer := a.centralTakes(c, ds, pe)
		a.t.Logf("%s: form %s %s: hub (offline) %d %s; central's lockStatus %s, a write %s", when, ds, pe, hst, hl, cl, answer)
		if hubOpen := hst == 200 && hl == aggregate.Open; hubOpen != takes {
			mismatches = append(mismatches, fmt.Sprintf("%s %s %s: hub %d %s, central takes a value: %v", when, ds, pe, hst, hl, takes))
		}
	}
	return
}

// try posts one cell through the hub and returns the status and body.
func (a *aggScene) try(c *tk.Client, ds, pe, de, coc, v string) (int, string) {
	a.t.Helper()
	st, b := c.PostDataValue(a.values, map[string]string{"ds": ds, "pe": pe, "ou": a.M.Facility.ID, "de": de, "co": coc, "value": v})
	a.t.Logf("%s enters %s=%s in %s %s (online=%v): hub answers %d %.200s", c.User, de, v, ds, pe, !a.Net.Down(), st, b)
	return st, string(b)
}

// F5: a period's input closes, and another's opens, while the hub is offline. The
// data set gets data input periods: the current month's closes, and last month's
// opens, two and a half minutes after the hub last synced. Before and after that
// moment the hub's offline answer for each form is compared with central's own (as
// the nurse, at the same moment), and the nurse enters a value in each. Must hold:
// forms lock and open as central would; a value entered while the form was open is
// pushed or refused as central decides on push, and kept if refused.
func TestF5InputPeriodPassesWhileOffline(t *testing.T) {
	a := beginAggregate(t)
	ds, de := a.ag.DataSet, a.ag.Number
	nurse := a.Web(tk.RoleNurse)
	nurse.Login()
	for _, pe := range []string{a.pe, a.old} {
		a.DeleteCentralDataValue(ds, pe, de, a.ag.COC)
	}
	now := a.App.Hub.CentralNow()
	moment := now.Add(150 * time.Second)
	a.SetDataInputPeriods(ds,
		tk.InputPeriod{Period: a.pe, Opening: dhis2Time(now.Add(-24 * time.Hour)), Closing: dhis2Time(moment)},
		tk.InputPeriod{Period: a.old, Opening: dhis2Time(moment), Closing: dhis2Time(now.Add(24 * time.Hour))})
	t.Logf("%s closes and %s opens at %s (central's clock)", a.pe, a.old, dhis2Time(moment))
	nurse.Login() // the access refresh at login reads the input periods
	a.Sync()

	a.Offline()
	var mismatches []string
	mismatches = append(mismatches, a.sameLock(nurse, "before", ds, a.pe, a.old)...)
	stBefore, _ := a.try(nurse, ds, a.pe, de, a.ag.COC, "51")
	if st, _ := a.try(nurse, ds, a.old, de, a.ag.COC, "52"); st/100 == 2 {
		mismatches = append(mismatches, "before: the hub took a value for "+a.old+", not yet open")
	}
	time.Sleep(time.Until(moment.Add(10 * time.Second)))
	mismatches = append(mismatches, a.sameLock(nurse, "after", ds, a.pe, a.old)...)
	if l, st, shows := a.hubLock(nurse, ds, a.pe, de, "51"); st == 200 {
		t.Logf("after: the closed form %s through the hub: %s, shows the value entered before it closed: %v", a.pe, l, shows)
	}
	if st, _ := a.try(nurse, ds, a.pe, de, a.ag.COC, "53"); st/100 == 2 {
		mismatches = append(mismatches, "after: the hub took a value for "+a.pe+", closed")
	}
	stOpened, _ := a.try(nurse, ds, a.old, de, a.ag.COC, "54")

	a.noRepeats(a.reconnectAggregate(3))
	issues := a.issues()
	vPe, byPe := a.onCentral(a.pe, de)
	vOld, byOld := a.onCentral(a.old, de)
	t.Logf("on central: %s=%q by %q, %s=%q by %q; refusals kept: %v", a.pe, vPe, byPe, a.old, vOld, byOld, issues)
	if stBefore/100 == 2 && vPe != "51" && !anyContains(issues, `"value":"51"`) {
		t.Errorf("I1: the value 51 the hub confirmed before %s closed is neither on central nor kept", a.pe)
	}
	if stOpened/100 == 2 && vOld != "54" && !anyContains(issues, `"value":"54"`) {
		t.Errorf("I1: the value 54 the hub confirmed after %s opened is neither on central nor kept", a.old)
	}
	if len(mismatches) > 0 {
		finding(t, "F5: the hub opens and closes a form at its data input period's exact time; central (2.42.7) by the day "+
			"(it takes values all of the closing day, and from the start of the opening day): on those days the hub refuses "+
			"offline what central takes")
		t.Errorf("forms lock and open unlike central: %v", mismatches)
	}
}

func anyContains(l []string, s string) bool {
	for _, x := range l {
		if strings.Contains(x, s) {
			return true
		}
	}
	return false
}

// J7: data input periods are set on central while a value waits offline: the current
// month closed on an earlier day (central judges input periods by the day, F5), last
// month open. Must hold: the waiting value pushed or
// refused as central decides, and kept if refused; after the next sync the hub locks
// and opens the forms as central does.
func TestJ7InputPeriodsChangedOnCentral(t *testing.T) {
	a := beginAggregate(t)
	ds, de := a.ag.DataSet, a.ag.Number
	nurse := a.Web(tk.RoleNurse)
	nurse.Login()
	for _, pe := range []string{a.pe, a.old} {
		a.DeleteCentralDataValue(ds, pe, de, a.ag.COC)
	}
	a.Sync()
	a.Offline()
	a.enter(nurse, a.pe, de, "61")
	a.enter(nurse, a.old, de, "62")
	now := a.App.Hub.CentralNow()
	a.SetDataInputPeriods(ds,
		tk.InputPeriod{Period: a.pe, Opening: dhis2Time(now.Add(-10 * 24 * time.Hour)), Closing: dhis2Time(now.Add(-26 * time.Hour))},
		tk.InputPeriod{Period: a.old, Opening: dhis2Time(now.Add(-10 * 24 * time.Hour)), Closing: dhis2Time(now.Add(10 * 24 * time.Hour))})
	a.noRepeats(a.reconnectAggregate(3))
	issues := a.issues()
	vPe, _ := a.onCentral(a.pe, de)
	vOld, byOld := a.onCentral(a.old, de)
	t.Logf("on central: %s=%q, %s=%q by %q; refusals kept: %v", a.pe, vPe, a.old, vOld, byOld, issues)
	if vOld != "62" || byOld != nurse.User {
		t.Errorf("I1: the open period's value on central: %q by %q, want 62 by %s", vOld, byOld, nurse.User)
	}
	if vPe != "61" && !anyContains(issues, `"value":"61"`) {
		t.Errorf("I1: the closed period's value 61 is neither on central nor kept: %v", issues)
	}
	a.Offline()
	mismatches := a.sameLock(nurse, "after the sync", ds, a.pe, a.old)
	if st, _ := a.try(nurse, ds, a.pe, de, a.ag.COC, "63"); st/100 == 2 {
		mismatches = append(mismatches, "the hub takes a value for the closed period after the sync")
	}
	if len(mismatches) > 0 {
		t.Errorf("not followed at the next sync: %v", mismatches)
	}
}

// J6: expiry days are set on central while last month's value waits offline: one day,
// so last month is locked (central's expiry) while the current month stays open. The
// boundary itself, a period passing while offline, is F5's. Must hold: the waiting
// value refused and kept (or pushed, for a user central lets edit expired periods);
// after the next sync the hub locks the period as central does.
func TestJ6ExpiryDaysWhileOffline(t *testing.T) {
	a := beginAggregate(t)
	ds, de := a.ag.DataSet, a.ag.Number
	nurse := a.Web(tk.RoleNurse)
	nurse.Login()
	for _, pe := range []string{a.pe, a.old} {
		a.DeleteCentralDataValue(ds, pe, de, a.ag.COC)
	}
	a.Sync()
	u, _ := a.App.Hub.Store.UserByName(nurse.User)
	t.Logf("the nurse may edit expired periods (F_EDIT_EXPIRED or ALL): %v", a.App.Hub.AggAccess(u.ID).Bypass)
	a.Offline()
	a.enter(nurse, a.old, de, "71")
	a.enter(nurse, a.pe, de, "72")
	a.SetDataSetField(ds, "expiryDays", 1)
	a.noRepeats(a.reconnectAggregate(3))
	issues := a.issues()
	vOld, _ := a.onCentral(a.old, de)
	vPe, byPe := a.onCentral(a.pe, de)
	t.Logf("on central: %s=%q, %s=%q by %q; refusals kept: %v", a.old, vOld, a.pe, vPe, byPe, issues)
	if vPe != "72" || byPe != nurse.User {
		t.Errorf("I1: the open period's value on central: %q by %q, want 72 by %s", vPe, byPe, nurse.User)
	}
	if vOld != "71" && !anyContains(issues, `"value":"71"`) {
		t.Errorf("I1: the expired period's value 71 is neither on central nor kept: %v", issues)
	}
	a.Offline()
	mismatches := a.sameLock(nurse, "after the sync", ds, a.old, a.pe)
	st, _ := a.try(nurse, ds, a.old, de, a.ag.COC, "73")
	if takes, answer := a.centralTakes(nurse, ds, a.old); (st/100 == 2) != takes {
		mismatches = append(mismatches, fmt.Sprintf("a new value for the expired period offline: hub %d, central %s", st, answer))
	}
	if len(mismatches) > 0 {
		t.Errorf("expiry not followed at the next sync: %v", mismatches)
	}
}

// J2: the data set is removed from the facility on central while a value waits
// offline. Must hold: as central decides on push (refused and kept, or stored); after
// the sync the hub serves the form no more than central does (I3).
func TestJ2DataSetRemovedFromFacility(t *testing.T) {
	a := beginAggregate(t)
	ds, de := a.ag.DataSet, a.ag.Integer
	nurse := a.Web(tk.RoleNurse)
	nurse.Login()
	a.DeleteCentralDataValue(ds, a.pe, de, a.ag.COC)
	a.Sync()
	a.Offline()
	a.enter(nurse, a.pe, de, "81")
	a.RemoveDataSetFromOrgUnit(ds, a.M.Facility.ID)
	a.noRepeats(a.reconnectAggregate(3))
	v, by := a.onCentral(a.pe, de)
	issues := a.issues()
	t.Logf("as central decided: the value on central %q by %q; refusals kept: %v", v, by, issues)
	if v != "81" && !anyContains(issues, `"value":"81"`) {
		t.Errorf("I1: the value 81 is neither on central nor kept: %v", issues)
	}
	for _, down := range []bool{false, true} {
		if down {
			a.Offline()
		}
		hl, hst, shows := a.hubLock(nurse, ds, a.pe, de, "81")
		cl, cst := a.centralLock(nurse, ds, a.pe)
		t.Logf("the form after the sync (online=%v): hub %d %s (shows the value: %v); central %d %s", !down, hst, hl, shows, cst, cl)
		if down && hst == 200 && cst != 200 {
			t.Errorf("I3: offline the hub serves a form central refuses the nurse (%d %s)", cst, cl)
		}
		if down {
			if st, _ := a.try(nurse, ds, a.pe, de, a.ag.COC, "82"); st/100 == 2 {
				t.Errorf("the hub takes a value offline for a data set no longer at the facility")
			}
		}
	}
}

// J4: a form is completed offline and fails validation on central. First the plain
// case: the data set requires valid data to complete (validCompleteOnly), so the hub
// refuses the completion offline at once ("needs a connection"). Then the rule changes
// under a waiting completion: the data set did not require it when the form was
// completed offline, and does when the completion reaches central, with a value that
// breaks a rule (number ≤ integer). Must hold: refused and kept, shown; the values
// themselves reach central.
func TestJ4CompletionFailsValidationOnline(t *testing.T) {
	a := beginAggregate(t)
	ds := a.ag.ValidDataSet
	if ds == "" {
		t.Skip("no data set with validCompleteOnly in this setup")
	}
	nurse := a.Web(tk.RoleNurse)
	nurse.Login()
	rule := tk.UID()
	if err := a.Central.JSON("POST", "/api/validationRules", map[string]any{"id": rule, "name": "Adversarial J4 number le integer " + rule,
		"importance": "HIGH", "operator": "less_than_or_equal_to", "periodType": "Monthly",
		"leftSide":  map[string]any{"expression": "#{" + a.ag.Number + "}", "missingValueStrategy": "SKIP_IF_ANY_VALUE_MISSING"},
		"rightSide": map[string]any{"expression": "#{" + a.ag.Integer + "}", "missingValueStrategy": "SKIP_IF_ANY_VALUE_MISSING"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Central.Do("DELETE", "/api/validationRules/"+rule, nil) })
	for _, de := range []string{a.ag.Number, a.ag.Integer, a.ag.Required} {
		a.DeleteCentralDataValue(ds, a.pe, de, a.ag.COC)
	}
	uncomplete := func() {
		q := url.Values{"ds": {ds}, "pe": {a.pe}, "ou": {a.M.Facility.ID}}
		a.Central.Do("DELETE", "/api/completeDataSetRegistrations?"+q.Encode(), nil)
	}
	uncomplete()
	t.Cleanup(uncomplete)
	complete := func() (int, []byte) {
		return nurse.Do("POST", "/api/"+a.App.Hub.APIMinor()+"/dataEntry/dataSetCompletion",
			map[string]any{"dataSet": ds, "period": a.pe, "orgUnit": a.M.Facility.ID, "completed": true})
	}
	a.Sync()

	a.Offline()
	st, b := complete()
	t.Logf("completing %s offline (validCompleteOnly): %d %.200s", ds, st, b)
	if st/100 == 2 {
		t.Errorf("the hub completed offline a form central completes only when valid")
	}

	a.Online()
	a.SetDataSetField(ds, "validCompleteOnly", false)
	nurse.Login()
	a.Sync()
	a.Offline()
	a.try(nurse, ds, a.pe, a.ag.Number, a.ag.COC, "9")
	a.try(nurse, ds, a.pe, a.ag.Integer, a.ag.COC, "1")
	st, b = complete()
	t.Logf("completing %s offline, the rule broken, validCompleteOnly off as the hub last saw it: %d %.200s", ds, st, b)
	if st/100 != 2 {
		t.Fatalf("the hub refused the completion offline")
	}
	a.putValidCompleteOnly(ds, true)
	a.Online()
	a.Net.Calls()
	var pushes []string
	for range 3 {
		a.Sync()
		var n []string
		for _, c := range a.Net.Calls() {
			if c.Method != "GET" && (strings.Contains(c.URL.Path, "dataValues") || strings.Contains(c.URL.Path, "omplet")) {
				n = append(n, fmt.Sprintf("%s %s %d", c.Method, c.URL.Path, c.Status))
			}
		}
		pushes = append(pushes, fmt.Sprint(n))
	}
	t.Logf("aggregate pushes per cycle: %v", pushes)
	issues := a.issues()
	var regs struct {
		Registrations []struct {
			Completed bool `json:"completed"`
		} `json:"completeDataSetRegistrations"`
	}
	q := url.Values{"dataSet": {ds}, "orgUnit": {a.M.Facility.ID}, "period": {a.pe}}
	a.Central.JSON("GET", "/api/completeDataSetRegistrations?"+q.Encode(), nil, &regs)
	vn, _ := a.onCentral(a.pe, a.ag.Number)
	vi, _ := a.onCentral(a.pe, a.ag.Integer)
	t.Logf("on central: number %q, integer %q, completion registrations %+v; refusals kept: %v", vn, vi, regs.Registrations, issues)
	if vn != "9" || vi != "1" {
		t.Errorf("I1: the values did not reach central: %q %q", vn, vi)
	}
	completed := len(regs.Registrations) > 0 && regs.Registrations[0].Completed
	if !completed && len(issues) == 0 {
		t.Errorf("I1: the completion is neither on central nor kept as a refusal")
	}
	if completed {
		finding(t, "J4: a completion queued offline while the data set did not require valid data is pushed after it does; "+
			"central's completion API does not run validation (2.42.7), so the form is completed with a rule broken")
		t.Errorf("I4: central completed the form with a broken rule; the hub pushed the completion")
	}
	if len(pushes) > 1 && pushes[1] != "[]" && pushes[2] != "[]" {
		t.Errorf("I5/I7: the completion is sent again and again: %v", pushes)
	}
}

// putValidCompleteOnly sets the field without a second cleanup (SetDataSetField's
// restores what was there first).
func (a *aggScene) putValidCompleteOnly(ds string, v bool) {
	a.t.Helper()
	var o map[string]any
	if err := a.Central.JSON("GET", "/api/dataSets/"+ds+"?fields=:owner", nil, &o); err != nil {
		a.t.Fatal(err)
	}
	o["validCompleteOnly"] = v
	if err := a.Central.JSON("PUT", "/api/dataSets/"+ds, o, nil); err != nil {
		a.t.Fatal(err)
	}
}

// J5: a category option combination is removed while a value for it waits offline:
// the data element gets a category combination of its own (sex), so the default
// option combination the value was entered for is no longer the element's. Must hold:
// refused and kept (or as central decides); after the sync the hub refuses the old
// combination offline as central does.
func TestJ5OptionComboRemoved(t *testing.T) {
	a := beginAggregate(t)
	ds, de := a.ag.DataSet, a.ag.Number
	nurse := a.Web(tk.RoleNurse)
	nurse.Login()
	var sex struct {
		DataSetElements []struct {
			DataElement   struct{ ID string }  `json:"dataElement"`
			CategoryCombo *struct{ ID string } `json:"categoryCombo"`
		} `json:"dataSetElements"`
	}
	if err := a.Central.JSON("GET", "/api/dataSets/"+ds+"?fields=dataSetElements[dataElement[id],categoryCombo[id]]", nil, &sex); err != nil {
		t.Fatal(err)
	}
	combo := ""
	for _, x := range sex.DataSetElements {
		if x.CategoryCombo != nil {
			combo = x.CategoryCombo.ID
		}
	}
	if combo == "" {
		t.Skip("no disaggregated category combination in the data set to move the element to")
	}
	a.DeleteCentralDataValue(ds, a.pe, de, a.ag.COC)
	a.Sync()
	a.Offline()
	a.enter(nurse, a.pe, de, "91")
	a.SetDataElementCombo(de, combo)
	t.Logf("%s now has category combination %s", de, combo)
	a.noRepeats(a.reconnectAggregate(3))
	v, by := a.onCentral(a.pe, de)
	issues := a.issues()
	t.Logf("on central: %q by %q; refusals kept: %v", v, by, issues)
	if v != "91" && !anyContains(issues, `"value":"91"`) {
		t.Errorf("I1: the value 91 is neither on central nor kept: %v", issues)
	}
	// Central's answer to the same write now, as the nurse, and the hub's offline.
	q := url.Values{"ds": {ds}, "pe": {a.pe}, "ou": {a.M.Facility.ID}, "de": {de}, "co": {a.ag.COC}, "value": {"93"}}
	cst, cb, _ := a.Central.As(nurse.User, a.M.Password).Do("POST", "/api/dataValues?"+q.Encode(), nil)
	t.Logf("central to the nurse, the old combination now: %d %.200s", cst, cb)
	a.Offline()
	hst, _ := a.try(nurse, ds, a.pe, de, a.ag.COC, "92")
	if (hst/100 == 2) != (cst/100 == 2) {
		t.Errorf("after the sync the hub answers the old combination %d, central %d", hst, cst)
	}
	if cst/100 == 2 {
		a.Online()
		a.DeleteCentralDataValue(ds, a.pe, de, a.ag.COC)
	}
}
