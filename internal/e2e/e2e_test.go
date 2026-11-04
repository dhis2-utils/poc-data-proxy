// End-to-end tests for the acceptance scenarios in spec 13.3, run against the fake
// central (internal/fakecentral). The uplink is cut by making central drop
// connections, so the hub sees real network errors.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/dhis2/facility-hub/internal/devkey"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/app"
	"github.com/dhis2/facility-hub/internal/config"
	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
)

func init() { hubcrypto.ArgonMemory, hubcrypto.ArgonTime = 8*1024, 1 }

type env struct {
	t       *testing.T
	central *fc.Central
	cSrv    *httptest.Server
	dir     string
	app     *app.App
	srv     *httptest.Server
	cfgHook func(*config.Config) // changes the hub's configuration (before start)
	device  devkey.Device        // the machine's TPM: nil for none
	running bool                 // a hub is started: stopped when the test ends
	cleanup bool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, central: fc.New(), dir: t.TempDir()}
	e.central.Seed("SeedTe00001", "SeedEn00001", "Seeded", "NAT-0001",
		map[string]string{"SeedEv00001": fc.Stage1, "SeedEv00003": fc.Stage3})
	e.cSrv = httptest.NewServer(e.central)
	t.Cleanup(e.cSrv.Close)
	e.start()
	return e
}

func (e *env) cfg() config.Config {
	c := config.Default()
	c.TPM = e.device != nil // a fake TPM where a test gives one (device_test.go)
	c.HeartbeatInterval = 0 // tests read the heartbeat after each sync
	c.CentralURL = e.cSrv.URL
	c.DataDir = e.dir
	c.TLS = false
	c.SyncInterval = time.Hour // tests run cycles by hand
	c.PingTimeout = time.Second
	c.ReservedPoolSize = 5
	c.LogRequests = true
	if e.cfgHook != nil {
		e.cfgHook(&c)
	}
	return c
}

