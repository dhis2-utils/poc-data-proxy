package live

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// aggregateEnv starts a hub set up with the nurse and clerk logged in, or skips when
// this setup has no aggregate data sets.
func aggregateEnv(t *testing.T) (*tk.Env, *tk.Aggregate, *tk.Client, *tk.Client) {
	t.Helper()
	e := tk.Start(t)
	if e.M.Aggregate == nil {
		t.Skip("this setup has no aggregate data sets")
	}
	e.Setup()
	nurse, clerk := e.Web(tk.RoleNurse), e.Web(tk.RoleClerk)
	nurse.Login()
	clerk.Login()
	e.Sync()
	return e, e.M.Aggregate, nurse, clerk
}

// The access refresh reads each user's dataEntry/metadata: what central lets them
// enter, kept for offline use.
func TestAggregateAccessRefreshed(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	h := e.App.Hub
	u, _ := h.Store.UserByName(nurse.User)
	acc := h.AggAccess(u.ID)
	if acc == nil || acc.DataSets[ag.DataSet] == nil || (ag.ValidDataSet != "" && acc.DataSets[ag.ValidDataSet] == nil) {
		t.Fatalf("nurse's aggregate access: %+v", acc)
	}
	e.Offline()
	// The app's own metadata request is answered offline from what the refresh fetched.
	nurse.JSON("GET", "/api/"+h.APIMinor()+"/dataEntry/metadata", nil, 200)
}

func formPath(e *tk.Env, ds, pe string) string {
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {e.M.Facility.ID}}
	return "/api/" + e.App.Hub.APIMinor() + "/dataEntry/dataValues?" + q.Encode()
}

func cell(e *tk.Env, ds, pe, de, coc string, extra ...string) map[string]string {
	f := map[string]string{"ds": ds, "pe": pe, "ou": e.M.Facility.ID, "de": de, "co": coc}
	for i := 0; i+1 < len(extra); i += 2 {
		f[extra[i]] = extra[i+1]
	}
	return f
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

// A value on central is served offline in a form this user never opened: the hub
// pulled it. Offline edits are attributed to their authors on central, whoever opens
// the app afterwards (the shared-browser case from the spike).
func TestAggregateOfflineAttribution(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	e.SetCentralDataValue(ag.DataSet, pe, ag.Integer, ag.COC, "11")
	e.Sync()
	e.Offline()
	st, hdr, b := clerk.DoHeader("GET", formPath(e, ag.DataSet, pe))
	if st != 200 || hdr.Get("X-Hub-Mode") != "offline" || !strings.Contains(string(b), `"11"`) {
		t.Fatalf("offline form for the clerk: %d %q %s", st, hdr.Get("X-Hub-Mode"), b)
	}
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	if st, b := nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "21")); st != 201 {
		t.Fatalf("nurse's offline value: %d %s", st, b)
	}
	form := clerk.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
	if got, _ := formValue(form, ag.Number, ag.COC); got != "21" {
		t.Fatalf("the clerk sees the nurse's offline value: %q", got)
	}
	if st, b := clerk.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Integer, ag.COC, "value", "22")); st != 201 {
		t.Fatalf("clerk's offline value: %d %s", st, b)
	}
	e.Online()
	e.Sync()
	if val, by, _ := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); val != "21" || by != nurse.User {
		t.Errorf("number on central: %q by %q, want 21 by %s", val, by, nurse.User)
	}
	if val, by, _ := e.CentralDataValue(ag.DataSet, pe, ag.Integer, ag.COC, ag.DefaultAOC); val != "22" || by != clerk.User {
		t.Errorf("integer on central: %q by %q, want 22 by %s", val, by, clerk.User)
	}
}

// Review focus 2: two users' offline edits of one cell are both pushed, in order.
func TestAggregateSameCellTwoUsers(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	e.Offline()
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "1"))
	clerk.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "2"))
	e.Online()
	e.Sync()
	if val, by, _ := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); val != "2" || by != clerk.User {
		t.Fatalf("last write wins: %q by %q", val, by)
	}
}

