package adversarial

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// More of central's lifecycle (group H) and of DHIS2's own updates (group N), against
// the real DHIS2 in HUB_MANIFEST. What a real central cannot be made to do here (a
// deadlock, a reshaped answer of a release that does not exist yet) is put on the
// uplink by Netcut: central's real answer, changed.

// pushesOf counts the hub's pushes in calls, and those whose answer was rewritten.
func pushesOf(calls []tk.Call) (all, rewritten int) {
	for _, c := range calls {
		if c.Method == "POST" && c.URL.Path == "/api/tracker" && c.URL.Query().Get("atomicMode") == "OBJECT" {
			all++
			if c.Fault == "rewrite" {
				rewritten++
			}
		}
	}
	return
}

// offlineSaves is the confirmed registrations the hub took itself (from its outbox),
// made in [from, to), and how many of them central holds.
func (r *netRun) offlineSaves(from, to time.Time) (made []netSave, onCentral int) {
	for _, s := range r.ledger.all() {
		if s.Confirmed && !s.Online && !s.Update && !s.At.Before(from) && s.At.Before(to) {
			made = append(made, s)
			if r.central("events", s.Event, "event") != nil {
				onCentral++
			}
		}
	}
	return
}

// H8: central's database deadlocks on one import: central answers 500 to every push
// that carries one registration (as DHIS2 does for a payload that trips a bug or a
// lock), while other pushes go through, and a burst of 500s hits every push for a
// moment. Must hold (I5): the failing entry is retried later, not lost, not looping
// for ever; the entries after it are not held up.
func TestH8CentralFailsSomeImports(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	start := time.Now()
	r.outage(20 * time.Second)
	var poison netSave
	for _, s := range r.ledger.all() {
		if s.Confirmed && !s.Online && !s.Update {
			poison = s
			break
		}
	}
	if poison.TE == "" {
		t.Fatal("no registration was queued offline")
	}
	const deadlock = `{"httpStatus":"Internal Server Error","httpStatusCode":500,"status":"ERROR",` +
		`"message":"could not execute statement; nested exception is org.hibernate.exception.LockAcquisitionException: deadlock detected"}`
	unpoison := r.e.Net.Answer(tk.All(tk.IsPush, tk.BodyHas(poison.TE)), 500, deadlock)
	defer unpoison()
	r.e.Net.Calls()
	back := time.Now()
	r.e.Net.SetDown(false)
	time.Sleep(2 * time.Minute)
	stop()
	calls := r.e.Net.Calls()
	pushes, failed := pushesOf(calls)
	s := r.p.Status()
	made, on := r.offlineSaves(start, back)
	t.Logf("2 minutes with central failing every push of %s (registered offline at %s): %d pushes, %d of them answered 500; "+
		"pending %v, rejected %v; of %d registrations the hub took offline, %d on central", poison.TE,
		poison.At.Format("15:04:05.000"), pushes, failed, s["pending"], s["rejected"], len(made), on)
	if on < len(made)-1 {
		// Found 28 October: a 5xx ended the push cycle and was retried at the next with
		// no limit, so nothing behind it reached central. Fixed 29 October: after a plain
		// 500 the push goes on to the next entry, and the entry is refused after a day.
		t.Errorf("I5: one import central fails holds up the rest: %d of %d other offline registrations reached central in 2 minutes",
			on, len(made)-1)
	}
	if failed > 4 {
		t.Logf("I5/I7: the failing import was sent %d times in 2 minutes (every cycle), with no end", failed)
	}

	// Central recovers: everything goes, once.
	unpoison()
	burst := r.e.Net.Answer(tk.IsPush, 500, deadlock)
	stop = r.write(300 * time.Millisecond)
	time.Sleep(15 * time.Second)
	burst()
	time.Sleep(10 * time.Second)
	stop()
	r.finish(5 * time.Minute)
	if r.central("trackedEntities", poison.TE, "trackedEntity") == nil {
		t.Errorf("I1: the registration central failed is not on central after it recovered")
	}
}

// slowly delays central's answers to matching requests by d.
func slowly(d time.Duration) func(int, []byte) (int, []byte) {
	return func(st int, b []byte) (int, []byte) {
		time.Sleep(d)
		return st, b
	}
}

// isWork matches what central does the work for: tracker imports and listings, and
// the aggregate data values; not its ping and system info.
func isWork(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/tracker") || strings.Contains(r.URL.Path, "dataValue")
}