// start builds a hub on e.dir (a "boot").
func (e *env) start() {
	devkey.OpenDevice = func() (devkey.Device, error) { return e.device, nil }
	a, err := app.New(e.cfg(), "0.1.0", false, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.app = a
	e.srv = httptest.NewServer(a.Handler)
	e.running = true
	if !e.cleanup {
		// Windows will not delete the test's folder while the hub holds its files.
		e.t.Cleanup(func() {
			if e.running {
				e.stop()
			}
		})
		e.cleanup = true
	}
	e.app.Hub.Monitor.Probe(context.Background(), e.cSrv.URL)
}

// stop is a hub shutdown (the in-memory data key is lost).
func (e *env) stop() {
	e.srv.Close()
	e.app.Close()
	e.running = false
}

func (e *env) offline() {
	e.central.SetDown(true)
	e.app.Hub.Monitor.Probe(context.Background(), e.cSrv.URL)
	e.app.Hub.Monitor.Probe(context.Background(), e.cSrv.URL)
	if e.app.Hub.Online() {
		e.t.Fatal("hub still online")
	}
}

func (e *env) online() {
	e.central.SetDown(false)
	e.app.Hub.Monitor.Probe(context.Background(), e.cSrv.URL)
	if !e.app.Hub.Online() {
		e.t.Fatal("hub still offline")
	}
}

func (e *env) sync() {
	e.t.Helper()
	if err := e.app.Worker.Cycle(context.Background()); err != nil {
		e.t.Fatalf("sync: %v", err)
	}
}

// --- clients ------------------------------------------------------------------------

type client struct {
	e     *env
	http  *http.Client
	user  string
	basic bool
	pw    string // the user's own password, when central gave them one (default fc.Password)
}

func (e *env) web(user string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, user: user, http: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

// android sends Basic auth on every request, like the Android Capture app.
func (e *env) android(user string) *client {
	return &client{e: e, user: user, basic: true, http: &http.Client{}}
}

// androidPw is an Android client of a user with its own password on central.
func (e *env) androidPw(user, pw string) *client {
	c := e.android(user)
	c.pw = pw
	return c
}

func (c *client) do(method, path string, body any) (int, []byte) {
	c.e.t.Helper()
	return c.doHost("", method, path, body)
}

// doHost sends the request with the given Host header ("" for the hub's address).
func (c *client) doHost(host, method, path string, body any) (int, []byte) {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.srv.URL+path, rd)
	if host != "" {
		req.Host = host
	}
	req.Header.Set("Content-Type", "application/json")
	if c.basic {
		pw := c.pw
		if pw == "" {
			pw = fc.Password
		}
		req.SetBasicAuth(c.user, pw)
		req.Header.Set("User-Agent", "okhttp/4.12.0")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// getWith is a GET with extra request headers.
func (c *client) getWith(path string, hdr map[string]string) (int, []byte) {
	c.e.t.Helper()
	req, _ := http.NewRequest("GET", c.e.srv.URL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// postForm posts a form like a browser on the hub's own page: with an Origin header.
func (c *client) postForm(path string, form url.Values) (*http.Response, error) {
	req, _ := http.NewRequest("POST", c.e.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", c.e.srv.URL)
	return c.http.Do(req)
}

func (c *client) login() {
	c.e.t.Helper()
	st, b := c.do("POST", "/api/auth/login", map[string]string{"username": c.user, "password": fc.Password})
	if st != 200 || !strings.Contains(string(b), "SUCCESS") {
		c.e.t.Fatalf("login %s: %d %s", c.user, st, b)
	}
}

func (c *client) json(method, path string, body any, wantStatus int) map[string]any {
	c.e.t.Helper()
	st, b := c.do(method, path, body)
	if st != wantStatus {
		c.e.t.Fatalf("%s %s as %s: got %d want %d: %s", method, path, c.user, st, wantStatus, b)
	}
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

func (e *env) setup() {
	e.t.Helper()
	c := e.web("hub_admin")
	form := url.Values{"step": {"login"}, "central_url": {e.cSrv.URL}, "username": {"hub_admin"}, "password": {fc.Password}}
	resp, err := c.postForm("/hub/setup", form)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "This hub now serves") {
		e.t.Fatalf("setup failed: %s", b)
	}
	e.sync()
}

// --- helpers on payloads ---------------------------------------------------------------

func newPatient(te, enr, first, nationalID string, events map[string]string) map[string]any {
	var evs []any
	for ev, stage := range events {
		evs = append(evs, map[string]any{"event": ev, "programStage": stage, "orgUnit": fc.Facility, "occurredAt": "2026-10-07",
			"status": "ACTIVE", "dataValues": []any{}})
	}
	attrs := []any{map[string]any{"attribute": fc.AttrFirstName, "value": first}}
	if nationalID != "" {
		attrs = append(attrs, map[string]any{"attribute": fc.AttrNationalI, "value": nationalID})
	}
	return map[string]any{"trackedEntities": []any{map[string]any{"trackedEntity": te, "trackedEntityType": fc.TET,
		"orgUnit": fc.Facility, "attributes": attrs,
		"enrollments": []any{map[string]any{"enrollment": enr, "program": fc.Program, "orgUnit": fc.Facility,
			"enrolledAt": "2026-10-07", "occurredAt": "2026-10-07", "status": "ACTIVE", "events": evs}}}}}
}

func event(ev, enr, stage string) map[string]any {
	return map[string]any{"events": []any{map[string]any{"event": ev, "enrollment": enr, "programStage": stage,
		"orgUnit": fc.Facility, "occurredAt": "2026-10-07", "status": "ACTIVE"}}}
}

func uids(list any, key string) []string {
	var out []string
	arr, _ := list.([]any)
	for _, x := range arr {
		out = append(out, fmt.Sprint(x.(map[string]any)[key]))
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func errorCodes(report map[string]any) []string {
	vr, _ := report["validationReport"].(map[string]any)
	return uids(vr["errorReports"], "errorCode")
}

const postSync = "/api/tracker?async=false&importStrategy=CREATE_AND_UPDATE&atomicMode=ALL"

// --- scenarios ---------------------------------------------------------------------------

func TestAcceptance(t *testing.T) {
	e := newEnv(t)
	t.Run("10 setup: a staff member sets up with only a login", func(t *testing.T) {
		e.setup()
		h := e.app.Hub
		if h.Facility() != fc.Facility || h.Catalog().Programs[fc.Program] == nil {
			t.Fatalf("facility %q programs %v", h.Facility(), h.Catalog().ProgramIDs())
		}
		reg := e.central.DataStoreValue("facility-hub-registry", h.HubID())
		if reg["facility"] != fc.Facility || reg["status"] != "active" {
			t.Fatalf("registry entry: %v", reg)
		}
		if len(e.central.DataStoreKeys("facility-hub-status")) != 1 {
			t.Fatal("no heartbeat")
		}
	})

	a, b, clerk, outsider := e.web("nurse_a"), e.web("nurse_b"), e.web("clerk_c"), e.web("outsider_d")
	tablet := e.android("nurse_b")
	t.Run("staff log in once online", func(t *testing.T) {
		for _, c := range []*client{a, b, clerk, outsider} {
			c.login()
		}
		tablet.json("GET", "/api/me", nil, 200) // Android starts with /api/me and Basic auth
		e.sync()
	})

	t.Run("no redirects for API paths (Android adopts redirect targets)", func(t *testing.T) {
		for _, p := range []string{"/api/trackedEntityAttributes?paging=false", "/api/tracker", "/api/tracker/trackedEntities"} {
			if st, _ := tablet.do("GET", p, nil); st >= 300 && st < 400 {
				t.Fatalf("%s answered %d", p, st)
			}
		}
	})

	t.Run("online async write from the tablet is visible to others offline", func(t *testing.T) {
		job := tablet.json("POST", "/api/tracker?atomicMode=OBJECT&importStrategy=CREATE_AND_UPDATE",
			newPatient("AsyncTe0001", "AsyncEn0001", "Async", "NAT-5001", nil), 200)
		id := job["response"].(map[string]any)["id"].(string)
		tablet.json("GET", "/api/tracker/jobs/"+id+"/report", nil, 200)
		e.offline()
		a.json("GET", "/api/tracker/trackedEntities/AsyncTe0001?program="+fc.Program, nil, 200)
		e.online()
	})

	t.Run("1 online pass-through attributed to nurse_a", func(t *testing.T) {
		rep := a.json("POST", postSync, newPatient("OnlineTe001", "OnlineEn001", "Online", "NAT-1001",
			map[string]string{"OnlineEv001": fc.Stage1}), 200)
		if rep["status"] != "OK" {
			t.Fatal(rep)
		}
		te := e.central.Object("OnlineTe001")
		if te == nil || te["createdBy"].(map[string]any)["username"] != "nurse_a" {
			t.Fatalf("central TE: %v", te)
		}
		if o := e.app.Hub.Index.Get("OnlineTe001"); o == nil || o.Origin != store.OriginCentral {
			t.Fatal("online write not stored on hub")
		}
		// Other users see it offline before their own next pull.
		e.offline()
		b.json("GET", "/api/tracker/trackedEntities/OnlineTe001?program="+fc.Program, nil, 200)
		e.online()
	})

	t.Run("2 offline sharing between nurse_a and nurse_b", func(t *testing.T) {
		e.offline()
		rep := a.json("POST", postSync, newPatient("OffTe000001", "OffEn000001", "Amina", "NAT-2001",
			map[string]string{"OffEv000001": fc.Stage1}), 200)
		if len(errorCodes(rep)) != 0 {
			t.Fatal(rep)
		}
		found := b.json("GET", "/api/tracker/trackedEntities?orgUnits="+fc.Facility+"&orgUnitMode=SELECTED&program="+fc.Program+
			"&filter="+fc.AttrFirstName+":like:amin&fields=*,enrollments[*]", nil, 200)
		if got := uids(found["trackedEntities"], "trackedEntity"); len(got) != 1 || got[0] != "OffTe000001" {
			t.Fatalf("nurse_b search: %v", got)
		}
		rep = b.json("POST", postSync, event("OffEv000002", "OffEn000001", fc.Stage2), 200)
		if len(errorCodes(rep)) != 0 {
			t.Fatal(rep)
		}
		evs := a.json("GET", "/api/tracker/events?orgUnit="+fc.Facility+"&program="+fc.Program+"&trackedEntity=OffTe000001", nil, 200)
		if got := uids(evs["events"], "event"); len(got) != 2 {
			t.Fatalf("nurse_a sees %v", got)
		}
	})

	t.Run("2a mixed clients: Android tablet and web", func(t *testing.T) {
		// Android: generated value from the hub's pool, async import, job report.
		st, body := tablet.do("GET", "/api/trackedEntityAttributes/"+fc.AttrTBID+"/generateAndReserve?numberToReserve=2", nil)
		var vals []map[string]any
		if st != 200 || json.Unmarshal(body, &vals) != nil || len(vals) != 2 {
			t.Fatalf("generateAndReserve offline: %d %s", st, body)
		}
		tbid := vals[0]["value"]
		p := newPatient("TabletTe001", "TabletEn001", "Kwame", "NAT-2002", map[string]string{"TabletEv001": fc.Stage1})
		te := p["trackedEntities"].([]any)[0].(map[string]any)
		te["attributes"] = append(te["attributes"].([]any), map[string]any{"attribute": fc.AttrTBID, "value": tbid})
		job := tablet.json("POST", "/api/tracker", p, 200) // async=true by default
		id := job["response"].(map[string]any)["id"].(string)
		rep := tablet.json("GET", "/api/tracker/jobs/"+id+"/report", nil, 200)
		if rep["status"] != "OK" || len(errorCodes(rep)) != 0 {
			t.Fatalf("job report: %v", rep)
		}
		// Web user continues on the Android-registered patient...
		a.json("POST", postSync, event("WebEv000001", "TabletEn001", fc.Stage2), 200)
		// ...and the tablet sees the web user's work.
		evs := tablet.json("GET", "/api/tracker/trackedEntities/TabletTe001?program="+fc.Program+"&fields=*,enrollments[*,events[*]]", nil, 200)
		enr := evs["enrollments"].([]any)[0].(map[string]any)
		if got := uids(enr["events"], "event"); !contains(got, "WebEv000001") {
			t.Fatalf("tablet sees %v", got)
		}
		// Android's offline download: includeDeleted, DESCENDANTS, an updatedAfter
		// watermark from central's clock, and ACCESSIBLE on single-object fetches.
		future := time.Now().Add(48 * time.Hour).UTC().Format("2006-01-02T15:04:05.000")
		down := tablet.json("GET", "/api/tracker/trackedEntities?fields=*&includeDeleted=true&order=createdAt:desc&orgUnitMode=DESCENDANTS&orgUnits="+
			fc.Facility+"&page=1&pageSize=50&paging=true&program="+fc.Program+"&updatedAfter="+future, nil, 200)
		if got := uids(down["trackedEntities"], "trackedEntity"); !contains(got, "TabletTe001") || !contains(got, "OffTe000001") {
			t.Fatalf("android download: %v", got)
		}
		evDown := tablet.json("GET", "/api/tracker/events?fields=*&includeDeleted=true&orgUnit="+fc.Facility+"&orgUnitMode=DESCENDANTS&page=1&pageSize=50&paging=true&program="+
			fc.Program+"&updatedAfter="+future, nil, 200)
		if got := uids(evDown["events"], "event"); !contains(got, "WebEv000001") {
			t.Fatalf("android event download: %v", got)
		}
		tablet.json("GET", "/api/tracker/trackedEntities/TabletTe001?program="+fc.Program+"&includeDeleted=true&orgUnitMode=ACCESSIBLE", nil, 200)
		// A pool value is never handed out twice.
		_, body2 := a.do("GET", "/api/trackedEntityAttributes/"+fc.AttrTBID+"/generate", nil)
		if bytes.Contains(body2, []byte(fmt.Sprint(tbid))) {
			t.Fatal("pool value handed out twice")
		}
		// Android's cached /api/me works offline.
		if st, _ := tablet.do("GET", "/api/me", nil); st != 200 {
			t.Fatalf("/api/me offline: %d", st)
		}
	})

	t.Run("3 stage access: nurse_b does not see stage 3", func(t *testing.T) {
		a.json("POST", postSync, event("OffEv000003", "OffEn000001", fc.Stage3), 200)
		path := "/api/tracker/trackedEntities/OffTe000001?program=" + fc.Program + "&fields=*,enrollments[*,events[*]]"
		stages := func(c *client) []string {
			te := c.json("GET", path, nil, 200)
			return uids(te["enrollments"].([]any)[0].(map[string]any)["events"], "programStage")
		}
		if !contains(stages(a), fc.Stage3) {
			t.Fatal("nurse_a should see stage 3")
		}
		if contains(stages(b), fc.Stage3) {
			t.Fatal("nurse_b sees stage 3")
		}
		// Pulled central data: the seeded stage-3 event is hidden from nurse_b too.
		te := b.json("GET", "/api/tracker/trackedEntities/SeedTe00001?fields=*,enrollments[*,events[*]]", nil, 200)
		if got := uids(te["enrollments"].([]any)[0].(map[string]any)["events"], "event"); contains(got, "SeedEv00003") {
			t.Fatal("nurse_b sees pulled stage 3 event")
		}
		b.json("GET", "/api/tracker/events?orgUnit="+fc.Facility+"&programStage="+fc.Stage3, nil, 403)
	})

	t.Run("4 program access: clerk_c cannot write events", func(t *testing.T) {
		rep := clerk.json("POST", postSync, event("ClerkEv0001", "OffEn000001", fc.Stage1), 409)
		if codes := errorCodes(rep); len(codes) != 1 || codes[0] != "E1095" {
			t.Fatalf("codes %v", codes)
		}
	})

	t.Run("5 facility scope: outsider_d is refused", func(t *testing.T) {
		outsider.json("GET", "/api/tracker/trackedEntities?orgUnits="+fc.Facility+"&program="+fc.Program, nil, 403)
		outsider.json("POST", postSync, event("OutEv000001", "OffEn000001", fc.Stage1), 403)
	})

	t.Run("offline refusals fail closed", func(t *testing.T) {
		a.json("GET", "/api/tracker/trackedEntities?orgUnitMode=ALL&program="+fc.Program, nil, 503)
		a.json("GET", "/api/tracker/trackedEntities?orgUnits="+fc.OtherFacility+"&program="+fc.Program, nil, 503)
		a.json("GET", "/api/tracker/trackedEntities/NoSuchTe001", nil, 503)
		a.json("POST", "/api/tracker?async=false&importStrategy=DELETE", map[string]any{}, 503)
		a.json("GET", "/api/tracker/trackedEntities?orgUnits="+fc.Facility+"&program="+fc.Program+"&followUp=true", nil, 400)
		a.json("POST", "/api/dataValueSets", map[string]any{}, 503)
	})

	t.Run("Capture web app shapes offline", func(t *testing.T) {
		// Versioned paths go to the hub's tracker handler, not the GET cache.
		found := a.json("GET", "/api/42/tracker/trackedEntities?orgUnitMode=ACCESSIBLE&program="+fc.Program+
			"&filter="+fc.AttrFirstName+":like:amin&fields=:all,!relationships,programOwners[orgUnit,program]", nil, 200)
		if got := uids(found["trackedEntities"], "trackedEntity"); len(got) != 1 || got[0] != "OffTe000001" {
			t.Fatalf("ACCESSIBLE search offline: %v", got)
		}
		// Capture's registration payload has no enrollment UID; the hub assigns one.
		p := newPatient("CapTe000001", "", "Capture", "NAT-4001", map[string]string{"": fc.Stage1})
		rep := a.json("POST", "/api/42/tracker?async=false", p, 200)
		if len(errorCodes(rep)) != 0 {
			t.Fatalf("registration without enrollment UID: %v", rep)
		}
		te := a.json("GET", "/api/42/tracker/trackedEntities/CapTe000001?program="+fc.Program+"&fields=*,enrollments[*,events[*]]", nil, 200)
		enr := te["enrollments"].([]any)[0].(map[string]any)
		ev := enr["events"].([]any)[0].(map[string]any)
		if _, ok := ev["dataValues"].([]any); !ok || ev["notes"] == nil || ev["attributeOptionCombo"] != fc.DefaultAOC ||
			ev["occurredAt"] != "2026-10-07T00:00:00.000" {
			t.Fatalf("event shape: %v", ev)
		}
		// atomicMode=ALL (the default) rejects the whole payload on any error.
		bad := newPatient("CapTe000002", "CapEn000002", "Bad", "NAT-4002", map[string]string{"CapEv000002": fc.Stage1})
		bad["trackedEntities"] = append(bad["trackedEntities"].([]any), map[string]any{"trackedEntity": "CapTe000003",
			"trackedEntityType": fc.TET, "orgUnit": fc.OtherFacility})
		rep = a.json("POST", "/api/tracker?async=false", bad, 409)
		if rep["status"] != "ERROR" || e.app.Hub.Index.Get("CapTe000002") != nil {
			t.Fatalf("ALL mode stored part of a failed payload: %v", rep)
		}
		// Generated values on the versioned path come from the pool, not the cache.
		st1, g1 := a.do("GET", "/api/42/trackedEntityAttributes/"+fc.AttrTBID+"/generate?expiration=3", nil)
		st2, g2 := a.do("GET", "/api/42/trackedEntityAttributes/"+fc.AttrTBID+"/generate?expiration=3", nil)
		if st1 != 200 || st2 != 200 || bytes.Equal(g1, g2) {
			t.Fatalf("versioned generate: %d %d same=%v", st1, st2, bytes.Equal(g1, g2))
		}
		if st, body := a.do("GET", "/api/ping", nil); st != 200 || string(body) != "pong" {
			t.Fatalf("ping offline: %d %s", st, body)
		}
	})

	t.Run("7 rejection: unique value conflict blocks dependents only", func(t *testing.T) {
		// Central gets NAT-7777 while the hub is offline, so the hub cannot know.
		e.central.Seed("CentralTe77", "CentralEn77", "Other", "NAT-7777", nil)
		a.json("POST", postSync, newPatient("DupTe000001", "DupEn000001", "Dup", "NAT-7777",
			map[string]string{"DupEv000001": fc.Stage1}), 200)
		a.json("POST", postSync, event("DupEv000002", "DupEn000001", fc.Stage2), 200) // later entry, depends on the first
		b.json("POST", postSync, newPatient("FineTe00001", "FineEn00001", "Fine", "NAT-7778", nil), 200)
	})

	t.Run("6 reconnect: pending data reaches central in order, by author", func(t *testing.T) {
		e.online()
		e.sync()
		order := e.central.Order()
		idx := func(uid string) int {
			for i, u := range order {
				if u == uid {
					return i
				}
			}
			t.Fatalf("%s not on central", uid)
			return -1
		}
		if !(idx("OffTe000001") < idx("OffEn000001") && idx("OffEn000001") < idx("OffEv000001") && idx("OffEv000001") < idx("OffEv000002")) {
			t.Fatalf("out of order: %v", order)
		}
		for uid, want := range map[string]string{"OffTe000001": "nurse_a", "OffEv000002": "nurse_b", "TabletTe001": "nurse_b",
			"WebEv000001": "nurse_a", "OffEv000003": "nurse_a", "FineTe00001": "nurse_b"} {
			o := e.central.Object(uid)
			if o == nil || o["createdBy"].(map[string]any)["username"] != want {
				t.Fatalf("%s createdBy: %v", uid, o)
			}
		}
		if n := e.app.Hub.Store.CountOutbox(store.OutboxPending); n != 0 {
			t.Fatalf("%d entries still pending", n)
		}
	})

	t.Run("7 rejection: reported to central and hub UI", func(t *testing.T) {
		for _, uid := range []string{"DupTe000001", "DupEn000001", "DupEv000001", "DupEv000002"} {
			if e.central.Object(uid) != nil {
				t.Fatalf("%s should be rejected", uid)
			}
			if o := e.app.Hub.Index.Get(uid); o == nil || o.SyncState != store.SyncRejected {
				t.Fatalf("%s not marked rejected", uid)
			}
		}
		issues, _ := e.app.Hub.Store.Issues(false, 100)
		byUID := map[string]string{}
		for _, is := range issues {
			byUID[is.ObjectUID] = string(is.Error)
		}
		if !strings.Contains(byUID["DupTe000001"], "E1064") || !strings.Contains(byUID["DupEv000002"], "blocked by") {
			t.Fatalf("issues: %v", byUID)
		}
		// The heartbeat counts them by kind, program and error code, so central sees the
		// pattern of hundreds without reading each.
		hb, _ := json.Marshal(e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["refused"])
		if !bytes.Contains(hb, []byte(`"code":"E1064","count":1,"kind":"trackedEntity"`)) {
			t.Fatalf("refused changes in the heartbeat: %s", hb)
		}
		keys := e.central.DataStoreKeys("facility-hub-issues")
		if len(keys) != len(issues) {
			t.Fatalf("central issues %v vs hub %d", keys, len(issues))
		}
		for _, k := range keys {
			if s, _ := json.Marshal(e.central.DataStoreValue("facility-hub-issues", k)); bytes.Contains(s, []byte("NAT-7777")) {
				t.Fatalf("issue payload contains the attribute value: %s", s)
			}
			v := e.central.DataStoreValue("facility-hub-issues", k)
			if s, _ := json.Marshal(v); bytes.Contains(s, []byte("Dup")) && !bytes.Contains(s, []byte("DupTe000001")) &&
				!bytes.Contains(s, []byte("DupEn000001")) && !bytes.Contains(s, []byte("DupEv00000")) {
				t.Fatalf("issue payload leaks values: %s", s)
			}
		}
		// Central staff see which attribute was the problem, not the value.
		dup, _ := json.Marshal(e.central.DataStoreValue("facility-hub-issues", e.app.Hub.HubID()+"-"+issueID(issues, "DupTe000001")))
		if want := "Non-unique attribute value `***` for attribute `" + fc.AttrNationalI + "`."; !bytes.Contains(dup, []byte(want)) {
			t.Fatalf("issue for DupTe000001 lacks %q: %s", want, dup)
		}
		admin := e.web("hub_admin")
		admin.login()
		st, page := admin.do("GET", "/hub/ui", nil)
		if st != 200 || !bytes.Contains(page, []byte("DupTe000001")) || !bytes.Contains(page, []byte("E1064")) || bytes.Contains(page, []byte("NAT-7777")) {
			t.Fatalf("admin page: %d", st)
		}
		// Staff assigned to the facility run the hub; no one else opens its page.
		if st, _ := a.do("GET", "/hub/ui", nil); st != 200 {
			t.Fatalf("nurse_a on admin page: %d", st)
		}
		if st, _ := e.web("outsider_d").do("GET", "/hub/ui", nil); st == 200 {
			t.Fatal("a user of another facility opened the admin page")
		}
	})

	t.Run("401 on push flags the user", func(t *testing.T) {
		e.offline()
		a.json("POST", postSync, event("RevokedEv01", "OffEn000001", fc.Stage1), 200)
		e.central.RevokeTokensOf("nurse_a")
		e.online()
		e.app.Worker.Cycle(context.Background())
		u, _ := e.app.Hub.Store.UserByName("nurse_a")
		if !u.NeedsLogin || e.app.Hub.Store.CountOutbox(store.OutboxPending) != 1 {
			t.Fatalf("needsLogin=%v pending=%d", u.NeedsLogin, e.app.Hub.Store.CountOutbox(store.OutboxPending))
		}
		a.login() // online login creates a new PAT
		e.sync()
		if e.central.Object("RevokedEv01") == nil {
			t.Fatal("entry not pushed after re-login")
		}
	})

	t.Run("8 offline login after reboot", func(t *testing.T) {
		e.stop()
		e.central.SetDown(true)
		e.start()
		e.offline()
		h := e.app.Hub
		if h.Store.Unlocked() || h.Index.Len() != 0 {
			t.Fatal("store readable before login")
		}
		// Unknown user and wrong password get the same answer offline (review M3).
		stranger := e.web("someone_new")
		st, body := stranger.do("POST", "/api/auth/login", map[string]string{"username": "someone_new", "password": "x"})
		wrong := e.web("nurse_b")
		st2, body2 := wrong.do("POST", "/api/auth/login", map[string]string{"username": "nurse_b", "password": "wrong"})
		if st != 401 || st2 != 401 || !bytes.Equal(body, body2) {
			t.Fatalf("unknown user %d %s vs wrong password %d %s", st, body, st2, body2)
		}
		b = e.web("nurse_b")
		b.login()
		if !h.Store.Unlocked() {
			t.Fatal("not unlocked")
		}
		b.json("GET", "/api/tracker/trackedEntities/OffTe000001?program="+fc.Program, nil, 200)
		// Android Basic auth works offline for a known user, too.
		e.android("nurse_a").json("GET", "/api/tracker/trackedEntities/OffTe000001", nil, 200)
	})

	t.Run("9 encryption: no patient data in any file on disk", func(t *testing.T) {
		e.stop()
		files, _ := filepath.Glob(filepath.Join(e.dir, "*"))
		if len(files) < 2 {
			t.Fatalf("files: %v", files)
		}
		// Values, search terms and secrets, and also what the index used to keep in
		// plaintext (review H1): which tracked entities exist, in which program, at
		// which facility, and when.
		needles := []string{"Amina", "amin", "Kwame", "Seeded", "NAT-2001", "NAT-0001", fc.Password, "d2p_",
			"OffTe000001", "SeedTe00001", "SeedEv00001", fc.Program, fc.Stage1, "2026-10-07"}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for _, needle := range needles {
				// The request log has today's date in its timestamps.
				if bytes.Contains(data, []byte(needle)) && !(filepath.Base(f) == "requests.log" && needle == "2026-10-07") {
					t.Fatalf("%q found in plain text in %s", needle, filepath.Base(f))
				}
			}
		}
	})
}

func TestResetHub(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	a.json("POST", postSync, newPatient("ResetTe0001", "ResetEn0001", "Reset", "NAT-3001", nil), 200)
	e.online()
	oldID := e.app.Hub.HubID()
	admin := e.web("hub_admin")
	admin.login()
	resp, err := admin.postForm("/hub/ui/reset", url.Values{"confirm": {"RESET"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if e.central.Object("ResetTe0001") == nil {
		t.Fatal("outbox not pushed before reset")
	}
	if e.app.Hub.Mode() != "setup" || e.app.Hub.HubID() == oldID || e.app.Hub.Index.Len() != 0 {
		t.Fatalf("mode %s id %s", e.app.Hub.Mode(), e.app.Hub.HubID())
	}
	if reg := e.central.DataStoreValue("facility-hub-registry", oldID); reg["status"] != "retired" {
		t.Fatalf("old registry entry: %v", reg)
	}
}

// Review fix: a reset by any staff member does not silently drop changes central
// refused: they need a second confirmation, and the registry says who reset.
func TestResetKeepsRefusedChanges(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	e.central.Seed("CentralTe78", "CentralEn78", "Other", "NAT-7878", nil)
	a.json("POST", postSync, newPatient("DupTe000078", "DupEn000078", "Dup", "NAT-7878", nil), 200)
	e.online()
	e.sync()
	if e.app.Hub.Store.CountOutbox(store.OutboxRejected)+e.app.Hub.Store.CountOutbox(store.OutboxPartial) == 0 {
		t.Fatal("no refused change to keep")
	}
	oldID := e.app.Hub.HubID()
	reset := func(confirm string) {
		t.Helper()
		resp, err := a.postForm("/hub/ui/reset", url.Values{"confirm": {confirm}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	reset("RESET")
	if e.app.Hub.Mode() != "active" || e.app.Hub.HubID() != oldID {
		t.Fatal("reset with refused changes and only RESET")
	}
	reset("RESET AND DISCARD")
	if e.app.Hub.Mode() != "setup" {
		t.Fatalf("mode %s after RESET AND DISCARD", e.app.Hub.Mode())
	}
	if reg := e.central.DataStoreValue("facility-hub-registry", oldID); reg["status"] != "retired" || reg["retiredByUser"] != "nurse_a" {
		t.Fatalf("old registry entry: %v", reg)
	}
}

// Review fix: the heartbeat counts refused changes that are still unresolved; one
// fixed and sent again no longer counts.
func TestRefusedCountsClearWhenFixed(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	e.central.Seed("CentralTe79", "CentralEn79", "Other", "NAT-7979", nil)
	a.json("POST", postSync, newPatient("DupTe000079", "DupEn000079", "Dup", "NAT-7979", nil), 200)
	e.online()
	e.sync()
	refused := func() string {
		b, _ := json.Marshal(e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["refused"])
		return string(b)
	}
	if !strings.Contains(refused(), "E1064") {
		t.Fatalf("refused: %s", refused())
	}
	// The issues name their author, so the supervisor knows whom to tell, and an
	// enrollment's its person, for the app's link to Capture.
	issues := map[string]map[string]any{}
	for _, k := range e.central.DataStoreKeys("facility-hub-issues") {
		v := e.central.DataStoreValue("facility-hub-issues", k)
		issues[fmt.Sprint(v["objectUid"])] = v
	}
	te, enr := issues["DupTe000079"], issues["DupEn000079"]
	if user, _ := te["user"].(string); te["username"] != "nurse_a" || user == "" {
		t.Fatalf("issue of the person: %v", te)
	}
	if enr["username"] != "nurse_a" || enr["trackedEntity"] != "DupTe000079" {
		t.Fatalf("issue of the enrollment: %v", enr)
	}
	e.offline() // fixed at the facility, sent through the hub
	a.json("POST", postSync, newPatient("DupTe000079", "DupEn000079", "Dup", "NAT-8080", nil), 200)
	e.online()
	e.sync()
	if e.central.Object("DupTe000079") == nil {
		t.Fatal("the fixed person did not reach central")
	}
	if r := refused(); r != "null" {
		t.Fatalf("refused after the fix: %s", r)
	}
	// Resolved issues leave central's dataStore, so it holds what is stuck now, not every
	// refusal ever.
	for _, k := range e.central.DataStoreKeys("facility-hub-issues") {
		if strings.HasPrefix(k, e.app.Hub.HubID()+"-") {
			t.Errorf("a resolved issue stays on central: %s", k)
		}
	}
	if n := e.app.Hub.Store.CountIssues(); n != 0 {
		t.Errorf("%d resolved issues stay on the hub", n)
	}
}

// The hub serves the org unit the user sets it up for: their only one, or the one
// they choose among theirs, at whatever level (a ward or a community can be a place of
// its own). There is no facility level to configure.
func TestSetupChoosesAmongOwnOrgUnits(t *testing.T) {
	login := func(e *env, user string) string {
		t.Helper()
		resp, err := e.web(user).postForm("/hub/setup", url.Values{"step": {"login"}, "central_url": {e.cSrv.URL},
			"username": {user}, "password": {fc.Password}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	e := newEnv(t)
	if b := login(e, "multi_g"); e.app.Hub.Mode() != "setup" || !strings.Contains(b, fc.Facility) || !strings.Contains(b, fc.OtherFacility) {
		t.Fatalf("a user of two org units is not offered both: mode %s", e.app.Hub.Mode())
	}
	e = newEnv(t)
	login(e, "district_e")
	if e.app.Hub.Facility() != fc.District {
		t.Fatalf("a user of one org unit: the hub serves %q, want %q", e.app.Hub.Facility(), fc.District)
	}
}

// With the facility-hub namespaces protected for a "hub owners" group, a staff
// member outside it cannot set the hub up: they are told to ask to be added, and the
// hub is left as it was, so a member can set it up next.
func TestSetupNeedsHubOwnersGroup(t *testing.T) {
	e := newEnv(t)
	e.central.HubWriters = map[string]bool{"hub_admin": true}
	resp, err := e.web("nurse_a").postForm("/hub/setup", url.Values{"step": {"login"}, "central_url": {e.cSrv.URL},
		"username": {"nurse_a"}, "password": {fc.Password}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e.app.Hub.Mode() != "setup" || !strings.Contains(string(b), "hub owners") {
		t.Fatalf("mode %s; page: %.300s", e.app.Hub.Mode(), b)
	}
	if n := e.central.TokensOf("nurse_a"); n != 0 {
		t.Errorf("the refused setup left %d tokens", n)
	}
	e.setup() // a member of the group
}

// A refused object that changes on central afterwards takes central's version, and
// the issue is then resolved; one central has not changed since keeps the hub's
// refused copy (docs/FINDINGS.md, "Refused changes").
func TestCentralNewerReplacesRefused(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.offline()
	e.central.Seed("CentralTe81", "CentralEn81", "Other", "NAT-8181", nil)
	a.json("POST", postSync, newPatient("DupTe000081", "DupEn000081", "Dup", "NAT-8181", nil), 200)
	e.online()
	e.sync()
	if o := e.app.Hub.Index.Get("DupTe000081"); o == nil || o.SyncState != store.SyncRejected {
		t.Fatal("not refused")
	}
	time.Sleep(1100 * time.Millisecond)                                    // a change after the refusal, in central's whole seconds
	e.central.Seed("DupTe000081", "DupEn000081", "Fixed", "NAT-8282", nil) // fixed on central
	e.sync()
	o := e.app.Hub.Index.Get("DupTe000081")
	if o == nil || o.SyncState != store.SyncSynced || !strings.Contains(fmt.Sprint(e.app.Hub.Index.Full(o)), "Fixed") {
		t.Fatalf("central's newer version did not replace the refused copy: %+v", o)
	}
	if r, _ := json.Marshal(e.central.DataStoreValue("facility-hub-status", e.app.Hub.HubID())["refused"]); string(r) != "null" {
		t.Errorf("still counted as refused: %s", r)
	}
}

func TestCentralOlderKeepsRefused(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("CentralTe83", "CentralEn83", "Kept", "NAT-8383", nil)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.sync()
	e.offline()
	e.central.Seed("CentralTe84", "CentralEn84", "Other", "NAT-8484", nil) // the hub cannot know
	te := newPatient("CentralTe83", "CentralEn83", "Kept", "NAT-8484", nil)["trackedEntities"].([]any)[0].(map[string]any)
	delete(te, "enrollments")
	a.json("POST", postSync, map[string]any{"trackedEntities": []any{te}}, 200) // a duplicate: refused
	e.online()
	e.sync()
	a.do("GET", "/api/tracker/trackedEntities/CentralTe83?program="+fc.Program+"&fields=*", nil) // central's older version
	o := e.app.Hub.Index.Get("CentralTe83")
	if o == nil || o.SyncState != store.SyncRejected || !strings.Contains(fmt.Sprint(e.app.Hub.Index.Full(o)), "NAT-8484") {
		t.Fatalf("central's older version replaced the refused copy: %+v", o)
	}
}

// TestOfflineAppShellAndConditionalGets: on 2.42+ apps open at /apps/<app>, a page
// without a file extension; offline it is served to any user, like other app files.
// A browser that holds another user's copy asks conditionally: the hub must still
// get (and cache) a full answer for this user.
func TestOfflineAppShellAndConditionalGets(t *testing.T) {
	e := newEnv(t)
	e.setup()
	nurse, clerk := e.web("nurse_a"), e.web("clerk_c")
	nurse.login()
	clerk.login()
	if st, _ := nurse.do("GET", "/apps/aggregate-data-entry", nil); st != 200 {
		t.Fatalf("online shell page: %d", st)
	}
	if st, _ := clerk.getWith("/api/42/userSettings", map[string]string{"If-None-Match": `"fake-settings"`}); st != 200 {
		t.Fatalf("conditional GET through the hub: %d, want 200 (the hub asks without the condition)", st)
	}
	e.offline()
	if st, b := clerk.do("GET", "/apps/aggregate-data-entry", nil); st != 200 || !strings.Contains(string(b), "global shell") {
		t.Fatalf("offline, a user who never opened the shell page: %d %s", st, b)
	}
	if st, b := clerk.do("GET", "/api/42/userSettings", nil); st != 200 || !strings.Contains(string(b), "keyUiLocale") {
		t.Fatalf("offline userSettings for the clerk: %d %s", st, b)
	}
}

// A user whose copy the hub holds keeps getting 304s from central: conditions are
// dropped only when the hub has nothing for this user to answer offline with.
func TestConditionalGetWithCachedCopy(t *testing.T) {
	e := newEnv(t)
	e.setup()
	nurse := e.web("nurse_a")
	nurse.login()
	if st, _ := nurse.do("GET", "/api/42/userSettings", nil); st != 200 {
		t.Fatalf("first GET: %d", st)
	}
	if st, _ := nurse.getWith("/api/42/userSettings", map[string]string{"If-None-Match": `"fake-settings"`}); st != 304 {
		t.Fatalf("conditional GET with the hub holding a copy: %d, want 304", st)
	}
}

func TestOfflineStaticCache(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	e.web("nurse_b").login() // nurse_b's first login is online
	if st, _ := a.do("GET", "/dhis-web-capture/index.html", nil); st != 200 {
		t.Fatal(st)
	}
	a.do("GET", "/api/programs/"+fc.Program+"?fields=id", nil) // metadata: nurse_a's own
	a.do("GET", "/api/me?fields=id", nil)                      // user-specific, never shared
	e.offline()
	b := e.web("nurse_b")
	b.login() // offline login; nurse_b never loaded the app through the hub
	st, body := b.do("GET", "/dhis-web-capture/index.html", nil)
	if st != 200 || !strings.Contains(string(body), "capture app") {
		t.Fatalf("static file offline: %d %s", st, body)
	}
	// Metadata is answered per user on central (sharing, language): never another
	// user's copy (TestPerUserMetadataNotShared).
	if st, _ := b.do("GET", "/api/programs/"+fc.Program+"?fields=id", nil); st != 503 {
		t.Fatalf("nurse_b got nurse_a's metadata offline: %d", st)
	}
	if st, _ := b.do("GET", "/api/apps?fields=id", nil); st != 503 {
		t.Fatalf("uncached API offline should need connection, got %d", st)
	}
	// /api/me variants come from nurse_b's own /api/me stored at login, never nurse_a's.
	st, body = b.do("GET", "/api/42/me.json?fields=id,username", nil)
	if st != 200 || !strings.Contains(string(body), "nurse_b") || strings.Contains(string(body), "nurse_a") {
		t.Fatalf("me offline: %d %s", st, body)
	}
}

// Validation results depend on the form's values, which may change offline: the
// hub never replays an online answer, it says "needs connection" (adversarial I6c:
// the Data Entry app showed "No validation alerts" for values central flags).
func TestValidationNeedsConnectionOffline(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a := e.web("nurse_a")
	a.login()
	for _, path := range []string{"/api/validation/dataSet/" + fc.AggDataSet + "?pe=202610&ou=" + fc.Facility,
		"/api/42/validation/dataSet/" + fc.AggDataSet + "?pe=202610&ou=" + fc.Facility} {
		if st, b := a.do("GET", path, nil); st != 200 {
			t.Fatalf("online %s: %d %s", path, st, b)
		}
		e.offline()
		if st, b := a.do("GET", path, nil); st != 503 || !strings.Contains(string(b), "needs connection") {
			t.Errorf("offline %s: %d %s, want 503 needs connection", path, st, b)
		}
		e.online()
	}
}

func issueID(issues []*store.Issue, uid string) string {
	for _, is := range issues {
		if is.ObjectUID == uid {
			return fmt.Sprint(is.ID)
		}
	}
	return ""
}