// Review focus 4: a period locked while edits wait: refused on push, kept as an issue;
// the other form's edit is pushed.
func TestAggregateLockedOnPushKeepsValue(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	e.NeedFake()
	pe := tk.CurrentMonth(e)
	e.Offline()
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "5"))
	// Values are keyed by element, period and option combinations, not data set: the
	// other form is last month's.
	prev := e.App.Hub.CentralNow().AddDate(0, -1, 0).Format("200601")
	nurse.PostDataValue(v, cell(e, ag.DataSet, prev, ag.Number, ag.COC, "value", "6"))
	e.Fake.Approve(ag.DataSet, pe)
	e.Online()
	e.Sync()
	if _, _, ok := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); ok {
		t.Fatal("approved period: central must not have the value")
	}
	if val, _, _ := e.CentralDataValue(ag.DataSet, prev, ag.Number, ag.COC, ag.DefaultAOC); val != "6" {
		t.Fatalf("the other form's value: %q", val)
	}
	issues, _ := e.App.Hub.Store.Issues(false, 10)
	if len(issues) != 1 || issues[0].Kind != "aggregate" || !strings.Contains(string(issues[0].Error), "E2017") ||
		!strings.Contains(string(issues[0].Error), `"value":"5"`) {
		t.Fatalf("issue: %+v", issues)
	}
	admin := e.Web(tk.RoleAdmin)
	admin.Login()
	if _, page := admin.Do("GET", "/hub/ui", nil); !strings.Contains(string(page), "Fake monthly, "+pe) ||
		!strings.Contains(string(page), ": 5") {
		t.Fatalf("the dashboard names the refused value: %.3000s", page)
	}
}

// Review focus 5: a completion waits for the form's earlier entries.
func TestAggregateCompletionWaitsForValues(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	e.NeedFake()
	pe := tk.CurrentMonth(e)
	e.Offline()
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Required, ag.COC, "value", "3"))
	clerk.JSON("POST", "/api/"+e.App.Hub.APIMinor()+"/dataEntry/dataSetCompletion",
		map[string]any{"dataSet": ag.DataSet, "period": pe, "orgUnit": e.M.Facility.ID, "completed": true}, 200)
	u, _ := e.App.Hub.Store.UserByName(nurse.User)
	e.Fake.RevokeToken(u.PATUID) // central refuses the nurse's token: her value waits
	e.Online()
	e.Sync()
	if e.Fake.Complete(ag.DataSet, pe, ag.DefaultAOC) {
		t.Fatal("completion pushed before the value it needs")
	}
	nurse.Login() // online: a new token
	e.Sync()
	if !e.Fake.Complete(ag.DataSet, pe, ag.DefaultAOC) {
		t.Fatal("completion not pushed after the value")
	}
}

// The pull stores the forms of the window that have data; a pushed edit reaches
// central as its author.
func TestAggregatePullAndPush(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	e.SetCentralDataValue(ag.DataSet, pe, ag.Integer, ag.COC, "4")
	e.Sync()
	h := e.App.Hub
	key := aggregate.FormKey{DataSet: ag.DataSet, Period: pe, OrgUnit: e.M.Facility.ID, AOC: ag.DefaultAOC}.String()
	f, err := h.Store.GetAggForm(key)
	if err != nil || f == nil || !strings.Contains(string(f.Body), `"4"`) {
		t.Fatalf("pulled form: %+v %v", f, err)
	}
	u, _ := h.Store.UserByName(nurse.User)
	v := "9"
	ed := &aggregate.Edit{Op: aggregate.OpValue, Form: aggregate.FormKey{DataSet: ag.DataSet, Period: pe,
		OrgUnit: e.M.Facility.ID, AOC: ag.DefaultAOC}, DataElement: ag.Number, COC: ag.COC, Value: &v, By: nurse.User}
	b, _ := json.Marshal(ed)
	h.Store.Tx(context.Background(), func(tx *sql.Tx) error {
		_, err := h.Store.AppendAggregate(tx, u.ID, b, ed.CellKey(), ed.Form.String(), false)
		return err
	})
	e.Sync()
	if val, by, _ := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); val != "9" || by != nurse.User {
		t.Fatalf("pushed: %q by %q", val, by)
	}
	if n := h.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Fatalf("pending after push: %d", n)
	}
}