// H2: central very slow, as while analytics runs: first its ping answers at once and
// the work takes 30 s an answer, then everything takes 30 s. Must hold: the hub stays
// usable (staff reads and saves are answered, by central or by the hub); its sync
// completes later; nothing lost.
func TestH2CentralVerySlow(t *testing.T) {
	r := netStart(t)
	read := r.reader()
	stop := r.write(500 * time.Millisecond)
	defer stop()
	r.outage(15 * time.Second)
	r.e.Net.SetDown(false)
	r.p.WaitFor("online", time.Minute, tk.Online)

	// 1. The work is slow, the ping is not.
	slow := r.e.Net.Rewrite(isWork, slowly(30*time.Second))
	r.e.Net.Calls()
	start := time.Now()
	var reads []time.Duration
	answered := map[string]int{}
	for time.Since(start) < 3*time.Minute {
		d, st, mode := read()
		reads = append(reads, d)
		answered[fmt.Sprintf("%d %s", st, mode)]++
		time.Sleep(2 * time.Second)
	}
	s := r.p.Status()
	pushes, _ := pushesOf(r.e.Net.Calls())
	t.Logf("work slow (30 s), ping fast, 3 min: mode %v, pending %v; saves waited %s; reads %s, answered %v; %d pushes",
		s["mode"], s["pending"], r.waits(start, time.Now()), durStats(reads), answered, pushes)
	slow()
	failed := 0
	for _, x := range r.ledger.all() {
		if !x.At.Before(start) && !x.Confirmed {
			failed++
		}
	}
	t.Logf("saves not confirmed while the work was slow: %d", failed)

	// 2. Everything slow, the ping too.
	r.e.Net.SetDelay(30 * time.Second)
	start = time.Now()
	reads, answered = nil, map[string]int{}
	for time.Since(start) < 2*time.Minute {
		d, st, mode := read()
		reads = append(reads, d)
		answered[fmt.Sprintf("%d %s", st, mode)]++
		time.Sleep(2 * time.Second)
	}
	t.Logf("everything slow, 2 min: mode %v; saves waited %s; reads %s, answered %v", r.p.Status()["mode"],
		r.waits(start, time.Now()), durStats(reads), answered)
	r.e.Net.SetDelay(0)
	for k, n := range answered {
		if !strings.HasPrefix(k, "200") {
			t.Errorf("staff reads failed while central was slow: %d answered %q", n, k)
		}
	}
	time.Sleep(10 * time.Second)
	stop()
	r.finish(5 * time.Minute)
}

// renameKey renames a JSON object key in a body, as a new release might.
func renameKey(from, to string) func(int, []byte) (int, []byte) {
	return func(st int, b []byte) (int, []byte) {
		return st, bytes.ReplaceAll(b, []byte(`"`+from+`":`), []byte(`"`+to+`":`))
	}
}

func isPull(r *http.Request) bool {
	return r.Method == "GET" && r.URL.Path == "/api/tracker/trackedEntities"
}

// N14, the pull: a new major renames the collection of the tracked entity listing (as
// 2.41 did once: "instances" for "trackedEntities"). Meanwhile a person is registered
// and an event edited on central. Must hold: the hub refuses rather than misreads: it
// does not take an empty page for "nothing changed" and move its watermark past what
// it did not read; once central answers as before, the hub holds both changes.
func TestN14PullAnswerReshaped(t *testing.T) {
	r := netStart(t)
	r.e.Web(tk.RoleNurse).Login() // unlocks the hub's process
	a := r.centralPerson("61")
	r.pulls(2)
	rename := r.e.Net.Rewrite(isPull, renameKey("trackedEntities", "instances"))
	defer rename()
	b := r.centralPerson("62")
	r.centralEdit(a, "63")
	r.pulls(3)
	s := r.p.Status()
	t.Logf("pulls with the listing renamed: lastPull %v, mode %v; hub log errors about it: %d", s["lastPull"], s["mode"],
		r.p.Grep("instances"))
	during := r.offlineValues(a.Event, b.Event)
	rename()
	r.pulls(3)
	after := r.offlineValues(a.Event, b.Event)
	t.Logf("the hub offline, while renamed: edited event %q, new person's event %q; after: %q, %q", during[0], during[1], after[0], after[1])
	if after[0] != "63" || after[1] != "62" {
		finding(t, "N14: a pull answer without the trackedEntities collection is read as an empty page")
		t.Errorf("N14: after central answers as before, the hub offline holds %q (want 63) and %q (want 62): "+
			"the pulls of the renamed answer moved the watermark past changes they did not read", after[0], after[1])
	}
}

