package adversarial

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// D3: the uplink flaps every few seconds for an hour while clients save. Must hold:
// the hub settles (it is not left offline or stuck between states), the outbox drains
// while it flaps and after, and the hub sends central no storm (I7). Each up and each
// down window lasts 5 s; the writers save about once a second each.
func TestD3FlapForAnHour(t *testing.T) {
	const span = time.Hour
	r := netStart(t)
	stop := r.write(time.Second)
	defer stop()
	time.Sleep(10 * time.Second)
	r.e.Net.Calls()
	refused := r.e.Net.Refused()
	start := time.Now()
	unflap := r.e.Net.Flap(5*time.Second, 5*time.Second)
	defer unflap()
	var calls []tk.Call
	var at []time.Time
	var pending []string
	for time.Since(start) < span {
		time.Sleep(time.Minute)
		for _, c := range r.e.Net.Calls() {
			calls, at = append(calls, c), append(at, time.Now())
		}
		s := r.p.Status()
		pending = append(pending, fmt.Sprint(pendingOf(s)))
		t.Logf("%3.0f min: %d saves so far, hub pending %d, mode %v, RSS %.0f MB", time.Since(start).Minutes(),
			len(r.ledger.all()), pendingOf(s), s["mode"], r.p.RSSMB())
	}
	unflap()
	for _, c := range r.e.Net.Calls() {
		calls, at = append(calls, c), append(at, time.Now())
	}
	flapped := time.Now()
	time.Sleep(10 * time.Second)
	stop()
	byMinute, busiest := perMinute(calls, at, start)
	pushes := 0
	for _, c := range calls {
		if c.Method == "POST" && c.URL.Path == "/api/tracker" && c.URL.Query().Get("atomicMode") == "OBJECT" {
			pushes++
		}
	}
	t.Logf("flapping %s: saves waited %s", span, r.waits(start, flapped))
	t.Logf("uplink while flapping: %s; %d pushes; %d more refused while down", callStats(calls, span), pushes, r.e.Net.Refused()-refused)
	t.Logf("requests to central per minute: %v; busiest minute: %s", byMinute, busiest)
	t.Logf("pending on the hub, minute by minute: %s", strings.Join(pending, " "))
	r.finish(10 * time.Minute)
	t.Logf("hub RSS after the check %.0f MB", r.p.RSSMB())
	if pushes < int(span/time.Minute) {
		t.Errorf("only %d pushes in %s of flapping: the outbox makes no progress", pushes, span)
	}
}