// Review focus 1: value, comment and delete of one cell, offline: each is kept.
func TestAggregateCollapseKeepsFields(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	e.Offline()
	for _, val := range []string{"1", "2", "3"} {
		nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", val))
	}
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "comment", "checked twice"))
	if n := e.App.Hub.Store.CountOutbox(store.OutboxPending); n != 2 {
		t.Fatalf("three value edits and a comment: %d outbox entries, want 2", n)
	}
	q := url.Values{"de": {ag.Number}, "co": {ag.COC}, "ds": {ag.DataSet}, "ou": {e.M.Facility.ID}, "pe": {pe}}
	if st, b := nurse.Do("DELETE", v+"?"+q.Encode(), nil); st != 204 {
		t.Fatalf("offline delete: %d %s", st, b)
	}
	if n := e.App.Hub.Store.CountOutbox(store.OutboxPending); n != 3 {
		t.Fatalf("the delete comes after the comment: %d entries, want 3", n)
	}
	form := nurse.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
	if _, ok := formValue(form, ag.Number, ag.COC); ok {
		t.Fatal("deleted value still shown")
	}
	e.Online()
	e.Sync()
	if _, _, ok := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); ok {
		t.Fatal("value on central after the delete")
	}
}

// Writes central would refuse are refused offline with central's codes; a period
// outside the window is locked.
func TestAggregateOfflineRefusals(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	e.Offline()
	if st, b := nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Integer, ag.COC, "value", "1.5")); st != 409 ||
		!strings.Contains(string(b), "E2030") {
		t.Fatalf("1.5 for an integer: %d %s", st, b)
	}
	old := e.App.Hub.CentralNow().AddDate(-3, 0, 0).Format("200601")
	form := nurse.JSON("GET", formPath(e, ag.DataSet, old), nil, 200)
	if form["lockStatus"] != "LOCKED" || len(form["dataValues"].([]any)) != 0 {
		t.Fatalf("a period outside the window: %v", form)
	}
	if st, b := nurse.PostDataValue(v, cell(e, ag.DataSet, old, ag.Number, ag.COC, "value", "1")); st != 409 ||
		!strings.Contains(string(b), "E2017") {
		t.Fatalf("value outside the window: %d %s", st, b)
	}
	if st, _ := nurse.Do("POST", "/api/"+e.App.Hub.APIMinor()+"/dataValues/file", nil); st != 409 {
		t.Fatalf("file value offline: %d, want 409 (never 503: the app would queue it in the browser)", st)
	}
}

// Review focus 3: an attribute option combination the user may not use is neither
// served nor written.
func TestAggregateAttributeAccess(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	if ag.DonorDataSet == "" {
		t.Skip("no data set with an attribute combination in this setup")
	}
	pe := tk.CurrentMonth(e)
	e.Offline()
	q := url.Values{"ds": {ag.DonorDataSet}, "pe": {pe}, "ou": {e.M.Facility.ID}, "cc": {ag.DonorCC}, "cp": {ag.DonorOptionNurse}}
	path := "/api/" + e.App.Hub.APIMinor() + "/dataEntry/dataValues?" + q.Encode()
	nurse.JSON("GET", path, nil, 200)
	if st, _ := clerk.Do("GET", path, nil); st != 409 {
		t.Fatalf("the clerk may not use the nurse's donor option: %d", st)
	}
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	if st, _ := clerk.PostDataValue(v, cell(e, ag.DonorDataSet, pe, ag.Number, ag.COC, "cc", ag.DonorCC, "cp",
		ag.DonorOptionNurse, "value", "1")); st != 409 {
		t.Fatalf("clerk writes with the nurse's option: %d", st)
	}
}

// Online with an empty outbox, the app talks to central.
func TestAggregatePassesThroughOnline(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	st, hdr, _ := nurse.DoHeader("GET", formPath(e, ag.DataSet, tk.CurrentMonth(e)))
	if st != 200 || hdr.Get("X-Hub-Mode") != "" {
		t.Fatalf("online form: %d %q", st, hdr.Get("X-Hub-Mode"))
	}
}