// N14, the push: a new major reshapes the tracker import report (bundleReport renamed),
// or answers with an error code the hub does not know. Must hold: nothing confirmed is
// lost; the hub does not read a refusal as a success.
func TestN14PushReportReshaped(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	start := time.Now()
	r.outage(15 * time.Second)
	stop()
	reshape := r.e.Net.Rewrite(tk.IsPush, renameKey("bundleReport", "importReport"))
	r.e.Net.Calls()
	back := time.Now()
	r.e.Net.SetDown(false)
	// An answer without bundleReport says nothing about the saves: the hub keeps them
	// queued and tries again (refused only after a day), so the queue does not drain
	// while the report is reshaped.
	time.Sleep(time.Minute)
	pushes, rewritten := pushesOf(r.e.Net.Calls())
	s := r.p.Status()
	made, on := r.offlineSaves(start, back)
	t.Logf("a minute with the report reshaped: %d pushes (%d reshaped); pending %v, rejected %v; %d of %d offline registrations on central; "+
		"log: %d rejected objects (%d 'not persisted by central')", pushes, rewritten, s["pending"], s["rejected"], on, len(made),
		r.p.Grep("msg=\"object rejected\""), r.p.Grep("not persisted by central"))
	if s["rejected"] != float64(0) {
		t.Errorf("I8: %v objects refused while central's report was reshaped; want them kept and retried", s["rejected"])
	}
	reshape()
	r.p.WaitFor("pushed", 3*time.Minute, func(s map[string]any) bool { return s["pending"] == float64(0) && s["syncing"] == false })
	r.pulls(2)
	s = r.p.Status()
	t.Logf("after central answers as before and two pulls: pending %v, rejected %v", s["pending"], s["rejected"])
	if s["rejected"] != float64(0) {
		// Found 28 October: compat v42 TrackerResult took any answer with a status for an
		// import report, and an object it did not find in bundleReport.typeReportMap as
		// not persisted: every object was refused, though central stored it. Fixed 29
		// October: an answer without bundleReport that is no refusal is no import report.
		t.Errorf("I8: %v objects central stored stay listed as refused on the hub after a reshaped report", s["rejected"])
	}
	r.finish(3 * time.Minute)
}

func isPAT(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "ApiToken ")
}

// N15: a new major changes personal access tokens: central stops taking the hub's
// tokens (401 to every one), and the endpoint that makes them has moved (404). Must
// hold: the hub keeps its queued saves and says what it needs; staff logging in online
// get new tokens when central makes them again; nothing lost.
func TestN15TokensChange(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(15 * time.Second)
	r.e.Net.SetDown(false)
	stop()
	const gone = `{"httpStatus":"Unauthorized","httpStatusCode":401,"status":"ERROR","message":"Unauthorized"}`
	noPAT := r.e.Net.Answer(isPAT, 401, gone)
	defer noPAT()
	moved := r.e.Net.Answer(func(q *http.Request) bool {
		return q.Method == "POST" && strings.HasPrefix(q.URL.Path, "/api/apiToken")
	},
		404, `{"httpStatus":"Not Found","httpStatusCode":404,"status":"ERROR","message":"Not found"}`)
	defer moved()
	time.Sleep(time.Minute)
	s := r.p.Status()
	t.Logf("tokens refused, a minute: mode %v, pending %v, stuckOutbox %v, message %v", s["mode"], s["pending"], s["stuckOutbox"], s["message"])
	c := r.e.Web(tk.RoleNurse)
	st, _, b, _ := c.Try("POST", "/api/auth/login", map[string]string{"username": c.User, "password": r.e.M.Password})
	t.Logf("an online login while central makes no tokens: %d %.200s", st, b)
	pr := r.e.M.NewPerson(r.prog, "Zqtokx") // not the run's tag: not in its ledger
	st, h, b, _ := c.Try("POST", tk.SyncImport, pr.Payload)
	t.Logf("a save after it: %d (hub mode %q) %.200s", st, h.Get("X-Hub-Mode"), b)
	time.Sleep(30 * time.Second)
	s = r.p.Status()
	t.Logf("then: pending %v, stuckOutbox %v, message %v", s["pending"], s["stuckOutbox"], s["message"])

	// Central takes tokens again (the hub's release supports the new way): staff log
	// in online, and the queue goes.
	noPAT()
	moved()
	stop = r.write(300 * time.Millisecond)
	time.Sleep(20 * time.Second)
	stop()
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted} {
		if _, ok := r.e.M.User(role); ok {
			r.e.Web(role).Login()
		}
	}
	r.finish(5 * time.Minute)
	if st == 200 && r.central("trackedEntities", pr.TE, "trackedEntity") == nil {
		t.Errorf("I1: the save confirmed while central made no tokens is not on central")
	}
}

