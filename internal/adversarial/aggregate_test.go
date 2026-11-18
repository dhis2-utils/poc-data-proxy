package adversarial

import (
	"fmt"
	"math/rand/v2"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregate"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// aggScene is a scene with the manifest's data set, its two latest open periods as the
// nurse sees them on the hub (pe the newer), and the hub's data value path.
type aggScene struct {
	*scene
	ag      *tk.Aggregate
	pe, old string
	peEnd   time.Time // when pe ends: after now if it is the current period
	values  string
}

func beginAggregate(t *testing.T, roles ...string) *aggScene {
	s := begin(t)
	if !s.Live() {
		t.Skip("adversarial scenarios run against a real DHIS2 (HUB_MANIFEST)")
	}
	if s.M.Aggregate == nil {
		t.Skip("this setup has no aggregate data sets")
	}
	for _, role := range append([]string{tk.RoleNurse}, roles...) {
		s.Web(role).Login()
	}
	s.Sync()
	a := &aggScene{scene: s, ag: s.M.Aggregate, values: "/api/" + s.App.Hub.APIMinor() + "/dataValues"}
	u, _ := s.App.Hub.Store.UserByName(s.M.Users[tk.RoleNurse].Username)
	acc := s.App.Hub.AggAccess(u.ID)
	if acc == nil || acc.DataSets[a.ag.DataSet] == nil {
		t.Fatalf("the nurse cannot enter %s on the hub", a.ag.DataSet)
	}
	ds := acc.DataSets[a.ag.DataSet]
	now := s.App.Hub.CentralNow()
	cur, _ := aggregate.Current(ds.PeriodType, now)
	var open []aggregate.Period
	for _, p := range aggregate.Window(ds, now, s.App.Hub.AggRetention().For(ds.ID)) {
		if !p.Start.After(cur.Start) {
			open = append(open, p)
		}
	}
	if len(open) < 2 {
		t.Skipf("%s has fewer than two open periods: %v", a.ag.DataSet, open)
	}
	a.pe, a.old = open[len(open)-1].ID, open[len(open)-2].ID
	a.peEnd = open[len(open)-1].End
	return a
}

// enter posts one cell as the Data Entry app does, and fails unless the hub confirms.
func (a *aggScene) enter(c *tk.Client, pe, de, value string) {
	a.t.Helper()
	st, b := c.PostDataValue(a.values, map[string]string{"ds": a.ag.DataSet, "pe": pe, "ou": a.M.Facility.ID,
		"de": de, "co": a.ag.COC, "value": value})
	a.t.Logf("%s enters %s=%s for %s (online=%v): hub answers %d %.200s", c.User, de, value, pe, !a.Net.Down(), st, b)
	if st/100 != 2 {
		a.t.Fatalf("the hub refused a valid value")
	}
}

// onCentral is a cell's value and author on central ("" if it has none).
func (a *aggScene) onCentral(pe, de string) (string, string) {
	v, by, _ := a.CentralDataValue(a.ag.DataSet, pe, de, a.ag.COC, a.ag.DefaultAOC)
	return v, by
}

// aggPushes counts the aggregate edits the hub sent to central per cycle, as reconnect
// does tracker pushes.
func (a *aggScene) reconnectAggregate(cycles int) []int {
	a.t.Helper()
	a.Online()
	a.Net.Calls()
	var pushes []int
	for range cycles {
		a.Sync()
		n := 0
		for _, c := range a.Net.Calls() {
			if (c.Method == "POST" || c.Method == "DELETE") && c.URL.Path == "/api/dataValues" {
				n++
			}
		}
		pushes = append(pushes, n)
	}
	a.t.Logf("aggregate pushes per cycle: %v", pushes)
	return pushes
}

// issues are the hub's open refusals of aggregate edits, each with whether the
// Facility Hub app lists it (facility-hub-issues on central).
func (a *aggScene) issues() []string {
	a.t.Helper()
	is, err := a.App.Hub.Store.UnresolvedIssues(1000)
	if err != nil {
		a.t.Fatal(err)
	}
	var out []string
	for _, x := range is {
		if x.Kind != "aggregate" {
			continue
		}
		key := fmt.Sprintf("%s-%d", a.App.Hub.HubID(), x.ID)
		st, _, _ := a.Central.Do("GET", "/api/dataStore/facility-hub-issues/"+key, nil)
		out = append(out, fmt.Sprintf("%s (listed on central: %v)", x.Error, st == 200))
	}
	return out
}

// J1: the form is approved on central while values for it wait offline. Must hold:
// refused on push and kept, shown; the other period's value goes through; nothing
// retried every cycle. Central (2.43.2) does not hold to it itself: its API takes a
// value into an approved form (POST /api/dataValues 201 as the nurse, the form
// APPROVED_HERE), while the Data Entry app shows the form locked. The hub pushes the
// offline value into the approved form.
func TestJ1ApprovedWhileOffline(t *testing.T) {
	finding(t, "J1: a value entered offline before central approved the form is pushed into the approved form (central's API accepts it)")
	a := beginAggregate(t)
	nurse := a.Web(tk.RoleNurse)
	nurse.Login()
	for _, pe := range []string{a.pe, a.old} {
		a.DeleteCentralDataValue(a.ag.DataSet, pe, a.ag.Number, a.ag.COC) // earlier runs' values
	}
	a.Sync()

	a.Offline()
	a.enter(nurse, a.pe, a.ag.Number, "41")
	a.enter(nurse, a.old, a.ag.Number, "42")
	a.ApproveDataSet(a.ag.DataSet, a.pe)
	a.noRepeats(a.reconnectAggregate(3))

	if v, by := a.onCentral(a.old, a.ag.Number); v != "42" || by != nurse.User {
		t.Errorf("I1: the other period's value on central: %q by %q, want 42 by %s", v, by, nurse.User)
	}
	v, by := a.onCentral(a.pe, a.ag.Number)
	issues := a.issues()
	t.Logf("the approved form's cell on central: %q by %q; refusals kept on the hub: %v", v, by, issues)
	// I8: what the Data Entry app reads of the form now, and whether the hub takes a new
	// value for it offline.
	for _, down := range []bool{false, true} {
		if down {
			a.Offline()
		}
		form := nurse.JSON("GET", "/api/"+a.App.Hub.APIMinor()+"/dataEntry/dataValues?"+url.Values{"ds": {a.ag.DataSet},
			"pe": {a.pe}, "ou": {a.M.Facility.ID}}.Encode(), nil, 200)
		got, _ := formValue(form, a.ag.Number, a.ag.COC)
		t.Logf("the form through the hub (online=%v): lockStatus %v, the cell %q", !down, form["lockStatus"], got)
	}
	st, b := nurse.PostDataValue(a.values, map[string]string{"ds": a.ag.DataSet, "pe": a.pe, "ou": a.M.Facility.ID,
		"de": a.ag.Number, "co": a.ag.COC, "value": "43"})
	t.Logf("a new value for the approved form offline, after the sync: %d %.200s", st, b)
	if v != "" {
		t.Errorf("I4: central's approved form has the offline value %q by %s", v, by)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], `"value":"41"`) || !strings.Contains(issues[0], "listed on central: true") {
		t.Errorf("I1/I8: want the approved period's value kept as one refusal, listed on central: %v", issues)
	}
}