// Completion offline: queued for an ordinary data set; refused where validation is
// mandatory (central does not check it, so a queued completion would bypass it) or
// a compulsory field is empty.
func TestAggregateCompletionOffline(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	path := "/api/" + e.App.Hub.APIMinor() + "/dataEntry/dataSetCompletion"
	req := func(ds string) map[string]any {
		return map[string]any{"dataSet": ds, "period": pe, "orgUnit": e.M.Facility.ID, "completed": true}
	}
	e.DeleteCentralDataValue(ag.DataSet, pe, ag.Required, ag.COC) // live instances keep earlier runs' values
	e.Sync()
	e.Offline()
	if ag.ValidDataSet != "" {
		if st, b := nurse.Do("POST", path, req(ag.ValidDataSet)); st != 409 || !strings.Contains(string(b), "Completing needs a connection") {
			t.Fatalf("validCompleteOnly offline: %d %s", st, b)
		}
	}
	if st, b := nurse.Do("POST", path, req(ag.DataSet)); st != 409 || !strings.Contains(string(b), "compulsory") {
		t.Fatalf("compulsory field empty: %d %s", st, b)
	}
	nurse.PostDataValue("/api/"+e.App.Hub.APIMinor()+"/dataValues", cell(e, ag.DataSet, pe, ag.Required, ag.COC, "value", "1"))
	nurse.JSON("POST", path, req(ag.DataSet), 200)
	form := nurse.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
	if cs := form["completeStatus"].(map[string]any); cs["complete"] != true {
		t.Fatalf("form after offline completion: %v", cs)
	}
	e.Online()
	e.Sync()
	var regs struct {
		Registrations []struct {
			Period    string `json:"period"`
			Completed bool   `json:"completed"`
		} `json:"completeDataSetRegistrations"`
	}
	q := url.Values{"dataSet": {ag.DataSet}, "orgUnit": {e.M.Facility.ID}, "period": {pe}}
	e.Central.JSON("GET", "/api/completeDataSetRegistrations?"+q.Encode(), nil, &regs)
	if len(regs.Registrations) != 1 || !regs.Registrations[0].Completed {
		t.Fatalf("on central: %+v", regs)
	}
}

// Pending edits whose period locks soon are listed on the dashboard.
func TestAggregateExpiryWarning(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	e.NeedFake()
	// Last month locks within a day: its last day plus whole days just past now.
	now := e.App.Hub.CentralNow()
	last := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	naiveNow := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute(), 0, 0, time.UTC)
	e.Fake.AggExpiryDays = float64(int(naiveNow.Sub(last).Hours()/24) + 1)
	pe := last.Format("200601")
	nurse.Login() // the access refresh at login reads the new expiry
	e.Sync()
	e.Offline()
	if st, b := nurse.PostDataValue("/api/"+e.App.Hub.APIMinor()+"/dataValues",
		cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "1")); st != 201 {
		t.Fatalf("value for last month: %d %s", st, b)
	}
	ws := e.App.Hub.AggWarnings(3 * 24 * time.Hour)
	if len(ws) != 1 || ws[0].Pending != 1 || !strings.Contains(ws[0].Label, pe) {
		t.Fatalf("warnings: %+v", ws)
	}
	admin := e.Web(tk.RoleAdmin)
	admin.Login()
	if st, page := admin.Do("GET", "/hub/ui", nil); st != 200 || !strings.Contains(string(page), "Locks soon") {
		t.Fatalf("admin page: %d, no expiry warning", st)
	}
}