// N11, the part the sandbox can do: central reports a new patch release of its major
// (2.43.2 to 2.43.99) while the hub holds queued saves. Must hold: the hub carries on
// (no "needs upgrade"), pushes and pulls. A real patch upgrade of central, with the
// oracles after it, needs an instance made for it.
func TestN11CentralPatchRelease(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(15 * time.Second)
	was := r.e.M.DHIS2Version
	parts := strings.Split(was, ".")
	patch := strings.Join(parts[:2], ".") + ".99"
	r.e.Net.SetVersion(patch)
	defer r.e.Net.SetVersion("")
	r.e.Net.SetDown(false)
	r.p.WaitFor("online", time.Minute, tk.Online)
	a := r.centralPerson("71")
	r.pulls(2)
	r.centralEdit(a, "72")
	r.pulls(2)
	time.Sleep(10 * time.Second)
	stop()
	s := r.p.Status()
	t.Logf("central says %s (was %s): needsUpgrade %v, message %v", patch, was, s["needsUpgrade"], s["message"])
	if s["needsUpgrade"] == true {
		t.Errorf("the hub asks for an upgrade for a patch release of a major it supports")
	}
	if v := r.offlineValues(a.Event); v[0] != "72" {
		t.Errorf("an event edited on central after the patch: the hub shows %q offline, want 72", v[0])
	}
	r.finish(5 * time.Minute)
}

// aggValue posts one aggregate value as a web client through the hub and returns the
// hub's answer.
func (s *scene) aggValue(c *tk.Client, de, pe, value string) (int, string) {
	s.t.Helper()
	path := "/api/" + strings.Split(s.M.DHIS2Version, ".")[1] + "/dataValues"
	st, b := c.PostDataValue(path, map[string]string{"ds": s.M.Aggregate.DataSet, "pe": pe, "ou": s.M.Facility.ID,
		"de": de, "co": s.M.Aggregate.COC, "value": value})
	return st, string(b[:min(len(b), 160)])
}

// month is the monthly period n months from now.
func month(n int) string { return time.Now().AddDate(0, n, 0).Format("200601") }

// H6, the setting central has for allowed future dates: a data set's open future
// periods. Must hold: followed at the next sync, both ways; offline the hub accepts and
// refuses what central would. (Central's time zone is a server setting: changing it
// needs the instance restarted with another zone, which the broker cannot do to an
// existing instance; the hub reads no password policy, central applies it at the online
// login the hub passes through.)
func TestH6FuturePeriodsFollowed(t *testing.T) {
	s := begin(t)
	if !s.Live() || s.M.Aggregate == nil {
		t.Skip("needs a real DHIS2 with the aggregate data set")
	}
	ds := "/api/dataSets/" + s.M.Aggregate.DataSet
	var was struct{ OpenFuturePeriods int }
	if err := s.Central.JSON("GET", ds+"?fields=openFuturePeriods", nil, &was); err != nil {
		t.Fatal(err)
	}
	set := func(n int) {
		if st, b, err := s.Central.Do("PATCH", ds, []any{map[string]any{"op": "replace", "path": "/openFuturePeriods", "value": n}}); err != nil || st/100 != 2 {
			t.Fatalf("openFuturePeriods %d: %d %v %s", n, st, err, b)
		}
	}
	t.Cleanup(func() { set(was.OpenFuturePeriods) })
	web := s.Web(tk.RoleNurse)
	web.Login()
	ahead := month(2)
	try := func(when string) int {
		s.Offline()
		defer s.Online()
		st, b := s.aggValue(web, s.M.Aggregate.Number, ahead, "7")
		t.Logf("%s: a value for %s offline: %d %s", when, ahead, st, b)
		return st
	}
	before := try(fmt.Sprintf("open future periods %d", was.OpenFuturePeriods))
	set(3)
	s.Sync()
	opened := try("3 after one sync")
	s.Sync() // pushed while the period is open on central
	v, _, _ := s.CentralDataValue(s.M.Aggregate.DataSet, ahead, s.M.Aggregate.Number, s.M.Aggregate.COC, s.M.Aggregate.DefaultAOC)
	t.Logf("on central for %s: %q", ahead, v)
	if opened/100 == 2 && v != "7" {
		t.Errorf("I1: the value the hub accepted while the period was open is not on central")
	}
	s.DeleteCentralDataValue(s.M.Aggregate.DataSet, ahead, s.M.Aggregate.Number, s.M.Aggregate.COC)
	set(was.OpenFuturePeriods)
	s.Sync()
	closed := try(fmt.Sprintf("back to %d after one sync", was.OpenFuturePeriods))
	if before/100 == 2 || opened/100 != 2 || closed/100 == 2 {
		t.Errorf("the hub does not follow open future periods at the next sync: %d, %d, %d", before, opened, closed)
	}
}