func formValue(form map[string]any, de, coc string) (string, bool) {
	vs, _ := form["dataValues"].([]any)
	for _, v := range vs {
		m := v.(map[string]any)
		if m["dataElement"] == de && m["categoryOptionCombo"] == coc {
			s, ok := m["value"].(string)
			return s, ok
		}
	}
	return "", false
}

// J3: one cell edited offline by two members of staff, and on central meanwhile. Must
// hold: the last write to reach central wins, as on central, and each write is in
// central's audit with its author. Cell A: the nurse offline, then central, then the
// clerk offline. Cell B: the clerk offline, then central: the hub's push comes after
// central's later write.
func TestJ3SameCellOfflineAndOnCentral(t *testing.T) {
	a := beginAggregate(t, tk.RoleClerk)
	nurse, clerk := a.Web(tk.RoleNurse), a.Web(tk.RoleClerk)
	nurse.Login()
	clerk.Login()
	cellA, cellB := a.ag.Number, a.ag.Integer
	for _, de := range []string{cellA, cellB} {
		a.DeleteCentralDataValue(a.ag.DataSet, a.pe, de, a.ag.COC)
	}
	a.Sync()
	admin := a.M.AdminUser
	base := 100 * (1 + rand.N(90)) // values of this run, told apart from earlier runs' in the audit
	v := func(n int) string { return strconv.Itoa(base + n) }

	a.Offline()
	a.enter(nurse, a.pe, cellA, v(11))
	a.SetCentralDataValue(a.ag.DataSet, a.pe, cellA, a.ag.COC, v(13))
	a.enter(clerk, a.pe, cellA, v(12))
	a.enter(clerk, a.pe, cellB, v(21))
	a.SetCentralDataValue(a.ag.DataSet, a.pe, cellB, a.ag.COC, v(23))
	a.noRepeats(a.reconnectAggregate(3))

	for _, c := range []struct {
		de     string
		writes []cellWrite // in the order they were made
		last   cellWrite   // the last to reach central
	}{
		{cellA, []cellWrite{{nurse.User, v(11)}, {admin, v(13)}, {clerk.User, v(12)}}, cellWrite{clerk.User, v(12)}},
		{cellB, []cellWrite{{clerk.User, v(21)}, {admin, v(23)}}, cellWrite{clerk.User, v(21)}},
	} {
		v, by := a.onCentral(a.pe, c.de)
		t.Logf("%s on central: %q by %s (writes in the order made: %v)", c.de, v, by, c.writes)
		if v != c.last.value || by != c.last.by {
			t.Errorf("%s: central has %q by %q, want the last write to arrive, %v", c.de, v, by, c.last)
		}
		audit := a.audit(a.pe, c.de)
		if !strings.HasPrefix(a.M.DHIS2Version, "2.42") {
			for _, w := range c.writes {
				if !slices.Contains(audit, w) {
					t.Errorf("I4: %s: the write %v is not in central's audit %v", c.de, w, audit)
				}
			}
			continue
		}
		// 2.42 audits an update with the value it replaced (2.43 with the new one;
		// both checked on 17 November 2026): each write left an entry by its author
		// holding one of the cell's values, and each value replaced is audited.
		vals := map[string]bool{}
		for _, w := range c.writes {
			vals[w.value] = true
		}
		for _, w := range c.writes {
			by, replaced := false, w == c.last
			for _, x := range audit {
				by = by || x.by == w.by && vals[x.value]
				replaced = replaced || x.value == w.value
			}
			if !by || !replaced {
				t.Errorf("I4: %s: the write %v is not in central's audit %v", c.de, w, audit)
			}
		}
	}
	if is := a.issues(); len(is) > 0 {
		t.Errorf("refusals: %v", is)
	}
}

// cellWrite is one write of a cell: who wrote which value.
type cellWrite struct{ by, value string }

// audit lists central's latest audit of a cell, oldest first.
func (a *aggScene) audit(pe, de string) []cellWrite {
	a.t.Helper()
	var res struct {
		Audits []struct {
			Created, ModifiedBy, AuditType, Value string
		} `json:"dataValueAudits"`
	}
	// Not by data set: with ds, 2.42 lists every element of the set and ignores de.
	q := url.Values{"pe": {pe}, "ou": {a.M.Facility.ID}, "de": {de}, "co": {a.ag.COC}, "pageSize": {"20"}}
	if err := a.Central.JSON("GET", "/api/audits/dataValue?"+q.Encode(), nil, &res); err != nil {
		a.t.Fatal(err)
	}
	var out []cellWrite
	for i := len(res.Audits) - 1; i >= 0; i-- {
		x := res.Audits[i]
		a.t.Logf("audit %s: %s %s by %s %q", de, x.Created, x.AuditType, x.ModifiedBy, x.Value)
		if x.AuditType != "DELETE" {
			out = append(out, cellWrite{x.ModifiedBy, x.Value})
		}
	}
	return out
}