// D6: name resolution fails while TCP works, and the reverse. The hub reaches central
// by a host name ("advcentral"), which the C library resolves through HOSTALIASES to
// this machine's name, and DNS (Docker's resolver) to its address, where a Front
// forwards to the Netcut. DNS failing: the alias names a host DNS does not know
// ("no such host", a real NXDOMAIN), while the Front still accepts connections. TCP
// failing: the name resolves, the Front stops listening (connection refused). Must
// hold: treated as offline, saves kept, recovers by itself; nothing lost or twice.
//
// HOSTALIASES makes Go use the C resolver; the test process reads it at its first
// lookup, so run this scenario on its own (-run TestD6).
func TestD6DNSFails(t *testing.T) {
	aliases := filepath.Join(t.TempDir(), "hostaliases")
	alias := func(target string) {
		if err := os.WriteFile(aliases, []byte("advcentral "+target+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	host, _ := os.Hostname()
	alias(host)
	t.Setenv("HOSTALIASES", aliases)
	r := netPrepare(t)
	front := tk.NewFront(t, "0.0.0.0:0", r.e.Net, nil)
	r.e.CentralAt("http://advcentral:" + front.Port())
	r.begin()
	stop := r.write(500 * time.Millisecond)
	defer stop()
	time.Sleep(15 * time.Second)

	var flapped []string
	for _, fault := range []struct {
		what         string
		cut, restore func()
		says         string // what the hub's log gives as the reason
	}{
		{"DNS fails, TCP works", func() { alias("no-such-central.invalid") }, func() { alias(host) }, "no such host"},
		{"DNS works, TCP fails", front.Close, func() {
			if err := front.Listen(); err != nil {
				t.Fatal(err)
			}
		}, "connection refused"},
	} {
		r.e.Net.Calls()
		accepts := front.Accepts.Load()
		says := r.p.Grep(fault.says)
		switches := r.p.Grep("msg=connectivity")
		cut := time.Now()
		fault.cut()
		r.p.WaitFor("offline", 2*time.Minute, tk.Offline)
		t.Logf("%s: offline %s after the fault", fault.what, time.Since(cut).Round(100*time.Millisecond))
		time.Sleep(2 * time.Minute)
		calls := r.e.Net.Calls()
		t.Logf("%s: saves waited %s; %d connections reached the front, %d requests central; the hub's log says %q %d times",
			fault.what, r.waits(cut, time.Now()), front.Accepts.Load()-accepts, len(calls), fault.says, r.p.Grep(fault.says)-says)
		flips := r.p.Grep("msg=connectivity") - switches
		t.Logf("%s: %s; the hub switched online or offline %d times; requests by path: %v", fault.what,
			callStats(calls, 2*time.Minute), flips, byPath(calls))
		if flips > 2 {
			flapped = append(flapped, fmt.Sprintf("%s: %d switches, %s", fault.what, flips, callStats(calls, 2*time.Minute)))
		}
		back := time.Now()
		fault.restore()
		r.p.WaitFor("online", 3*time.Minute, tk.Online)
		t.Logf("%s: online %s after the fault ended", fault.what, time.Since(back).Round(100*time.Millisecond))
		r.waitDrained("drained", 5*time.Minute)
		t.Logf("%s: drained %s after the fault ended", fault.what, time.Since(back).Round(time.Second))
		if r.p.Grep(fault.says) == says {
			t.Errorf("%s: the hub's log never says %q", fault.what, fault.says)
		}
	}
	time.Sleep(5 * time.Second)
	stop()
	r.finish(5 * time.Minute)
	if len(flapped) > 0 {
		t.Errorf("D6: with DNS failing, the hub flapped online and offline: %v", flapped)
	}
}

// byPath counts calls by method and path.
func byPath(calls []tk.Call) map[string]int {
	out := map[string]int{}
	for _, c := range calls {
		out[c.Method+" "+c.URL.Path]++
	}
	return out
}

// D7: a captive portal, as a hotel's or a prepaid modem's, answers instead of central:
// every request gets the portal's login page (200, HTML), then, in a second round, a
// redirect to it (302). Must hold: not mistaken for central (the ping must say
// "pong"); the hub goes offline and keeps saves; nothing it confirms is lost or twice;
// it comes back when the portal goes. Before the second failed ping the hub still
// believes it is online and passes clients' requests through: what it does with the
// portal's answers to an aggregate value and to a tracker save is recorded, and
// whether the hub then shows, as saved, a value central never got.
func TestD7CaptivePortal(t *testing.T) {
	r := netStart(t)
	agg := r.e.M.Aggregate
	nurse := r.e.Web(tk.RoleNurse)
	nurse.Login()
	pe := time.Now().UTC().Format("200601") // the data set opens the current month (openFuturePeriods 1)
	if agg != nil {
		r.e.DeleteCentralDataValue(agg.DataSet, pe, agg.Integer, agg.COC)
	}
	stop := r.write(300 * time.Millisecond)
	defer stop()
	time.Sleep(15 * time.Second)

	type aggSave struct {
		value  string
		status int
		mode   string
		body   string
	}
	var aggSaves []aggSave
	for i, kind := range []string{tk.PortalPage, tk.PortalRedirect} {
		r.e.Net.Calls()
		start := time.Now()
		r.e.Net.SetPortal(kind)
		// Within the first ping interval, as the hub still believes it is online: one
		// aggregate value, as the Data Entry app sends it.
		if agg != nil {
			v := fmt.Sprint(7100 + i)
			st, h, b := postValue(nurse, r.e, agg.DataSet, pe, agg.Integer, agg.COC, v)
			aggSaves = append(aggSaves, aggSave{v, st, h, fmt.Sprintf("%.120s", b)})
			t.Logf("portal %q: an aggregate value %s through the hub at once: %d (X-Hub-Mode %q) %.120s", kind, v, st, h, b)
		}
		r.p.WaitFor("offline", 2*time.Minute, tk.Offline)
		t.Logf("portal %q: the hub offline %s after the portal came", kind, time.Since(start).Round(100*time.Millisecond))
		if agg != nil {
			got, st := hubFormValue(nurse, r.e, agg.DataSet, pe, agg.Integer, agg.COC)
			t.Logf("portal %q: offline, the hub's form shows the cell as %q (%d)", kind, got, st)
		}
		time.Sleep(90 * time.Second)
		calls := r.e.Net.Calls()
		var answered []string
		for _, c := range calls {
			if c.Fault == "portal" && !strings.HasSuffix(c.URL.Path, "/api/system/ping") {
				answered = append(answered, c.Method+" "+c.URL.Path)
			}
		}
		t.Logf("portal %q: %d requests answered by the portal, besides pings: %v; saves waited %s", kind,
			len(calls), answered, r.waits(start, time.Now()))
		back := time.Now()
		r.e.Net.SetPortal("")
		r.p.WaitFor("online", 2*time.Minute, tk.Online)
		r.waitDrained("drained", 5*time.Minute)
		t.Logf("portal %q: drained %s after the portal went", kind, time.Since(back).Round(time.Second))
	}
	time.Sleep(5 * time.Second)
	stop()
	r.finish(5 * time.Minute)
	if n := r.p.Grep(`reason="central returned 200"`); n > 0 {
		t.Errorf("%d saves rejected on a portal's answer", n)
	}
	if agg == nil {
		return
	}
	// After the portal and a pull: what central and the hub hold of the cell.
	r.pulls(1)
	cv, by, _ := r.e.CentralDataValue(agg.DataSet, pe, agg.Integer, agg.COC, agg.DefaultAOC)
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	hv, _ := hubFormValue(nurse, r.e, agg.DataSet, pe, agg.Integer, agg.COC)
	r.e.Net.SetDown(false)
	r.p.WaitFor("online", 2*time.Minute, tk.Online)
	t.Logf("the cell after the portal: central %q by %q; the hub offline %q", cv, by, hv)
	// Found 29 October: the portal's page was passed back as success, and the value
	// never sent. Fixed: an /api/ answer that cannot be central's counts as central
	// unreachable (proxy.notCentral), so the hub takes the value itself. Each value
	// goes into the same cell, so central holds the last one confirmed.
	var last *aggSave
	for i := range aggSaves {
		if aggSaves[i].status/100 == 2 {
			last = &aggSaves[i]
		}
	}
	if last != nil && cv != last.value {
		t.Errorf("I1: the aggregate value %s was answered %d %q (%s) and is not on central (central has %q)",
			last.value, last.status, last.mode, last.body, cv)
	}
}

// postValue posts one aggregate cell through the hub as the Data Entry app does.
func postValue(c *tk.Client, e *tk.Env, ds, pe, de, coc, v string) (int, string, []byte) {
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {e.M.Facility.ID}, "de": {de}, "co": {coc}, "value": {v}}
	st, h, b, err := c.Try("POST", "/api/dataValues?"+q.Encode(), nil)
	if err != nil {
		return 0, "", []byte(err.Error())
	}
	return st, h.Get("X-Hub-Mode"), b
}

// hubFormValue reads one cell of a form through the hub, as the Data Entry app does.
func hubFormValue(c *tk.Client, e *tk.Env, ds, pe, de, coc string) (string, int) {
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {e.M.Facility.ID}}
	st, _, b, err := c.Try("GET", "/api/dataEntry/dataValues?"+q.Encode(), nil)
	if err != nil || st != 200 {
		return "", st
	}
	var form map[string]any
	json.Unmarshal(b, &form)
	v, _ := formValue(form, de, coc)
	return v, st
}

// D8: central behind TLS presents a certificate the hub must not trust: expired, for
// another host, or self-signed. The hub trusts a test CA (SSL_CERT_FILE), which signs
// central's good certificate for 127.0.0.1; a Front terminates TLS before the Netcut
// and switches certificates. Must hold: the hub refuses each (no TLS session, so no
// token or data reaches what presents it), stays offline, says why, and recovers when
// the good certificate is back; nothing lost or twice.
func TestD8BadCertificate(t *testing.T) {
	ca := tk.NewTestCA(t)
	t.Setenv("SSL_CERT_FILE", ca.File)
	r := netPrepare(t)
	now := time.Now()
	good := ca.Issue("127.0.0.1", now.Add(-time.Hour), now.Add(24*time.Hour), false)
	front := tk.NewFront(t, "127.0.0.1:0", r.e.Net, good)
	r.e.CentralAt("https://127.0.0.1:" + front.Port())
	r.begin()
	admin := r.e.Web(tk.RoleAdmin)
	admin.Login()
	stop := r.write(500 * time.Millisecond)
	defer stop()
	time.Sleep(15 * time.Second)

	for _, bad := range []struct {
		what string
		cert *tls.Certificate
		says string
	}{
		{"expired", ca.Issue("127.0.0.1", now.Add(-48*time.Hour), now.Add(-time.Hour), false), "expired"},
		{"for another host", ca.Issue("central.example.org", now.Add(-time.Hour), now.Add(24*time.Hour), false), "validate certificate"},
		{"self-signed", ca.Issue("127.0.0.1", now.Add(-time.Hour), now.Add(24*time.Hour), true), "unknown authority"},
	} {
		r.e.Net.Calls()
		says := r.p.Grep(bad.says)
		front.SetCert(bad.cert)
		hs := front.Handshakes.Load()
		cut := time.Now()
		// Connections the hub already holds stay up with the old certificate: a
		// certificate changes for new connections only (as when central's expires).
		// Close them, as a restarted proxy with its new certificate would.
		front.Close()
		if err := front.Listen(); err != nil {
			t.Fatal(err)
		}
		r.p.WaitFor("offline", 2*time.Minute, tk.Offline)
		t.Logf("certificate %s: offline %s after it was presented", bad.what, time.Since(cut).Round(100*time.Millisecond))
		time.Sleep(90 * time.Second)
		// Requests central answered after the switch (one in flight at the switch may
		// complete on its old connection).
		var calls []tk.Call
		for _, c := range r.e.Net.Calls() {
			if c.At.After(cut.Add(time.Second)) {
				calls = append(calls, c)
			}
		}
		handshakes := front.Handshakes.Load() - hs
		_, page := admin.Do("GET", "/hub/ui", nil)
		t.Logf("certificate %s: saves waited %s; %d TLS sessions, %d requests reached central; the log says %q %d times; "+
			"the hub page gives that reason: %v", bad.what, r.waits(cut, time.Now()), handshakes,
			len(calls), bad.says, r.p.Grep(bad.says)-says, strings.Contains(string(page), bad.says))
		if handshakes > 0 || len(calls) > 0 {
			t.Errorf("certificate %s: the hub talked to central through it (%d TLS sessions, %d requests)", bad.what, handshakes, len(calls))
		}
		if r.p.Grep(bad.says) == says {
			t.Errorf("certificate %s: the hub's log never says %q", bad.what, bad.says)
		}
		back := time.Now()
		front.SetCert(good)
		r.p.WaitFor("online", 2*time.Minute, tk.Online)
		r.waitDrained("drained", 5*time.Minute)
		t.Logf("certificate %s: drained %s after the good certificate was back", bad.what, time.Since(back).Round(time.Second))
	}
	time.Sleep(5 * time.Second)
	stop()
	r.finish(5 * time.Minute)
}

// D7, the window before the hub goes offline, step by step: the hub believes it is
// online (in process, it probes only when told), the portal answers, then the uplink
// is cut and the hub serves offline what it kept. A read the hub caches for offline
// use (a program's metadata, as Capture asks for it) and an aggregate value are made
// online, then again while the portal answers. Must hold: offline the hub serves
// central's answer, not the portal's; it shows no value central never got as saved.
func TestD7PortalAnswersKept(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("adversarial scenarios run against a real DHIS2 (HUB_MANIFEST)")
	}
	nurse := s.Web(tk.RoleNurse)
	nurse.Login()
	s.Sync()
	p := s.M.Primary()
	read := "/api/programs/" + p.ID + "?fields=id,name,programStages[id]"
	st, good := nurse.Do("GET", read, nil)
	t.Logf("online, central answers the read: %d %.100s", st, good)
	var problems []string
	for i, kind := range []string{tk.PortalPage, tk.PortalRedirect} {
		s.Net.SetPortal(kind)
		st, b := nurse.Do("GET", read, nil)
		t.Logf("portal %q, the hub still online: the read %d %.80q", kind, st, b)
		var vst int
		var v string
		if ag := s.M.Aggregate; ag != nil {
			v = fmt.Sprint(7200 + i)
			s.DeleteCentralDataValue(ag.DataSet, time.Now().UTC().Format("200601"), ag.Integer, ag.COC)
			var vb []byte
			vst, _, vb = postValue(nurse, s.Env, ag.DataSet, time.Now().UTC().Format("200601"), ag.Integer, ag.COC, v)
			t.Logf("portal %q: an aggregate value %s: %d %.80q", kind, v, vst, vb)
		}
		s.Net.SetPortal("")
		s.Offline()
		st, b = nurse.Do("GET", read, nil)
		t.Logf("portal %q, then offline: the hub serves the read %d %.80q", kind, st, b)
		if st != 200 || string(b) != string(good) && strings.Contains(string(b), "Hotspot") {
			problems = append(problems, fmt.Sprintf("portal %q: offline the hub serves the portal's answer to %s (%d %.60q)", kind, read, st, b))
		}
		if ag := s.M.Aggregate; ag != nil {
			pe := time.Now().UTC().Format("200601")
			hv, _ := hubFormValue(nurse, s.Env, ag.DataSet, pe, ag.Integer, ag.COC)
			cv, _, _ := s.CentralDataValue(ag.DataSet, pe, ag.Integer, ag.COC, ag.DefaultAOC)
			t.Logf("portal %q: the value %s answered %d; offline the hub's form shows %q; central has %q; pending on the hub %d",
				kind, v, vst, hv, cv, s.App.Hub.Store.CountOutbox("pending"))
		}
		s.Online()
		if ag := s.M.Aggregate; ag != nil {
			s.Sync()
			cv, _, _ := s.CentralDataValue(ag.DataSet, time.Now().UTC().Format("200601"), ag.Integer, ag.COC, ag.DefaultAOC)
			if vst/100 == 2 && cv != v {
				problems = append(problems, fmt.Sprintf("portal %q: the value %s answered %d is not on central after the next sync (central has %q)", kind, v, vst, cv))
			}
		}
		nurse.Do("GET", read, nil) // central's answer again, for the next round
	}
	// Found 29 October: before its second failed ping the hub kept the portal's answers
	// as central's. Fixed: an /api/ answer that cannot be central's (HTML for JSON, a
	// redirect to another host) counts as central unreachable (proxy.notCentral).
	for _, p := range problems {
		t.Error(p)
	}
}