// N16, what the sandbox can do of a metadata migration: the data set's structure
// changes on central (a data element leaves it, then comes back) and the hub must hold
// central's catalogue after one sync, with no stale cache: values for the element
// refused offline while it is out, accepted again once it is back. (New uids for
// categories cannot be made on a running central; a real migration needs an instance
// made for it.)
func TestN16CatalogFollowsMetadata(t *testing.T) {
	s := begin(t)
	if !s.Live() || s.M.Aggregate == nil {
		t.Skip("needs a real DHIS2 with the aggregate data set")
	}
	ds := "/api/dataSets/" + s.M.Aggregate.DataSet
	var owner map[string]any
	if err := s.Central.JSON("GET", ds+"?fields=:owner", nil, &owner); err != nil {
		t.Fatal(err)
	}
	put := func(o map[string]any) {
		if st, b, err := s.Central.Do("PUT", ds, o); err != nil || st/100 != 2 {
			t.Fatalf("putting the data set: %d %v %.300s", st, err, b)
		}
	}
	t.Cleanup(func() { put(owner) })
	without := map[string]any{}
	for k, v := range owner {
		without[k] = v
	}
	var kept []any
	for _, x := range owner["dataSetElements"].([]any) {
		if de, _ := x.(map[string]any)["dataElement"].(map[string]any); de["id"] != s.M.Aggregate.Integer {
			kept = append(kept, x)
		}
	}
	without["dataSetElements"] = kept
	web := s.Web(tk.RoleNurse)
	web.Login()
	pe := month(0)
	try := func(when, v string) int {
		s.Offline()
		defer s.Online()
		st, b := s.aggValue(web, s.M.Aggregate.Integer, pe, v)
		t.Logf("%s: a value for the integer element offline: %d %s", when, st, b)
		return st
	}
	in := try("in the data set", "11")
	s.Sync() // pushed while the element is in the data set
	put(without)
	s.Sync()
	out := try("out of the data set, after one sync", "12")
	put(owner)
	s.Sync()
	back := try("back in the data set, after one sync", "13")
	if in/100 != 2 || out/100 == 2 || back/100 != 2 {
		t.Errorf("the hub's catalogue does not follow central after one sync: %d, %d, %d", in, out, back)
	}
	s.reconnect(2)
	v, by, _ := s.CentralDataValue(s.M.Aggregate.DataSet, pe, s.M.Aggregate.Integer, s.M.Aggregate.COC, s.M.Aggregate.DefaultAOC)
	t.Logf("on central: %q by %q", v, by)
	if v != "13" {
		t.Errorf("I1: central holds %q, want the last value the hub confirmed (13)", v)
	}
	s.DeleteCentralDataValue(s.M.Aggregate.DataSet, pe, s.M.Aggregate.Integer, s.M.Aggregate.COC)
}