// The hub's offline forms match central's, for each aggregate user and each form of
// the window that has values: values (with comment, follow-up, stored by), lock and
// completion.
func TestAggregateOracle(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	e.SetCentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, "17")
	e.Sync()
	type cellState struct {
		Value, Comment, StoredBy string
		FollowUp                 bool
	}
	norm := func(form map[string]any) (map[string]cellState, string, bool) {
		out := map[string]cellState{}
		for _, v := range form["dataValues"].([]any) {
			m := v.(map[string]any)
			c, _ := m["comment"].(string)
			f, _ := m["followUp"].(bool)
			out[m["dataElement"].(string)+"."+m["categoryOptionCombo"].(string)] = cellState{m["value"].(string), c,
				m["storedBy"].(string), f}
		}
		cs, _ := form["completeStatus"].(map[string]any)
		done, _ := cs["complete"].(bool)
		lock, _ := form["lockStatus"].(string)
		return out, lock, done
	}
	online := map[string]map[string]any{}
	for _, c := range []*tk.Client{nurse, clerk} {
		online[c.User] = c.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
	}
	e.Offline()
	for _, c := range []*tk.Client{nurse, clerk} {
		off := c.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
		cv, cl, cd := norm(online[c.User])
		hv, hl, hd := norm(off)
		for k, v := range cv {
			if hv[k] != v {
				t.Errorf("%s %s: MISSING or DIFFERS: central %+v, hub %+v", c.User, k, v, hv[k])
			}
		}
		for k, v := range hv {
			if _, ok := cv[k]; !ok {
				t.Errorf("%s %s: LEAK: hub shows %+v, central nothing", c.User, k, v)
			}
		}
		if cl != hl || cd != hd {
			t.Errorf("%s: lock %s/%s, complete %v/%v (central/hub)", c.User, cl, hl, cd, hd)
		}
	}
}

// A value entered online through the hub just before an outage is shown offline to
// colleagues: the hub learns from the writes it passes to central, as for tracker.
func TestAggregateOnlineWriteSeenOffline(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	val := fmt.Sprint(100 + time.Now().Nanosecond()%800)
	if st, b := nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", val)); st != 201 {
		t.Fatalf("online value: %d %s", st, b)
	}
	e.Offline() // before any sync
	form := clerk.JSON("GET", formPath(e, ag.DataSet, pe), nil, 200)
	if got, _ := formValue(form, ag.Number, ag.COC); got != val {
		t.Fatalf("offline, the clerk sees %q, want the nurse's online value %s", got, val)
	}
}

// Review fix: an edit folded into an outbox entry while that entry is being pushed
// is not lost: the newer value reaches central.
func TestAggregateEditDuringPush(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	e.NeedFake()
	pe := tk.CurrentMonth(e)
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	e.Offline()
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "1"))
	e.Online()
	done := false
	e.Fake.OnAggWrite = func() { // the nurse edits the cell again while its entry is on its way
		if !done {
			done = true
			if st, b := nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "2")); st != 201 {
				t.Errorf("edit during the push: %d %s", st, b)
			}
		}
	}
	e.Sync()
	e.Fake.OnAggWrite = nil
	e.Sync()
	if val, _, _ := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); val != "2" {
		t.Fatalf("on central: %q, want the newer 2", val)
	}
}

// Review fix: while an earlier author's edits wait (their token refused), later
// edits of the same cell, and completions of the same form, wait behind them.
func TestAggregateBlockedAuthorKeepsOrder(t *testing.T) {
	e, ag, nurse, clerk := aggregateEnv(t)
	e.NeedFake()
	pe := tk.CurrentMonth(e)
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	e.Offline()
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "1"))
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Required, ag.COC, "value", "7"))
	clerk.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Number, ag.COC, "value", "2"))
	clerk.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Integer, ag.COC, "value", "3"))
	clerk.JSON("POST", "/api/"+e.App.Hub.APIMinor()+"/dataEntry/dataSetCompletion",
		map[string]any{"dataSet": ag.DataSet, "period": pe, "orgUnit": e.M.Facility.ID, "completed": true}, 200)
	u, _ := e.App.Hub.Store.UserByName(nurse.User)
	e.Fake.RevokeToken(u.PATUID)
	e.Online()
	e.Sync()
	if _, _, ok := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); ok {
		t.Fatal("the clerk's edit of the cell went ahead of the nurse's waiting one")
	}
	if e.Fake.Complete(ag.DataSet, pe, ag.DefaultAOC) {
		t.Fatal("the completion went ahead of the nurse's waiting values")
	}
	nurse.Login()
	e.Sync()
	if val, by, _ := e.CentralDataValue(ag.DataSet, pe, ag.Number, ag.COC, ag.DefaultAOC); val != "2" || by != clerk.User {
		t.Fatalf("after the nurse's login: %q by %q, want the clerk's later 2", val, by)
	}
	if !e.Fake.Complete(ag.DataSet, pe, ag.DefaultAOC) {
		t.Fatal("completion not pushed after the values")
	}
}

