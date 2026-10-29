package adversarial

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// payloadCase is one request a client sends to break the hub.
type payloadCase struct {
	what        string
	method      string
	path        string
	contentType string
	body        func() []byte
}

// bigEvents is valid JSON of about mb MB: unit repeated in an events list.
func bigEvents(mb int, unit string) []byte {
	var b bytes.Buffer
	b.WriteString(`{"events":[`)
	for i := 0; b.Len() < mb<<20; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(unit)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

// badEvent is an event of a program central does not have.
func (r *netRun) badEvent() string {
	return `{"event":"` + tk.UID() + `","program":"NoSuchProg1","programStage":"NoSuchStag1","orgUnit":"` + r.e.M.Facility.ID +
		`","occurredAt":"2026-10-01","status":"ACTIVE","dataValues":[{"dataElement":"` + r.de + `","value":"1"}]}`
}

// sendRaw sends a request with this body as the user's Android client (Basic auth).
func (r *netRun) sendRaw(user, method, path, ct string, body []byte) (int, string, string, time.Duration, error) {
	start := time.Now()
	req, _ := http.NewRequest(method, r.e.Srv.URL+path, bytes.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.SetBasicAuth(user, r.e.M.Password)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return 0, "", "", time.Since(start), err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return resp.StatusCode, resp.Header.Get("X-Hub-Mode"), string(b), time.Since(start), nil
}

// sendCases sends each case online, then offline, and returns what broke I5: a hub
// error (a 5xx of the hub's own; central's, passed through online, are central's), a
// dropped connection, an acceptance, the hub not answering a normal read after, or a
// body over the limit forwarded whole. accepted lists the cases the hub took.
func (r *netRun) sendCases(cases []payloadCase, user string, read func() int) (problems, accepted []string) {
	t := r.t
	for _, down := range []bool{false, true} {
		r.e.Net.SetDown(down)
		if down {
			r.p.WaitFor("offline", time.Minute, tk.Offline)
		} else {
			r.p.WaitFor("online", 2*time.Minute, tk.Online)
		}
		for _, c := range cases {
			var body []byte
			if c.body != nil {
				body = c.body()
			}
			r.e.Net.Calls()
			rss := r.p.RSSMB()
			st, mode, b, d, err := r.sendRaw(user, c.method, c.path, c.contentType, body)
			var forwarded int64
			for _, x := range r.e.Net.Calls() {
				if x.Method == c.method && !strings.HasSuffix(x.URL.Path, "/ping") {
					forwarded = max(forwarded, x.Sent)
				}
			}
			t.Logf("offline=%v %s (%d bytes): %d (X-Hub-Mode %q) in %s %v %.150q; forwarded %d bytes; RSS %.0f → %.0f MB, peak %.0f MB",
				down, c.what, len(body), st, mode, d.Round(time.Millisecond), err, b, forwarded, rss, r.p.RSSMB(), r.p.PeakRSSMB())
			if !r.p.Running() {
				t.Fatalf("the hub process died on %s", c.what)
			}
			hubs := down || mode != "" || !strings.Contains(b, "httpStatusCode") && !strings.Contains(b, "Apache Tomcat")
			switch {
			case err != nil:
				problems = append(problems, fmt.Sprintf("offline=%v %s: %v", down, c.what, err))
			case st >= 500 && hubs && !(st == 503 && down):
				problems = append(problems, fmt.Sprintf("offline=%v %s: %d %.100s", down, c.what, st, b))
			case st/100 == 2 && c.method != "GET":
				accepted = append(accepted, fmt.Sprintf("offline=%v %s: %d", down, c.what, st))
			}
			if len(body) > 64<<20 && forwarded > 64<<20 {
				problems = append(problems, fmt.Sprintf("%s forwarded to central whole (%d bytes)", c.what, forwarded))
			}
			if st := read(); st != 200 {
				problems = append(problems, fmt.Sprintf("after %s the hub does not answer a normal read: %d", c.what, st))
			}
		}
	}
	r.e.Net.SetDown(false)
	r.p.WaitFor("online", 2*time.Minute, tk.Online)
	return
}

// I8c: clients send malformed payloads, online and offline, while a writer saves.
// Must hold: each is refused with a 4xx (never a 5xx of the hub's, a hang or a crash),
// the hub keeps serving everyone else, nothing of it lands on central, and a body over
// the limit is not forwarded to central (I7). Large payloads within the limit:
// TestI8cLargePayloads.
func TestI8cMalformedPayloads(t *testing.T) {
	r := netStart(t)
	nurse := r.e.Android(tk.RoleNurse)
	web := r.e.Web(tk.RoleNurse)
	web.Login()
	p := r.prog
	stop := r.write(time.Second)
	defer stop()
	huge := strings.Repeat("9", 1<<20)
	cases := []payloadCase{
		{"truncated JSON", "POST", tk.SyncImport, "application/json", func() []byte { return []byte(`{"trackedEntities":[{"trackedEntity":"`) }},
		{"not JSON", "POST", tk.SyncImport, "application/json", func() []byte { return []byte("\x00\x01\x02 this is not json \xff\xfe") }},
		{"wrong types", "POST", tk.SyncImport, "application/json", func() []byte {
			return []byte(`{"trackedEntities":"x","events":[{"event":5,"dataValues":{"a":1}}],"enrollments":[null,1,"s"]}`)
		}},
		{"deep nesting", "POST", tk.SyncImport, "application/json", func() []byte {
			return []byte(`{"events":` + strings.Repeat("[", 200000) + strings.Repeat("]", 200000) + `}`)
		}},
		{"bad identifiers", "POST", tk.SyncImport, "application/json", func() []byte {
			return []byte(`{"events":[{"event":"../../../etc/passwd","program":"` + p.ID + `","programStage":"'; DROP TABLE objects;--",` +
				`"orgUnit":"` + r.e.M.Facility.ID + `","occurredAt":"2026-13-45","status":"NOPE"}]}`)
		}},
		{"a 1 MB value for a number", "POST", tk.SyncImport, "application/json", func() []byte {
			pr := r.e.M.NewPerson(p, "Zqhuge"+strings.ToLower(tk.UID()[:5])) // not the run's tag: not in the ledger
			te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
			te["enrollments"].([]any)[0].(map[string]any)["events"] = []any{map[string]any{"event": tk.UID(), "program": p.ID,
				"programStage": r.stage, "orgUnit": r.e.M.Facility.ID, "occurredAt": "2026-10-01", "status": "ACTIVE",
				"dataValues": []any{map[string]any{"dataElement": r.de, "value": huge}}}}
			b, _ := json.Marshal(pr.Payload)
			return b
		}},
		{"a 100 kB query", "GET", "/api/tracker/trackedEntities?program=" + p.ID + "&filter=" + strings.Repeat("a", 100000), "", nil},
		{"an aggregate value of 1 MB", "POST", "/api/dataValues?" + aggQuery(r, huge), "", nil},
		{"multipart garbage", "POST", "/api/dataValues", "multipart/form-data; boundary=xyz", func() []byte {
			return []byte("--xyz\r\nContent-Disposition: form-data; name=\"de\"\r\n\r\n" + strings.Repeat("\x00", 4096) + "\r\n--xy")
		}},
	}
	read := func() int { st, _, _ := web.DoHeader("GET", "/api/me?fields=id"); return st }
	problems, accepted := r.sendCases(cases, nurse.User, read)

	// A body sent a byte a second must not hold the hub up for others.
	slow := make(chan error, 1)
	go func() {
		pr, pw := io.Pipe()
		go func() {
			for i := 0; i < 60; i++ {
				if _, err := pw.Write([]byte("{")); err != nil {
					return
				}
				time.Sleep(time.Second)
			}
			pw.Close()
		}()
		req, _ := http.NewRequest("POST", r.e.Srv.URL+tk.SyncImport, pr)
		req.SetBasicAuth(nurse.User, r.e.M.Password)
		resp, err := (&http.Client{Timeout: 3 * time.Minute}).Do(req)
		if err == nil {
			resp.Body.Close()
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		slow <- err
	}()
	var reads []int
	for start := time.Now(); time.Since(start) < 20*time.Second; time.Sleep(time.Second) {
		if st := read(); st != 200 {
			problems = append(problems, fmt.Sprintf("a read during a slow upload: %d", st))
		}
		reads = append(reads, 200)
	}
	t.Logf("a body sent a byte a second: %v; %d reads meanwhile", <-slow, len(reads))
	stop()
	r.finish(5 * time.Minute)
	t.Logf("hub RSS at the end %.0f MB, peak %.0f MB", r.p.RSSMB(), r.p.PeakRSSMB())
	for _, p := range problems {
		t.Errorf("I5: %s", p)
	}
	for _, a := range accepted {
		if a != "offline=true a 1 MB value for a number: 200" {
			t.Errorf("I5: %s: accepted", a)
		}
	}
	if slices.Contains(accepted, "offline=true a 1 MB value for a number: 200") {
		finding(t, "I8c: offline the hub takes a NUMBER value of a million digits, which central refuses on push (E1302): "+
			"refused and kept, but confirmed to the client first")
		t.Errorf("the hub confirmed offline a value central refuses")
	}
}

// I8c, large payloads: one of 60 MB (300 000 events of a program central does not
// have) and one of 70 MB, and six at once of 60, 15 and 3 MB, online and offline (15 MB: just under the limit, imported one at a time).
// Must hold as TestI8cMalformedPayloads, with the hub's memory bounded: it runs on a
// Pi 4 with 1 GB. Found 29 October: within the 64 MB limit, one of 60 MB took the hub
// from 174 to 605 MB, six at once to 5.7 GB. Fixed: /api/tracker takes at most 16 MB
// (core.MaxTrackerBody; Android posts all it holds in one request), and bodies above
// 4 MB are imported one at a time.
func TestI8cLargePayloads(t *testing.T) {
	r := netStart(t)
	nurse := r.e.Android(tk.RoleNurse)
	web := r.e.Web(tk.RoleNurse)
	web.Login()
	cases := []payloadCase{
		{"60 MB of events of an unknown program", "POST", tk.SyncImport, "application/json", func() []byte { return bigEvents(60, r.badEvent()) }},
		{"70 MB", "POST", tk.SyncImport, "application/json", func() []byte { return bigEvents(70, r.badEvent()) }},
	}
	read := func() int { st, _, _ := web.DoHeader("GET", "/api/me?fields=id"); return st }
	problems, accepted := r.sendCases(cases, nurse.User, read)
	for _, down := range []bool{false, true} {
		r.e.Net.SetDown(down)
		if down {
			r.p.WaitFor("offline", time.Minute, tk.Offline)
		}
		for _, mb := range []int{60, 15, 3} {
			var wg sync.WaitGroup
			var mu sync.Mutex
			var statuses []int
			body := bigEvents(mb, r.badEvent())
			rss := r.p.RSSMB()
			start := time.Now()
			for range 6 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					st, _, _, _, _ := r.sendRaw(nurse.User, "POST", tk.SyncImport, "application/json", body)
					mu.Lock()
					statuses = append(statuses, st)
					mu.Unlock()
				}()
			}
			wg.Wait()
			t.Logf("offline=%v six %d MB payloads at once: %v in %s; RSS %.0f → %.0f MB, peak %.0f MB", down, mb, statuses,
				time.Since(start).Round(time.Millisecond), rss, r.p.RSSMB(), r.p.PeakRSSMB())
			if !r.p.Running() {
				t.Fatal("the hub process died on six large payloads at once")
			}
		}
	}
	r.e.Net.SetDown(false)
	for _, p := range append(problems, accepted...) {
		t.Errorf("I5: %s", p)
	}
	if peak := r.p.PeakRSSMB(); peak > 1024 {
		t.Errorf("the hub's memory reached %.0f MB", peak)
	}
}

// aggQuery is a data value query for the manifest's data set with this value.
func aggQuery(r *netRun, v string) string {
	ag := r.e.M.Aggregate
	if ag == nil {
		return ""
	}
	return url.Values{"ds": {ag.DataSet}, "pe": {time.Now().UTC().Format("200601")}, "ou": {r.e.M.Facility.ID},
		"de": {ag.Number}, "co": {ag.COC}, "value": {v}}.Encode()
}

// I9c: many devices start their first download at once, as on the day a facility's
// hub is set up again: 30 Android devices of 20 users (each user's first login
// through the hub among them) download every program at the facility, 50 people a
// page, and the event programs' events, together; first while the hub is online, then
// again all at once while it is offline. Must hold: the hub serves them all; no device
// waits for ever (each request times out after 10 minutes, a device after 20); no
// request fails but the "needs connection" of what the hub cannot answer offline.
func TestI9cFirstDownloadsAtOnce(t *testing.T) {
	r := netStart(t)
	var users []string
	for _, role := range []string{tk.RoleNurse, tk.RoleRestricted, tk.RoleClerk, tk.RoleSearchless} {
		if u, ok := r.e.M.User(role); ok {
			users = append(users, u.Username)
		}
	}
	for _, m := range r.e.M.Matrix {
		if c := m.Dims["capture"]; (c == "facility" || c == "facility+sibling" || c == "district") && len(users) < 20 {
			users = append(users, m.Username)
		}
	}
	const devices = 30
	type result struct {
		user    string
		dur     time.Duration
		pages   int
		people  int
		events  int
		errs    map[int]int
		slowest time.Duration
	}
	run := func(phase string) []result {
		res := make([]result, devices)
		var wg sync.WaitGroup
		start := time.Now()
		rssStart := r.p.RSSMB()
		for i := range devices {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res[i] = result{user: users[i%len(users)], errs: map[int]int{}}
				d := &device{c: r.e.AndroidUser(users[i%len(users)]), base: r.e.Srv.URL, pw: r.e.M.Password}
				d.c.HTTP.Timeout = 10 * time.Minute
				s := time.Now()
				res[i].pages, res[i].people, res[i].events, res[i].slowest = d.firstDownload(r, res[i].errs)
				res[i].dur = time.Since(s)
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		peak := rssStart
		for waiting := true; waiting; {
			select {
			case <-done:
				waiting = false
			case <-time.After(time.Second):
				peak = max(peak, r.p.RSSMB())
				if time.Since(start) > 20*time.Minute {
					t.Fatalf("%s: devices still downloading after 20 minutes", phase)
				}
			}
		}
		var durs []time.Duration
		errs := map[int]int{}
		for _, x := range res {
			durs = append(durs, x.dur)
			for k, v := range x.errs {
				errs[k] += v
			}
		}
		t.Logf("%s: %d devices done in %s; per device %s; hub RSS %.0f MB, peak %.0f MB; failed requests by status %v",
			phase, devices, time.Since(start).Round(time.Second), durStats(durs), rssStart, peak, errs)
		for _, x := range res[:min(len(res), 6)] {
			t.Logf("  %s: %d pages, %d people, %d events in %s, slowest request %s, errors %v", x.user, x.pages, x.people,
				x.events, x.dur.Round(time.Millisecond), x.slowest.Round(time.Millisecond), x.errs)
		}
		return res
	}
	online := run("online")
	r.e.Net.SetDown(true)
	r.p.WaitFor("offline", time.Minute, tk.Offline)
	offline := run("offline")
	r.e.Net.SetDown(false)
	r.p.WaitFor("online", 2*time.Minute, tk.Online)
	// A device asks for every program; a user may not read some, and central refuses
	// those (4xx) online as the hub does offline. A failure is a timeout, a dropped
	// connection or a 5xx (offline, 503 is "needs connection").
	for phase, res := range map[string][]result{"online": online, "offline": offline} {
		for _, x := range res {
			for st, n := range x.errs {
				if st == -1 || (st >= 500 && !(phase == "offline" && st == 503)) {
					t.Errorf("%s: %s: %d requests answered %d", phase, x.user, n, st)
				}
			}
		}
	}
	for i := range offline {
		if offline[i].people != online[i].people || offline[i].events != online[i].events {
			t.Logf("%s: offline %d people and %d events, online %d and %d", offline[i].user, offline[i].people,
				offline[i].events, online[i].people, online[i].events)
		}
	}
}

// device is one Android device's first download through the hub.
type device struct {
	c    *tk.Client
	base string
	pw   string
}

func (d *device) get(path string, errs map[int]int) (int, []byte, time.Duration) {
	start := time.Now()
	st, _, b, err := d.c.Try("GET", path, nil)
	if err != nil {
		st = -1
	}
	if st != 200 {
		errs[st]++
	}
	return st, b, time.Since(start)
}

// firstDownload: the user, the server, then each program's people (or an event
// program's events), 50 a page, as the Android app asks.
func (d *device) firstDownload(r *netRun, errs map[int]int) (pages, people, events int, slowest time.Duration) {
	ou := r.e.M.Facility.ID
	if st, _, _ := d.get("/api/me?fields=id,username,organisationUnits[id],userRoles[id]", errs); st != 200 {
		return
	}
	d.get("/api/system/info", errs)
	fields := "trackedEntity,orgUnit,attributes[attribute,value],enrollments[enrollment,program,status,events[event,programStage,status,dataValues[dataElement,value]]]"
	for _, p := range slices.Concat(r.e.M.Programs, r.e.M.EventPrograms) {
		for page := 1; page < 100; page++ {
			var path, key string
			if p.Event {
				path = "/api/tracker/events?" + url.Values{"program": {p.ID}, "orgUnit": {ou}, "orgUnitMode": {"DESCENDANTS"},
					"includeDeleted": {"true"}, "page": {fmt.Sprint(page)}, "pageSize": {"50"}, "paging": {"true"},
					"fields": {"event,program,programStage,orgUnit,status,occurredAt,attributeOptionCombo,dataValues[dataElement,value]"}}.Encode()
				key = "events"
			} else {
				path = "/api/tracker/trackedEntities?" + url.Values{"program": {p.ID}, "orgUnits": {ou}, "orgUnitMode": {"DESCENDANTS"},
					"includeDeleted": {"true"}, "page": {fmt.Sprint(page)}, "pageSize": {"50"}, "paging": {"true"}, "fields": {fields}}.Encode()
				key = "trackedEntities"
			}
			st, b, dur := d.get(path, errs)
			slowest = max(slowest, dur)
			pages++
			if st != 200 {
				break
			}
			var res map[string]json.RawMessage
			json.Unmarshal(b, &res)
			var items []json.RawMessage
			json.Unmarshal(res[key], &items)
			if p.Event {
				events += len(items)
			} else {
				people += len(items)
			}
			if len(items) < 50 {
				break
			}
		}
	}
	return
}

// I9c, what the burst showed: after a restart the store is locked until a known user
// logs in, and a user the hub has never seen cannot get a key slot meanwhile; their
// device is passed through to central. Here a new user's device asks once before the
// nurse unlocks the store and goes on asking after. Must hold: once the store is
// unlocked, the hub registers the user at their next online request, so the device
// works offline (as it would had its first request come a moment later).
func TestI9cNewDeviceWhileStoreLocked(t *testing.T) {
	s := begin(t)
	var fresh string
	for _, m := range s.M.Matrix {
		if m.Dims["capture"] == "facility" {
			if u, _ := s.App.Hub.Store.UserByName(m.Username); u == nil {
				fresh = m.Username
				break
			}
		}
	}
	if fresh == "" {
		t.Skip("no facility user the hub does not know")
	}
	s.Reboot() // locked until a known user logs in
	dev := s.AndroidUser(fresh)
	st, _ := dev.Do("GET", "/api/me?fields=id", nil)
	t.Logf("%s's device, the store locked: /api/me %d", fresh, st)
	nurse := s.Web(tk.RoleNurse)
	nurse.Login()
	for range 3 {
		st, _ = dev.Do("GET", "/api/me?fields=id", nil)
	}
	u, _ := s.App.Hub.Store.UserByName(fresh)
	t.Logf("after the nurse unlocked the store: the device's /api/me %d; the hub knows %s: %v", st, fresh, u != nil)
	s.Sync()
	s.Offline()
	st, b := dev.Do("GET", "/api/me?fields=id", nil)
	t.Logf("offline: the device's /api/me %d %.150s", st, b)
	if st != 200 {
		finding(t, "I9c: a device whose first request reaches the hub while its store is locked keeps a pass-through "+
			"session: the hub does not register the user when the store is unlocked, and offline refuses the device "+
			"until that session ends (up to 12 hours of use)")
		t.Errorf("offline the hub refuses a user it served online after the store was unlocked: %d", st)
	}
}