// H7: token lifetimes shortened. DHIS2 2.43 has no system setting for how long
// personal access tokens or sessions live (systemSettings lists none; a session's
// timeout is in dhis.conf, and the hub keeps no session on central); the lifetime of
// the tokens hubs make is the country's facility-hub-config/tokens. It goes
// from the default (90 days) to 7. Must hold: the hubs' tokens are renewed to the new
// life in time (the hub's at its next refresh, a user's at their next online login);
// saves keep pushing.
func TestH7TokenLifeShortened(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	const key = "/api/dataStore/facility-hub-config/tokens" // the hub keeps its copy as token_settings
	st, was, _ := s.Central.Do("GET", key, nil)
	t.Cleanup(func() {
		if st == 200 {
			s.Central.Do("PUT", key, was)
		} else {
			s.Central.Do("DELETE", key, nil)
		}
	})
	nurseUser := s.M.Users[tk.RoleNurse]
	tokens := func(username string) []string {
		var toks struct {
			APIToken []struct {
				ID     string
				Expire int64
			} `json:"apiToken"`
		}
		if err := s.Central.As(username, s.M.Password).JSON("GET", "/api/apiToken?fields=id,expire&paging=false", nil, &toks); err != nil {
			t.Fatal(err)
		}
		var out []string
		mine := s.App.Hub.TokenUIDs() // other hubs' tokens of the same user are theirs
		for _, x := range toks.APIToken {
			if slices.Contains(mine, x.ID) {
				out = append(out, fmt.Sprintf("%s %.1fd", x.ID, time.Until(time.UnixMilli(x.Expire)).Hours()/24))
			}
		}
		return out
	}
	web := s.Web(tk.RoleNurse)
	web.Login()
	t.Logf("before: the nurse's tokens on central %v; the hub's token expires %s", tokens(nurseUser.Username), s.App.Hub.Runtime().HubTokenExp)
	s.PutSigned("facility-hub-config/tokens", `{"expiryDays": 7}`) // as the app writes it; approves the hub
	// A cycle renews the hub's token before it reads the country's settings: the cycle
	// after takes the new life.
	s.Sync()
	hubExp, _ := time.Parse(time.RFC3339, s.App.Hub.Runtime().HubTokenExp)
	t.Logf("after a sync: the hub's token expires in %.1f days", time.Until(hubExp).Hours()/24)
	s.Sync()
	hubExp, _ = time.Parse(time.RFC3339, s.App.Hub.Runtime().HubTokenExp)
	t.Logf("after the next: the hub's token expires in %.1f days", time.Until(hubExp).Hours()/24)
	web = s.Web(tk.RoleNurse)
	web.Login()
	toks := tokens(nurseUser.Username)
	t.Logf("after the nurse's next online login: their tokens on central %v", toks)
	if time.Until(hubExp) > 8*24*time.Hour {
		t.Errorf("the hub's token was not renewed to the new life")
	}
	for _, x := range toks {
		var id string
		var days float64
		fmt.Sscanf(x, "%s %fd", &id, &days)
		if days > 8 {
			t.Errorf("a token of the nurse lives %.1f days after the country chose 7: %s", days, id)
		}
	}
	nurse := s.connect(nurseUser.Username)
	s.Offline()
	s.offlineSave(nurse, "Zqtoklife")
	s.reconnect(1)
	s.expect(nil, tk.OnCentral)
}

// N17, the part the sandbox can do on its one instance: central is upgraded to a major
// this hub release does not support (its version read as 2.44.0 on the uplink), the
// upgrade fails, and central is rolled back to its previous major; the hub held saves
// across both outages. Must hold: the hub leaves "needs upgrade" by itself, pushes
// what it held, and pulls what changes after. Restoring central's database from a
// backup is H4 (28 October); the two together, with a real upgrade and a real
// rollback, need an instance made for it.
func TestN17CentralRolledBack(t *testing.T) {
	r := netStart(t)
	stop := r.write(300 * time.Millisecond)
	defer stop()
	r.outage(15 * time.Second) // central down for its upgrade
	r.e.Net.SetVersion("2.44.0")
	r.e.Net.SetDown(false)
	s := r.p.WaitFor("needs upgrade", 2*time.Minute, func(s map[string]any) bool { return s["needsUpgrade"] == true && tk.Online(s) })
	t.Logf("central back on the new major: %v", s["message"])
	time.Sleep(20 * time.Second)
	r.outage(15 * time.Second) // down again for the rollback
	r.e.Net.SetVersion("")
	back := time.Now()
	r.e.Net.SetDown(false)
	r.p.WaitFor("supported again", 2*time.Minute, func(s map[string]any) bool { return s["needsUpgrade"] == nil && tk.Online(s) })
	t.Logf("supported again %s after central came back on its previous major", time.Since(back).Round(time.Second))
	time.Sleep(10 * time.Second)
	stop()
	r.finish(5 * time.Minute)
	a := r.centralPerson("81")
	r.pulls(2)
	r.centralEdit(a, "82")
	r.pulls(2)
	if v := r.offlineValues(a.Event); v[0] != "82" {
		t.Errorf("an event edited on central after the rollback: the hub shows %q offline, want 82", v[0])
	}
}