// Review fix: a data set the hub has not pulled is answered locked, not as an empty,
// open form.
func TestAggregateUnpulledFormLocked(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	e.NeedFake()
	h := e.App.Hub
	h.Store.SetPrivate("agg_pulled:"+ag.DataSet, "") // as on a hub that never pulled it
	e.Offline()
	form := nurse.JSON("GET", formPath(e, ag.DataSet, tk.CurrentMonth(e)), nil, 200)
	if form["lockStatus"] != "LOCKED" {
		t.Fatalf("a data set never pulled: %v", form["lockStatus"])
	}
}

// Review fix: central answering errors keeps what the hub holds: forms are not pruned,
// a user's aggregate access is not emptied.
func TestAggregateErrorsKeepForms(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	e.NeedFake()
	h := e.App.Hub
	pe := tk.CurrentMonth(e)
	e.SetCentralDataValue(ag.DataSet, pe, ag.Integer, ag.COC, "4")
	e.Sync()
	key := aggregate.FormKey{DataSet: ag.DataSet, Period: pe, OrgUnit: e.M.Facility.ID, AOC: ag.DefaultAOC}.String()
	if f, _ := h.Store.GetAggForm(key); f == nil {
		t.Fatal("form not pulled")
	}
	e.Fake.AggFail = map[string]int{"/api/dataValueSets": 500, "/api/dataEntry/metadata": 500}
	u, _ := h.Store.UserByName(nurse.User)
	h.RefreshAccess(context.Background(), u.ID)
	e.App.Worker.Cycle(context.Background()) // reports the errors
	e.Fake.AggFail = nil
	if f, _ := h.Store.GetAggForm(key); f == nil {
		t.Fatal("a failing pull pruned the form")
	}
	if acc := h.AggAccess(u.ID); acc == nil || acc.DataSets[ag.DataSet] == nil {
		t.Fatal("a failing metadata request emptied the nurse's aggregate access")
	}
}

// Review fix: during an upgrade an aggregate write is refused with a 409, never a
// 503 (the app would queue it in the browser).
func TestAggregateWriteDuringUpgrade(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	h := e.App.Hub
	e.Offline()
	h.FreezeLocalWrites()
	defer h.ThawLocalWrites()
	st, b := nurse.PostDataValue("/api/"+h.APIMinor()+"/dataValues", cell(e, ag.DataSet, tk.CurrentMonth(e), ag.Number, ag.COC, "value", "1"))
	if st != 409 {
		t.Fatalf("write during an upgrade: %d %s, want 409", st, b)
	}
}

// Review fix: clearing a value that was only entered offline is no refusal: central
// has nothing to delete.
func TestAggregateDeleteOfflineOnlyValue(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	pe := tk.CurrentMonth(e)
	e.DeleteCentralDataValue(ag.DataSet, pe, ag.Integer, ag.COC)
	e.Sync()
	v := "/api/" + e.App.Hub.APIMinor() + "/dataValues"
	e.Offline()
	nurse.PostDataValue(v, cell(e, ag.DataSet, pe, ag.Integer, ag.COC, "value", "5"))
	q := url.Values{"de": {ag.Integer}, "co": {ag.COC}, "ds": {ag.DataSet}, "ou": {e.M.Facility.ID}, "pe": {pe}}
	if st, b := nurse.Do("DELETE", v+"?"+q.Encode(), nil); st != 204 {
		t.Fatalf("offline delete: %d %s", st, b)
	}
	e.Online()
	e.Sync()
	if n := e.App.Hub.Store.CountIssues(); n != 0 {
		is, _ := e.App.Hub.Store.Issues(false, 5)
		t.Fatalf("%d issues for a value that never reached central: %s", n, is[0].Error)
	}
}
