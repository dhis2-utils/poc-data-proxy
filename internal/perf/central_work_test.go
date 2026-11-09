package perf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/store"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// Central's work for one facility (docs/PERFORMANCE.md, "Central's load from one
// facility, three ways"). Each unit of work (a patient visit in a web app, an Android
// sync, one of the hub's cycles) runs alone against a real DHIS2 whose Postgres has
// pg_stat_statements; around it the statistics are reset and read, so the unit's
// database time, statements, rows and blocks are central's work for it. Requests pass
// an unthrottled Netcut, which times each answer (central's time, not the link's).
//
// It needs HUB_MANIFEST (an eRegisters instance set up by clients/eregisters_setup.py
// and seeded by clients/central_load_seed.py) and HUB_CENTRAL_PG (a Postgres URL for
// central's database, pg_stat_statements loaded). HUB_CENTRAL_UNITS picks unit groups
// (idle, web, android, aggregate, hub, via-hub; default all; via-hub runs the web apps,
// and Android and the monthly form if listed, through a hub), HUB_CENTRAL_APPS the web
// apps (default
// capture,eregisters), HUB_CENTRAL_RUNS the repetitions (default 3), HUB_CENTRAL_OUT
// the samples file (JSON lines, appended). PULL_WAIT (seconds) is passed to the web
// driver: eRegisters re-reads 5 minutes before its last pull.
// TestCentralWorkReport turns the samples into the tables.

const visitStage = "K2nxbE9ubSs" // Medical Visit

type dbStat struct {
	Statements, Rows, Blocks, BlocksRead int64
	ExecMs                               float64
}

// sample is one run of one unit.
type sample struct {
	Unit       string             `json:"unit"`
	At         string             `json:"at"`
	DBMs       float64            `json:"dbMs"`       // total_exec_time
	Statements int64              `json:"statements"` // calls
	Rows       int64              `json:"rows"`
	Blocks     int64              `json:"blocks"` // shared_blks_hit + shared_blks_read
	BlocksRead int64              `json:"blocksRead"`
	Requests   int                `json:"requests"`
	ServerMs   float64            `json:"serverMs"` // sum of the answers' times
	Bytes      int64              `json:"bytes"`    // answers
	Sent       int64              `json:"sent"`     // request bodies
	WallMs     float64            `json:"wallMs"`
	Failed     int                `json:"failed"` // answers 400 and up, 404 aside
	Kinds      map[string]int     `json:"kinds"`  // requests by method and path
	KindMs     map[string]float64 `json:"kindMs"` // their server time
}

func psql(t testing.TB, dsn, sql string) string {
	t.Helper()
	out, err := exec.Command("psql", "-X", "-A", "-t", "-q", "-c", sql, dsn).CombinedOutput()
	if err != nil {
		t.Fatalf("psql: %v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func dbReset(t testing.TB, dsn string) { psql(t, dsn, "SELECT pg_stat_statements_reset()") }

func dbRead(t testing.TB, dsn string) dbStat {
	out := psql(t, dsn, `SELECT coalesce(sum(calls),0), coalesce(sum(total_exec_time),0), coalesce(sum(rows),0),
		coalesce(sum(shared_blks_hit+shared_blks_read),0), coalesce(sum(shared_blks_read),0)
		FROM pg_stat_statements WHERE query NOT LIKE '%pg_stat_statements%'`)
	f := strings.Split(out, "|")
	if len(f) != 5 {
		t.Fatalf("pg_stat_statements: %q", out)
	}
	var s dbStat
	s.Statements, _ = strconv.ParseInt(f[0], 10, 64)
	s.ExecMs, _ = strconv.ParseFloat(f[1], 64)
	s.Rows, _ = strconv.ParseInt(f[2], 10, 64)
	s.Blocks, _ = strconv.ParseInt(f[3], 10, 64)
	s.BlocksRead, _ = strconv.ParseInt(f[4], 10, 64)
	return s
}

// meter measures one unit at a time.
type meter struct {
	t     testing.TB
	dsn   string
	out   string
	mu    sync.Mutex
	net   *tk.Netcut
	unit  string
	start time.Time
}

func (m *meter) begin(unit string, net *tk.Netcut) {
	m.mu.Lock()
	defer m.mu.Unlock()
	net.Calls()
	dbReset(m.t, m.dsn)
	m.unit, m.net, m.start = unit, net, time.Now()
}

func (m *meter) end() sample {
	m.mu.Lock()
	defer m.mu.Unlock()
	wall := time.Since(m.start)
	db := dbRead(m.t, m.dsn)
	s := sample{Unit: m.unit, At: time.Now().UTC().Format(time.RFC3339), DBMs: db.ExecMs, Statements: db.Statements,
		Rows: db.Rows, Blocks: db.Blocks, BlocksRead: db.BlocksRead, WallMs: float64(wall.Milliseconds()),
		Kinds: map[string]int{}, KindMs: map[string]float64{}}
	for _, c := range m.net.Calls() {
		s.Requests++
		s.ServerMs += float64(c.Dur.Microseconds()) / 1000
		s.Bytes += c.Bytes
		s.Sent += max(c.Sent, 0)
		if c.Status >= 400 && c.Status != 404 {
			s.Failed++
		}
		k := c.Method + " " + callPath(c.URL.Path)
		s.Kinds[k]++
		s.KindMs[k] += float64(c.Dur.Microseconds()) / 1000
	}
	b, _ := json.Marshal(s)
	f, err := os.OpenFile(m.out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		m.t.Fatal(err)
	}
	f.Write(append(b, '\n'))
	f.Close()
	m.t.Logf("%-40s db %8.1f ms %7d stmts %9d rows %9d blocks | %4d requests, server %8.1f ms, %8.1f KB, %d failed, wall %.1f s",
		s.Unit, s.DBMs, s.Statements, s.Rows, s.Blocks, s.Requests, s.ServerMs, float64(s.Bytes)/1024, s.Failed, s.WallMs/1000)
	return s
}

func (m *meter) measure(unit string, net *tk.Netcut, fn func()) sample {
	m.begin(unit, net)
	fn()
	return m.end()
}

// callPath names a request by its path, identifiers and the API version elided.
func callPath(p string) string {
	p = strings.Replace(p, "/api/43/", "/api/", 1)
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if len(s) == 11 && i > 2 && isUID(s) {
			parts[i] = "{uid}"
		}
	}
	return strings.Join(parts, "/")
}

func isUID(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

type workEnv struct {
	m      *tk.Manifest
	dsn    string
	meter  *meter
	direct *tk.Netcut // the clients' line to central, without a hub
	runs   int
	units  map[string]bool
}

func centralWork(t *testing.T) *workEnv {
	if os.Getenv("HUB_MANIFEST") == "" || os.Getenv("HUB_CENTRAL_PG") == "" {
		t.Skip("set HUB_MANIFEST (an eRegisters instance, clients/central_load_seed.py) and HUB_CENTRAL_PG to measure")
	}
	m, err := tk.LoadManifest(os.Getenv("HUB_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("HUB_CENTRAL_OUT")
	if out == "" {
		out = filepath.Join(t.TempDir(), "samples.jsonl")
	}
	n, err := tk.NewNetcut(m.CentralURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	w := &workEnv{m: m, dsn: os.Getenv("HUB_CENTRAL_PG"), direct: n, runs: 3, units: map[string]bool{}}
	w.meter = &meter{t: t, dsn: w.dsn, out: out}
	if v, _ := strconv.Atoi(os.Getenv("HUB_CENTRAL_RUNS")); v > 0 {
		w.runs = v
	}
	for _, u := range strings.Split(os.Getenv("HUB_CENTRAL_UNITS"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			w.units[u] = true
		}
	}
	return w
}

func (w *workEnv) want(group string) bool { return len(w.units) == 0 || w.units[group] }

// apps are the web apps to drive: HUB_CENTRAL_APPS (e.g. "eregisters") or both.
func (w *workEnv) apps() []string {
	if v := os.Getenv("HUB_CENTRAL_APPS"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"capture", "eregisters"}
}

// --- plain HTTP clients (Android replay, aggregate, central-side changes) --------------

// dev is a device talking to base (central through a Netcut, or a hub) as Android's
// SDK does (PasswordAndCookieAuthenticator): Basic auth until central has given it a
// session cookie, then the cookie. Central checks a password in about 100 ms (BCrypt,
// measured on this instance), a cookie in a few.
type dev struct {
	t          testing.TB
	base, user string
	pass       string
	hc         *http.Client
}

func newDev(t testing.TB, base, user, pass string) *dev {
	jar, _ := cookiejar.New(nil)
	return &dev{t: t, base: base, user: user, pass: pass, hc: &http.Client{Jar: jar, Timeout: 10 * time.Minute}}
}

func (d *dev) do(method, path string, body any) (int, []byte) {
	d.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, d.base+path, rd)
	if len(d.hc.Jar.Cookies(req.URL)) == 0 {
		req.SetBasicAuth(d.user, d.pass)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "okhttp/4.12.0")
	resp, err := d.hc.Do(req)
	if err != nil {
		d.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (d *dev) json(method, path string, body, out any) int {
	d.t.Helper()
	st, b := d.do(method, path, body)
	if out != nil {
		json.Unmarshal(b, out)
	}
	return st
}

// person is a seeded person to visit.
type person struct {
	TE, Enrollment, First, Surname string
}

// people picks n people of the facility at random, as the admin.
func (w *workEnv) people(t testing.TB, n int) []person {
	c := tk.NewCentral(w.m.CentralURL, w.m.AdminUser, w.m.AdminPassword).As(w.m.Users["admin"].Username, w.m.Password)
	var out []person
	for len(out) < n {
		var res struct {
			TrackedEntities []struct {
				TrackedEntity string
				Attributes    []struct{ Attribute, Value string }
				Enrollments   []struct{ Enrollment string }
			}
		}
		q := url.Values{"program": {w.m.Primary().ID}, "orgUnits": {w.m.Facility.ID}, "pageSize": {"50"},
			"page": {strconv.Itoa(1 + rand.IntN(50))}, "fields": {"trackedEntity,attributes[attribute,value],enrollments[enrollment]"}}
		if err := c.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &res); err != nil {
			t.Fatal(err)
		}
		for _, te := range res.TrackedEntities {
			p := person{TE: te.TrackedEntity}
			if len(te.Enrollments) > 0 {
				p.Enrollment = te.Enrollments[0].Enrollment
			}
			for _, a := range te.Attributes {
				switch a.Attribute {
				case "KSq9EyZ8ZFi":
					p.First = a.Value
				case "TWPNbc9O2nK":
					p.Surname = a.Value
				}
			}
			if p.Enrollment != "" && p.First != "" && p.Surname != "" {
				out = append(out, p)
			}
		}
	}
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out[:n]
}

// staffSave saves visits and registrations on central directly as a staff member,
// not measured: the changes other staff make between two syncs.
func (w *workEnv) staffSave(t testing.TB, visits, registrations int) {
	d := newDev(t, w.m.CentralURL, w.m.Users["staff1"].Username, w.m.Password)
	p := w.m.Primary()
	for _, pr := range w.people(t, visits) {
		_, payload := w.m.NewEvent(p, visitStage, pr.Enrollment)
		if st, b := d.do("POST", tk.SyncImport, payload); st != 200 {
			t.Fatalf("visit on central: %d %.500s", st, b)
		}
	}
	for i := 0; i < registrations; i++ {
		pr := w.m.NewPerson(p, "Zz"+strings.ToLower(tk.UID()[:6]), visitStage)
		if st, b := d.do("POST", tk.SyncImport, pr.Payload); st != 200 {
			t.Fatalf("registration on central: %d %.500s", st, b)
		}
	}
}

// --- the units ------------------------------------------------------------------------

// TestCentralWork runs the units; see the comment at the top of the file.
func TestCentralWork(t *testing.T) {
	w := centralWork(t)
	if w.want("idle") {
		t.Run("idle", func(t *testing.T) {
			for i := 0; i < w.runs; i++ {
				w.meter.measure("idle, 60 s", w.direct, func() { time.Sleep(time.Minute) })
			}
		})
	}
	if w.want("web") {
		t.Run("web", func(t *testing.T) {
			ctl := w.control(t, nil)
			for _, app := range w.apps() {
				w.drive(t, app, w.direct.URL(), ctl, "direct", w.m.Users[map[string]string{"capture": "staff3", "eregisters": "staff4"}[app]].Username)
			}
		})
	}
	if w.want("android") {
		t.Run("android", func(t *testing.T) { w.androidUnits(t, w.direct.URL(), w.direct, "direct") })
	}
	if w.want("aggregate") {
		t.Run("aggregate", func(t *testing.T) { w.aggregateUnits(t, w.direct.URL(), w.direct, "direct") })
	}
	if w.want("hub") {
		t.Run("hub", func(t *testing.T) { w.hubUnits(t) })
	}
	if w.want("via-hub") {
		t.Run("via-hub", func(t *testing.T) { w.viaHub(t) })
	}
}

// control serves the driver's marks: /begin?unit= and /end around each unit, and
// /localreads?on= for a hub.
func (w *workEnv) control(t *testing.T, e *tk.Env) string {
	net := w.direct
	if e != nil {
		net = e.Net
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/begin":
			w.meter.begin(r.URL.Query().Get("unit"), net)
		case "/end":
			s := w.meter.end()
			json.NewEncoder(rw).Encode(s)
		case "/day": // a facility-day of other staff's saves, before a pull
			w.staffSave(t, 200, 100)
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// drive runs clients/central_load_visit.py: one browser, the app started once to
// warm its cache, then RUNS app starts, visits and registrations, each measured.
func (w *workEnv) drive(t *testing.T, app, base, ctl, where, user string) {
	t.Helper()
	ppl := w.people(t, w.runs+2)
	pj, _ := json.Marshal(ppl)
	out := filepath.Join(filepath.Dir(w.meter.out), "driver-"+app+"-"+strings.NewReplacer(" ", "-", ",", "").Replace(where))
	os.MkdirAll(out, 0o755)
	cmd := exec.Command("python3", "-I", "../../clients/central_load_visit.py")
	cmd.Env = append(os.Environ(), "BASE_URL="+base, "CONTROL_URL="+ctl, "APP="+app, "WHERE="+where,
		"USER="+user, "PASSWORD="+w.m.Password, "ORG_UNIT="+w.m.Facility.ID, "PROGRAM="+w.m.Primary().ID,
		"PEOPLE="+string(pj), "RUNS="+strconv.Itoa(w.runs), "OUT="+out)
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	os.WriteFile(filepath.Join(out, "result.json"), b, 0o644)
	if err != nil {
		t.Fatalf("central_load_visit.py %s %s: %v\n%s", app, where, err, b)
	}
	var res struct {
		Errors []string `json:"errors"`
		Saves  []struct {
			Unit   string `json:"unit"`
			Status any    `json:"status"`
		} `json:"saves"`
	}
	json.Unmarshal(b, &res)
	for _, s := range res.Saves {
		if st, _ := s.Status.(float64); st != 200 {
			t.Errorf("%s %s: save %v", app, s.Unit, s.Status)
		}
	}
	for _, e := range res.Errors {
		t.Errorf("%s %s: %s", app, where, e)
	}
}

// --- Android -----------------------------------------------------------------------

// Android Capture's requests, approximated from SDK 1.14 (dhis2-android-sdk, its
// *Fields.kt and *Call.kt) and testdata/2.42/capture_android_endpoints.txt: the
// endpoints, filters and paging are the SDK's; the field lists are shortened by hand
// but keep every nested collection the SDK asks for (a program stage's data elements
// in full, a rule's actions, an option's set), which is what makes central read.
const (
	andIdent    = "id,code,name,displayName,created,lastUpdated,deleted"
	andNameable = andIdent + ",shortName,displayShortName,description,displayDescription"
	andStyle    = "style[color,icon]"
	andAccess   = "access[read,data[read,write]]"
	andAttrVals = "attributeValues[value,attribute[id,code,name]]"
	andDE       = andNameable + ",valueType,zeroIsSignificant,aggregationType,formName,domainType,displayFormName," +
		"optionSet[id],categoryCombo[id],fieldMask," + andStyle + "," + andAccess + ",legendSets[id]," + andAttrVals
	andProgram = andNameable + ",version,onlyEnrollOnce,displayIncidentDate,registration,selectEnrollmentDatesInFuture," +
		"dataEntryMethod,ignoreOverdueEvents,selectIncidentDatesInFuture,useFirstStageDuringRegistration," +
		"displayFrontPageList,programType,programTrackedEntityAttributes[" + andNameable + ",mandatory,allowFutureDate," +
		"displayInList,sortOrder,searchable,renderType,program[id],trackedEntityAttribute[id]],relatedProgram[id]," +
		"trackedEntityType[id],categoryCombo[id]," + andAccess + ",programRuleVariables[" + andIdent +
		",useCodeForOptionSet,program[id],programStage[id],dataElement[id],trackedEntityAttribute[id]," +
		"programRuleVariableSourceType]," + andStyle + ",expiryDays,completeEventsExpiryDays,expiryPeriodType," +
		"minAttributesRequiredToSearch,maxTeiCountToReturn,featureType,accessLevel,programSections[" + andIdent +
		",description,program[id],trackedEntityAttributes[id],sortOrder,renderType]," + andAttrVals +
		",displayEnrollmentDateLabel,displayIncidentDateLabel"
	andStage = andIdent + ",description,displayDescription,displayExecutionDateLabel,displayDueDateLabel," +
		"allowGenerateNextVisit,validCompleteOnly,reportDateToUse,openAfterEnrollment,repeatable,featureType,formType," +
		"displayGenerateEventBox,generatedByEnrollmentDate,autoGenerateEvent,sortOrder,hideDueDate,blockEntryForm," +
		"minDaysFromStart,standardInterval,periodType,program,remindCompleted,validationStrategy,enableUserAssignment," +
		"programStageSections[" + andIdent + ",sortOrder,programIndicators[id,program[id]],dataElements[id],renderType]," +
		"programStageDataElements[" + andIdent + ",displayInReports,compulsory,allowProvidedElsewhere,sortOrder," +
		"allowFutureDate,renderType,dataElement[" + andDE + "],programStage[id]]," + andStyle + "," + andAttrVals + "," + andAccess
	andRule = andIdent + ",priority,condition,program[id],programStage[id],programRuleActions[" + andIdent +
		",data,content,location,programRuleActionType,trackedEntityAttribute[id],programIndicator[id]," +
		"programStageSection[id],programStage[id],dataElement[id],option[id],optionGroup[id],displayContent,priority]"
	andIndicator = andNameable + ",displayInForm,expression,dimensionItem,filter,decimals,aggregationType,analyticsType," +
		"program[id],analyticsPeriodBoundaries[id,boundaryTarget,analyticsPeriodBoundaryType,offsetPeriods,offsetPeriodType]," +
		"legendSets[id],categoryCombo[id],attributeCombo[id]"
	andTEA = andNameable + ",pattern,sortOrderInListNoProgram,valueType,expression,programScope,aggregationType," +
		"displayInListNoProgram,generated,displayOnVisitSchedule,orgunitScope,unique,inherit,fieldMask,legendSets[id]," +
		"optionSet[id]," + andStyle + "," + andAccess + ",formName,displayFormName,confidential"
	andOption  = andIdent + ",sortOrder,optionSet[id]," + andStyle
	andDataSet = andNameable + ",periodType,categoryCombo[id],mobile,version,expiryDays,timelyDays," +
		"notifyCompletingUser,openFuturePeriods,fieldCombinationRequired,validCompleteOnly,noValueRequiresComment," +
		"skipOffline,dataElementDecoration,renderAsTabs,renderHorizontally,workflow[id],dataSetElements[dataSet[id]," +
		"dataElement[id],categoryCombo[id]],indicators[id],sections[" + andIdent + ",description,sortOrder,dataSet[id]," +
		"showRowTotals,showColumnTotals,dataElements[id],greyedFields[id]],compulsoryDataElementOperands[id,dataElement[id]," +
		"categoryOptionCombo[id]],dataInputPeriods[period,openingDate,closingDate]," + andAccess + "," + andStyle
	andTE = "trackedEntity,createdAt,updatedAt,createdAtClient,updatedAtClient,orgUnit,trackedEntityType,geometry," +
		"deleted,attributes[attribute,value,createdAt,updatedAt],relationships,programOwners[orgUnit,program,trackedEntity]," +
		"enrollments[enrollment,createdAt,updatedAt,createdAtClient,updatedAtClient,orgUnit,program,enrolledAt,occurredAt," +
		"completedAt,followUp,status,deleted,trackedEntity,geometry,notes,relationships,events[event,enrollment," +
		"createdAt,updatedAt,createdAtClient,updatedAtClient,program,programStage,orgUnit,occurredAt,scheduledAt," +
		"completedAt,status,deleted,trackedEntity,attributeOptionCombo,assignedUser,geometry,notes,relationships," +
		"dataValues[dataElement,storedBy,value,createdAt,updatedAt,providedElsewhere]]]"
)

func idList(ids []string) string { return "[" + strings.Join(ids, ",") + "]" }

func chunks(ids []string, n int) [][]string {
	var out [][]string
	for len(ids) > n {
		out = append(out, ids[:n])
		ids = ids[n:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

type idRef struct {
	ID string `json:"id"`
}

// androidMetadataSync replays the SDK's metadata download for one user: what it asks
// follows from what the previous answers named (the user's programs, their stages'
// option sets, the data sets of the user's org units).
func androidMetadataSync(t testing.TB, d *dev, facility string) {
	get := func(path string, q url.Values, out any) {
		if st := d.json("GET", path+"?"+q.Encode(), nil, out); st != 200 && st != 404 {
			t.Fatalf("android %s: %d", path, st)
		}
	}
	v := func(kv ...string) url.Values {
		q := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			q.Add(kv[i], kv[i+1])
		}
		return q
	}
	get("/api/system/info", v("fields", "serverDate,dateFormat,version,contextPath,systemName"), nil)
	var me struct {
		Programs          []string `json:"programs"`
		DataSets          []string `json:"dataSets"`
		OrganisationUnits []idRef  `json:"organisationUnits"`
	}
	get("/api/me", v("fields", "id,code,name,displayName,created,lastUpdated,birthday,education,gender,jobTitle,surname,"+
		"firstName,introduction,employer,interests,languages,email,phoneNumber,nationality,username,"+
		"userRoles[id,code,name,displayName],organisationUnits[id,path,programs,dataSets],"+
		"teiSearchOrganisationUnits[id,path],dataViewOrganisationUnits[id,path],userGroups[id,name],programs,dataSets"), &me)
	get("/api/me/authorization", nil, nil)
	get("/api/systemSettings", v("key", "keyFlag,keyStyle,keyDefaultBaseMap,keyBingMapsApiKey"), nil)
	get("/api/userSettings", v("fields", "keyUiLocale,keyDbLocale"), nil)
	for _, k := range []string{"ANDROID_SETTINGS_APP/info", "ANDROID_SETTING_APP/general_settings",
		"ANDROID_SETTING_APP/program_settings", "ANDROID_SETTING_APP/dataSet_settings", "APK_DISTRIBUTION/versions",
		"USE_CASES/stockUseCases"} {
		get("/api/dataStore/"+k, nil, nil)
	}
	get("/api/constants", v("fields", andIdent+",value", "paging", "false"), nil)
	get("/api/organisationUnitLevels", v("fields", andIdent+",level", "paging", "false"), nil)
	var ous struct {
		OrganisationUnits []struct {
			Programs []idRef `json:"programs"`
			DataSets []idRef `json:"dataSets"`
		} `json:"organisationUnits"`
	}
	get("/api/organisationUnits", v("fields", andNameable+",path,openingDate,closedDate,level,parent[id],"+
		"programs[id],dataSets[id],ancestors[id,displayName],organisationUnitGroups[id,name,shortName]",
		"filter", "path:like:"+facility, "order", "created:asc", "paging", "true", "page", "1", "pageSize", "500"), &ous)
	progs, sets := map[string]bool{}, map[string]bool{}
	for _, ou := range ous.OrganisationUnits {
		for _, p := range ou.Programs {
			progs[p.ID] = true
		}
		for _, s := range ou.DataSets {
			sets[s.ID] = true
		}
	}
	programs := keys(progs)
	var pr struct {
		Programs []struct {
			TrackedEntityType              idRef `json:"trackedEntityType"`
			ProgramTrackedEntityAttributes []struct {
				TrackedEntityAttribute idRef `json:"trackedEntityAttribute"`
			} `json:"programTrackedEntityAttributes"`
		} `json:"programs"`
	}
	get("/api/programs", v("fields", andProgram, "filter", "id:in:"+idList(programs), "paging", "false"), &pr)
	optionSets, teas, tets := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range pr.Programs {
		tets[p.TrackedEntityType.ID] = true
		for _, a := range p.ProgramTrackedEntityAttributes {
			teas[a.TrackedEntityAttribute.ID] = true
		}
	}
	var ps struct {
		ProgramStages []struct {
			ProgramStageDataElements []struct {
				DataElement struct {
					OptionSet idRef `json:"optionSet"`
				} `json:"dataElement"`
			} `json:"programStageDataElements"`
		} `json:"programStages"`
	}
	get("/api/programStages", v("fields", andStage, "filter", "program.id:in:"+idList(programs), "paging", "false"), &ps)
	for _, s := range ps.ProgramStages {
		for _, de := range s.ProgramStageDataElements {
			optionSets[de.DataElement.OptionSet.ID] = true
		}
	}
	for _, c := range chunks(programs, 64) {
		get("/api/programRules", v("fields", andRule, "filter", "program.id:in:"+idList(c), "paging", "false"), nil)
	}
	for _, c := range chunks(programs, 50) {
		get("/api/programIndicators", v("fields", andIndicator, "filter", "displayInForm:eq:true",
			"filter", "program.id:in:"+idList(c), "paging", "false"), nil)
	}
	get("/api/trackedEntityTypes", v("fields", andNameable+",featureType,trackedEntityTypeAttributes[trackedEntityType[id],"+
		"trackedEntityAttribute[id],displayInList,mandatory,searchable],"+andStyle+","+andAccess,
		"filter", "id:in:"+idList(keys(tets)), "paging", "false"), nil)
	var tea struct {
		TrackedEntityAttributes []struct {
			OptionSet idRef `json:"optionSet"`
		} `json:"trackedEntityAttributes"`
	}
	for _, c := range chunks(keys(teas), 130) {
		get("/api/trackedEntityAttributes", v("fields", andTEA, "filter", "id:in:"+idList(c), "paging", "false"), &tea)
		for _, a := range tea.TrackedEntityAttributes {
			optionSets[a.OptionSet.ID] = true
		}
	}
	get("/api/relationshipTypes", v("fields", andIdent+",bidirectional,fromToName,toFromName,fromConstraint,toConstraint,"+
		andAccess, "paging", "false"), nil)
	// Aggregate metadata: the data sets of the user's org units and their data elements.
	var ds struct {
		DataSets []struct {
			CategoryCombo   idRef `json:"categoryCombo"`
			DataSetElements []struct {
				DataElement idRef `json:"dataElement"`
			} `json:"dataSetElements"`
		} `json:"dataSets"`
	}
	get("/api/dataSets", v("fields", andDataSet, "filter", "id:in:"+idList(keys(sets)), "paging", "false"), &ds)
	des, combos := map[string]bool{}, map[string]bool{}
	for _, s := range ds.DataSets {
		combos[s.CategoryCombo.ID] = true
		for _, e := range s.DataSetElements {
			des[e.DataElement.ID] = true
		}
	}
	var del struct {
		DataElements []struct {
			OptionSet     idRef `json:"optionSet"`
			CategoryCombo idRef `json:"categoryCombo"`
		} `json:"dataElements"`
	}
	for _, c := range chunks(keys(des), 250) {
		get("/api/dataElements", v("fields", andDE, "filter", "id:in:"+idList(c), "paging", "false"), &del)
		for _, e := range del.DataElements {
			optionSets[e.OptionSet.ID] = true
			combos[e.CategoryCombo.ID] = true
		}
	}
	var cc struct {
		CategoryCombos []struct {
			Categories []idRef `json:"categories"`
		} `json:"categoryCombos"`
	}
	get("/api/categoryCombos", v("fields", andIdent+",isDefault,categories[id],categoryOptionCombos["+andIdent+
		",categoryOptions[id]]", "filter", "id:in:"+idList(keys(combos)), "paging", "false"), &cc)
	cats := map[string]bool{}
	for _, c := range cc.CategoryCombos {
		for _, x := range c.Categories {
			cats[x.ID] = true
		}
	}
	get("/api/categories", v("fields", andIdent+",categoryOptions[id],dataDimensionType", "filter",
		"id:in:"+idList(keys(cats)), "paging", "false"), nil)
	get("/api/categoryOptions", v("fields", andNameable+",startDate,endDate,"+andAccess, "filter",
		"categories.id:in:"+idList(keys(cats)), "paging", "true", "page", "1", "pageSize", "500"), nil)
	sets2 := keys(optionSets)
	for _, c := range chunks(sets2, 130) {
		get("/api/optionSets", v("fields", andIdent+",version,valueType", "filter", "id:in:"+idList(c), "paging", "false"), nil)
	}
	for _, c := range chunks(sets2, 130) {
		for page := 1; ; page++ {
			var opts struct {
				Options []json.RawMessage `json:"options"`
			}
			get("/api/options", v("fields", andOption, "filter", "optionSet.id:in:"+idList(c), "paging", "true",
				"page", strconv.Itoa(page), "pageSize", "1000"), &opts)
			if len(opts.Options) < 1000 {
				break
			}
		}
		get("/api/optionGroups", v("fields", andIdent+",optionSet[id],options[id]", "filter",
			"optionSet.id:in:"+idList(c), "paging", "false"), nil)
	}
	get("/api/indicators", v("fields", andNameable+",annualized,indicatorType[id],numerator,denominator,url,legendSets[id]",
		"filter", "id:in:[]", "paging", "false"), nil)
	get("/api/legendSets", v("fields", andIdent+",symbolizer,legends["+andIdent+",startValue,endValue,color]", "paging", "false"), nil)
	get("/api/trackedEntityInstanceFilters", v("fields", andIdent+",program[id],description,sortOrder,entityQueryCriteria",
		"filter", "program.id:in:"+idList(programs), "paging", "false"), nil)
	get("/api/programStageWorkingLists", v("fields", andIdent+",program[id],programStage[id],programStageQueryCriteria",
		"filter", "program.id:in:"+idList(programs), "paging", "false"), nil)
	get("/api/eventFilters", v("fields", andIdent+",program,programStage,description,eventQueryCriteria",
		"filter", "program:in:"+idList(programs), "paging", "false"), nil)
	get("/api/externalMapLayers", v("fields", andIdent+",url,mapService", "paging", "false"), nil)
}

// androidDataSync replays the SDK's daily data download: changed people per program
// (500 at most, 50 a page, the facility and below, deleted ones too), and aggregate
// values and completions of the user's data sets.
func androidDataSync(t testing.TB, d *dev, m *tk.Manifest, updatedAfter time.Time) int {
	st := d.json("GET", "/api/system/info?fields=serverDate,version", nil, nil)
	if st != 200 {
		t.Fatalf("android system info: %d", st)
	}
	people := 0
	for page := 1; page <= 10; page++ {
		q := url.Values{"program": {m.Primary().ID}, "orgUnits": {m.Facility.ID}, "orgUnitMode": {"DESCENDANTS"},
			"fields": {andTE}, "includeDeleted": {"true"}, "order": {"createdAt:desc"}, "paging": {"true"},
			"page": {strconv.Itoa(page)}, "pageSize": {"50"},
			"updatedAfter": {updatedAfter.UTC().Format("2006-01-02T15:04:05.000")}}
		var res struct {
			TrackedEntities []json.RawMessage `json:"trackedEntities"`
		}
		if st := d.json("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &res); st != 200 {
			t.Fatalf("android download: %d", st)
		}
		people += len(res.TrackedEntities)
		if len(res.TrackedEntities) < 50 {
			break
		}
	}
	now := time.Now()
	q := url.Values{"orgUnit": {m.Facility.ID}, "children": {"true"}, "includeDeleted": {"true"},
		"lastUpdated": {updatedAfter.UTC().Format("2006-01-02")}, "paging": {"false"},
		"fields": {"dataElement,period,orgUnit,categoryOptionCombo,attributeOptionCombo,value,storedBy,created,lastUpdated,comment,followup,deleted"}}
	for _, ds := range []string{"C4oUitImBPK", "RtEYsASU7PG", "ic1BSWhGOso", "nGkMm2VBT4G", "VDhwrW9DiC1", "quMWqLxzcfO",
		"dFRD2A5fdvn", "DFMoIONIalm", "GwSIuQVi8b2", "EBqVAQRmiPm"} {
		q.Add("dataSet", ds)
	}
	for i := 1; i <= 3; i++ { // the SDK's default: the last periods of each type
		q.Add("period", now.AddDate(0, -i, 0).Format("200601"))
		_, w := now.AddDate(0, 0, -7*i).ISOWeek()
		q.Add("period", fmt.Sprintf("%dW%d", now.AddDate(0, 0, -7*i).Year(), w))
	}
	q.Add("period", fmt.Sprintf("%dQ%d", now.Year(), (int(now.Month())-1)/3+1))
	d.json("GET", "/api/dataValueSets?"+q.Encode(), nil, nil)
	q.Set("fields", "period,dataSet,organisationUnit,attributeOptionCombo,date,storedBy,completed")
	q.Del("includeDeleted")
	d.json("GET", "/api/completeDataSetRegistrations?"+q.Encode(), nil, nil)
	return people
}

// androidUpload is a sync of one record after it is saved: the SDK posts it
// asynchronously, follows the job to its report, then downloads the person again.
func androidUpload(t testing.TB, d *dev, m *tk.Manifest, payload map[string]any, te string) {
	var job struct {
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if st := d.json("POST", "/api/tracker?async=true&atomicMode=OBJECT&importStrategy=CREATE_AND_UPDATE", payload, &job); st != 200 || job.Response.ID == "" {
		t.Fatalf("android upload: %d %+v", st, job)
	}
	for i := 0; ; i++ {
		var notes []struct {
			Completed bool `json:"completed"`
		}
		d.json("GET", "/api/tracker/jobs/"+job.Response.ID, nil, &notes)
		if slices.ContainsFunc(notes, func(n struct {
			Completed bool `json:"completed"`
		}) bool {
			return n.Completed
		}) {
			break
		}
		if i > 120 {
			t.Fatal("android upload: the job did not complete")
		}
		time.Sleep(500 * time.Millisecond)
	}
	var rep struct {
		Status string `json:"status"`
	}
	if st := d.json("GET", "/api/tracker/jobs/"+job.Response.ID+"/report", nil, &rep); st != 200 || rep.Status != "OK" {
		t.Fatalf("android upload report: %d %s", st, rep.Status)
	}
	q := url.Values{"program": {m.Primary().ID}, "fields": {andTE}, "includeDeleted": {"true"}, "orgUnitMode": {"ACCESSIBLE"}}
	if st := d.json("GET", "/api/tracker/trackedEntities/"+te+"?"+q.Encode(), nil, nil); st != 200 {
		t.Fatalf("android download of %s: %d", te, st)
	}
}

// androidUnits measures an Android device's work at base: its weekly metadata sync,
// its daily data sync after a facility-day of changes, and a record sync after a
// visit and after a registration.
func (w *workEnv) androidUnits(t *testing.T, base string, net *tk.Netcut, where string) {
	d := newDev(t, base, w.m.Users["staff9"].Username, w.m.Password)
	androidMetadataSync(t, d, w.m.Facility.ID) // warm central's caches, as other facilities keep them
	for i := 0; i < w.runs; i++ {
		w.meter.measure(where+": android metadata sync", net, func() { androidMetadataSync(t, d, w.m.Facility.ID) })
	}
	since := w.daySaved(t)
	for i := 0; i < w.runs; i++ {
		var n int
		w.meter.measure(where+": android daily data sync", net, func() { n = androidDataSync(t, d, w.m, since) })
		t.Logf("the daily sync downloaded %d people", n)
	}
	p := w.m.Primary()
	for i, pr := range w.people(t, w.runs) {
		_, payload := w.m.NewEvent(p, visitStage, pr.Enrollment)
		w.meter.measure(where+": android visit, record sync", net, func() { androidUpload(t, d, w.m, payload, pr.TE) })
		reg := w.m.NewPerson(p, "Zy"+strings.ToLower(tk.UID()[:6])+strconv.Itoa(i), visitStage)
		w.meter.measure(where+": android registration, record sync", net, func() { androidUpload(t, d, w.m, reg.Payload, reg.TE) })
	}
}

// daySaved makes a facility-day of changes on central (not measured; 200 visits and
// 100 registrations) once per client set-up (androidUnits) and returns the time
// before them.
func (w *workEnv) daySaved(t testing.TB) time.Time {
	var info struct {
		ServerDate string `json:"serverDate"`
	}
	tk.NewCentral(w.m.CentralURL, w.m.AdminUser, w.m.AdminPassword).JSON("GET", "/api/system/info", nil, &info)
	at, err := time.Parse("2006-01-02T15:04:05.000", info.ServerDate)
	if err != nil {
		t.Fatalf("server date %q: %v", info.ServerDate, err)
	}
	start := time.Now()
	w.staffSave(t, 200, 100)
	t.Logf("a facility-day of changes saved on central in %s", time.Since(start).Round(time.Second))
	return at.Add(-time.Minute)
}

// --- aggregate ---------------------------------------------------------------------

// aggregateUnits measures one monthly form: HMIS 105:01 opened (its values for the
// month), 150 values sent in one dataValueSets request and the form completed, as
// eRegisters sends its HMIS reports. A month has one such form per facility; the
// report divides it by 22 working days.
func (w *workEnv) aggregateUnits(t *testing.T, base string, net *tk.Netcut, where string) {
	d := newDev(t, base, w.m.Users["staff2"].Username, w.m.Password)
	var ds struct {
		CategoryCombo struct {
			CategoryOptionCombos []struct{ ID, Name string } `json:"categoryOptionCombos"`
		} `json:"categoryCombo"`
		DataSetElements []struct {
			DataElement struct {
				ID        string `json:"id"`
				ValueType string `json:"valueType"`
			} `json:"dataElement"`
		} `json:"dataSetElements"`
	}
	const form = "RtEYsASU7PG"
	tk.NewCentral(w.m.CentralURL, w.m.AdminUser, w.m.AdminPassword).JSON("GET",
		"/api/dataSets/"+form+"?fields=categoryCombo[categoryOptionCombos[id,name]],dataSetElements[dataElement[id,valueType]]", nil, &ds)
	aoc := "" // the form is filled for nationals
	for _, c := range ds.CategoryCombo.CategoryOptionCombos {
		if strings.Contains(c.Name, "National") {
			aoc = c.ID
		}
	}
	var numeric []string
	for _, e := range ds.DataSetElements {
		if strings.Contains(e.DataElement.ValueType, "INTEGER") || e.DataElement.ValueType == "NUMBER" {
			numeric = append(numeric, e.DataElement.ID)
		}
	}
	for i := 0; i < w.runs; i++ {
		period := time.Now().AddDate(0, -1-i, 0).Format("200601")
		w.meter.measure(where+": aggregate monthly form", net, func() {
			q := "dataSet=" + form + "&period=" + period + "&orgUnit=" + w.m.Facility.ID + "&attributeOptionCombo=" + aoc
			d.do("GET", "/api/dataValueSets?"+q, nil)
			var values []map[string]string
			for j, de := range numeric[:min(150, len(numeric))] {
				values = append(values, map[string]string{"dataElement": de, "value": strconv.Itoa(1 + (j*7+i)%40)})
			}
			body := map[string]any{"dataSet": form, "period": period, "orgUnit": w.m.Facility.ID,
				"attributeOptionCombo": aoc, "dataValues": values}
			if st, b := d.do("POST", "/api/dataValueSets?importStrategy=CREATE_AND_UPDATE", body); st != 200 {
				t.Errorf("dataValueSets: %d %.300s", st, b)
			}
			reg := map[string]any{"completeDataSetRegistrations": []any{map[string]any{
				"dataSet": form, "period": period, "organisationUnit": w.m.Facility.ID, "attributeOptionCombo": aoc,
				"completed": true}}}
			if st, b := d.do("POST", "/api/completeDataSetRegistrations", reg); st != 200 {
				t.Errorf("completion: %d %.300s", st, b)
			}
		})
	}
}

// --- the hub ------------------------------------------------------------------------

// hubStart sets a hub up as the facility's admin and has the ten staff log in through
// it, as they would on the first day; the first sync then pulls for all of them.
func (w *workEnv) hubStart(t *testing.T) (*tk.Env, func()) {
	e := tk.Start(t)
	return e, func() {
		e.SetupOnly(tk.RoleAdmin)
		for n := 1; n <= 10; n++ {
			e.Web(fmt.Sprintf("staff%d", n)).Login()
		}
		e.Sync()
	}
}

func (w *workEnv) setLocalReads(t testing.TB, e *tk.Env, on bool) {
	body := map[string]any{"programs": []string{}}
	if on {
		body["programs"] = []string{w.m.Primary().ID}
	}
	if err := e.Central.JSON("PUT", "/api/dataStore/facility-hub-config/localReads", body, nil); err != nil {
		if err = e.Central.JSON("POST", "/api/dataStore/facility-hub-config/localReads", body, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.App.Hub.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// cycle runs sync cycles until the outbox is empty, then lets a cycle the push
// triggered finish.
func cycle(t testing.TB, e *tk.Env) {
	e.Sync()
	for i := 0; e.App.Hub.Store.CountOutbox(store.OutboxPending) > 0 && i < 20; i++ {
		e.Sync()
	}
	time.Sleep(3 * time.Second)
}

// hubUnits measures the hub's own work against central: its first pull (a fresh hub
// each time), a 15-minute cycle, the hourly refresh, the weekly full pull, its pings,
// and the catch-up after half a day offline; then, with local reads on, the hourly
// refresh (with the metadata check) and a cycle.
func (w *workEnv) hubUnits(t *testing.T) {
	for i := 0; i < w.runs; i++ {
		t.Run("first pull", func(t *testing.T) {
			e, first := w.hubStart(t)
			w.meter.measure("hub: setup and first pull", e.Net, first)
		})
	}
	e, first := w.hubStart(t)
	first()
	t.Cleanup(func() { w.setLocalReads(t, e, false) })
	hub := e.App.Hub
	hub.Cfg.AccessRefreshInterval, hub.Cfg.OwnershipCheckInterval = time.Hour, time.Hour
	e.Sync() // the hourly work done, as after any hourly cycle
	// Online the hub pings central once a minute (central.Monitor): 60 pings as it sends them.
	for i := 0; i < w.runs; i++ {
		w.meter.measure("hub: 60 pings", e.Net, func() {
			for j := 0; j < 60; j++ {
				if resp, err := http.Get(e.Net.URL() + "/api/system/ping"); err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		})
	}
	quarter := func() { w.staffSave(t, 6, 3) } // a quarter hour of the facility's saves, on central
	for i := 0; i < w.runs; i++ {
		quarter()
		w.meter.measure("hub: sync cycle", e.Net, func() { cycle(t, e) })
	}
	hourly := func(unit string) {
		quarter()
		hub.Cfg.AccessRefreshInterval, hub.Cfg.OwnershipCheckInterval = 0, 0
		w.meter.measure(unit, e.Net, func() { cycle(t, e) })
		hub.Cfg.AccessRefreshInterval, hub.Cfg.OwnershipCheckInterval = time.Hour, time.Hour
	}
	for i := 0; i < w.runs; i++ {
		hourly("hub: hourly refresh")
	}
	for i := 0; i < w.runs; i++ {
		if _, err := hub.Store.DB().Exec(`UPDATE pull_state SET last_full_at = ''`); err != nil {
			t.Fatal(err)
		}
		w.meter.measure("hub: weekly full pull", e.Net, func() { cycle(t, e) })
	}
	// Half a day offline: 150 patients through the hub (a third registrations) by four
	// staff, as Capture web saves them; then the link returns.
	clients := []*tk.Client{e.Web("staff5"), e.Web("staff6"), e.Web("staff7"), e.Web("staff8")}
	for _, c := range clients {
		c.Login()
	}
	p := w.m.Primary()
	for i := 0; i < w.runs; i++ {
		ppl := w.people(t, 50)
		ppl = append(ppl, w.people(t, 50)...)
		e.Offline()
		for j := 0; j < 150; j++ {
			c := clients[j%len(clients)]
			var payload map[string]any
			if j%3 == 2 {
				payload = w.m.NewPerson(p, "Zx"+strings.ToLower(tk.UID()[:6]), visitStage).Payload
			} else {
				_, payload = w.m.NewEvent(p, visitStage, ppl[j*2/3%len(ppl)].Enrollment)
			}
			if st, b := c.Do("POST", tk.SyncImport, payload); st != 200 {
				t.Fatalf("offline save: %d %.300s", st, b)
			}
		}
		e.Online()
		w.meter.measure("hub: catch-up after half a day offline", e.Net, func() { cycle(t, e) })
		if n := hub.Store.CountOutbox(store.OutboxPending); n > 0 {
			t.Errorf("after the catch-up %d entries are pending", n)
		}
	}
	// Local reads on: the hourly refresh adds the metadata check, every cycle the
	// national refresh flag. The first check only records the time.
	w.setLocalReads(t, e, true)
	hourly("hub, local reads: first hourly refresh (not counted)")
	for i := 0; i < w.runs; i++ {
		hourly("hub, local reads: hourly refresh")
	}
	// The metadata check alone, as the hourly refresh runs it: one member of each
	// access group asks what changed since the last check (sync.checkMetadata).
	users, err := hub.Store.Users()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var members []central.Auth
	for _, u := range users {
		acc := hub.Access(u.ID)
		if u.CannotPush() || !acc.InFacility(hub.FacilityPath()) || seen[acc.GroupKey()] {
			continue
		}
		if a, err := hub.UpstreamAuth(u.ID); err == nil {
			seen[acc.GroupKey()] = true
			members = append(members, a)
		}
	}
	for i := 0; i < w.runs; i++ {
		w.meter.measure("hub, local reads: metadata check alone", e.Net, func() {
			if err := hub.CheckMetadata(context.Background(), members); err != nil {
				t.Error(err)
			}
		})
	}
	for i := 0; i < w.runs; i++ {
		quarter()
		w.meter.measure("hub, local reads: sync cycle", e.Net, func() { cycle(t, e) })
	}
}

// viaHub measures the same clients through a hub: passing everything through, then
// with local reads on.
func (w *workEnv) viaHub(t *testing.T) {
	e, first := w.hubStart(t)
	first()
	t.Cleanup(func() { w.setLocalReads(t, e, false) })
	ctl := w.control(t, e)
	for _, mode := range []string{"via hub", "via hub, local reads"} {
		if mode == "via hub, local reads" {
			w.setLocalReads(t, e, true)
			e.Sync()
		}
		for _, app := range w.apps() {
			w.drive(t, app, e.Srv.URL, ctl, mode, w.m.Users[map[string]string{"capture": "staff3", "eregisters": "staff4"}[app]].Username)
		}
		if w.want("android") {
			w.androidUnits(t, e.Srv.URL, e.Net, mode)
		}
		if w.want("aggregate") {
			w.aggregateUnits(t, e.Srv.URL, e.Net, mode)
		}
	}
}

// --- the report ---------------------------------------------------------------------

// The facility-day (docs/PERFORMANCE.md): ten staff, eight on a web app and two on
// Android, 30 patients each, a third of them new registrations; half a day offline.
const (
	webStaff, androidStaff = 8, 2
	patients               = 30    // per staff member and day
	cycles, hourlies       = 32, 8 // a working day's sync cycles, of which hourly refreshes
	workingDays            = 22    // a month's, for the monthly form
	pingHours              = 24    // the hub pings once a minute, day and night
)

type unitMedian struct {
	n                                                  int
	db, stmts, rows, blocks, reqs, server, bytes, wall float64
}

func medians(samples []sample) map[string]unitMedian {
	by := map[string][]sample{}
	for _, s := range samples {
		by[s.Unit] = append(by[s.Unit], s)
	}
	out := map[string]unitMedian{}
	for u, ss := range by {
		mid := func(f func(sample) float64) float64 {
			v := make([]float64, len(ss))
			for i, s := range ss {
				v[i] = f(s)
			}
			slices.Sort(v)
			if len(v)%2 == 1 {
				return v[len(v)/2]
			}
			return (v[len(v)/2-1] + v[len(v)/2]) / 2
		}
		out[u] = unitMedian{n: len(ss), db: mid(func(s sample) float64 { return s.DBMs }),
			stmts:  mid(func(s sample) float64 { return float64(s.Statements) }),
			rows:   mid(func(s sample) float64 { return float64(s.Rows) }),
			blocks: mid(func(s sample) float64 { return float64(s.Blocks) }),
			reqs:   mid(func(s sample) float64 { return float64(s.Requests) }),
			server: mid(func(s sample) float64 { return s.ServerMs }),
			bytes:  mid(func(s sample) float64 { return float64(s.Bytes) }),
			wall:   mid(func(s sample) float64 { return s.WallMs })}
	}
	return out
}

func (a unitMedian) plus(b unitMedian, times float64) unitMedian {
	a.db += b.db * times
	a.stmts += b.stmts * times
	a.rows += b.rows * times
	a.blocks += b.blocks * times
	a.reqs += b.reqs * times
	a.server += b.server * times
	a.bytes += b.bytes * times
	return a
}

// TestCentralWorkReport reads the samples in HUB_CENTRAL_OUT and prints the per-unit
// medians and the facility-day per scenario (written to HUB_CENTRAL_REPORT if set).
// Database figures have DHIS2's idle background taken off.
func TestCentralWorkReport(t *testing.T) {
	if os.Getenv("HUB_CENTRAL_OUT") == "" {
		t.Skip("set HUB_CENTRAL_OUT to the samples of TestCentralWork (several, comma separated: a unit in a later file replaces it in earlier ones)")
	}
	var samples []sample
	for _, p := range strings.Split(os.Getenv("HUB_CENTRAL_OUT"), ",") {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		var file []sample
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var s sample
			if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
				t.Fatal(err)
			}
			file = append(file, s)
		}
		f.Close()
		samples = slices.DeleteFunc(samples, func(s sample) bool {
			return slices.ContainsFunc(file, func(n sample) bool { return n.Unit == s.Unit })
		})
		samples = append(samples, file...)
	}
	// DHIS2's own background work (its scheduler) runs whatever the clients do:
	// measured idle, per millisecond, and taken off each sample for its wall time.
	idle, ok := medians(samples)["idle, 60 s"]
	if !ok {
		t.Fatal("no idle samples: run TestCentralWork with the idle unit")
	}
	for i, s := range samples {
		if s.Unit == "idle, 60 s" {
			continue
		}
		f := s.WallMs / idle.wall
		samples[i].DBMs = max(0, s.DBMs-idle.db*f)
		samples[i].Statements = max(0, s.Statements-int64(idle.stmts*f))
		samples[i].Rows = max(0, s.Rows-int64(idle.rows*f))
		samples[i].Blocks = max(0, s.Blocks-int64(idle.blocks*f))
	}
	med := medians(samples)
	units := make([]string, 0, len(med))
	for u := range med {
		units = append(units, u)
	}
	slices.Sort(units)
	var out []string
	out = append(out, "| Unit | Runs | DB time (ms) | Statements | Rows | Blocks | Requests | Server time (ms) | KB | Wall (s) |",
		"|---|---|---|---|---|---|---|---|---|---|")
	for _, u := range units {
		m := med[u]
		out = append(out, fmt.Sprintf("| %s | %d | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %.0f | %.1f |",
			u, m.n, m.db, m.stmts, m.rows, m.blocks, m.reqs, m.server, m.bytes/1024, m.wall/1000))
	}

	visits, regs := float64(patients)*2/3, float64(patients)/3
	type term struct {
		group, unit string
		times       float64
	}
	// share: the part of the day's patients seen online (with a hub, the other half is
	// saved offline and reaches central in the catch-up).
	clientTerms := func(where, app string, share float64) []term {
		ts := []term{
			{"web staff", where + ": " + app + " app start", webStaff},
			{"web staff", where + ": " + app + " visit", webStaff * visits * share},
			{"web staff", where + ": " + app + " registration", webStaff * regs * share},
			{"Android", where + ": android visit, record sync", androidStaff * visits * share},
			{"Android", where + ": android registration, record sync", androidStaff * regs * share},
			{"Android", where + ": android daily data sync", androidStaff},
			{"Android", where + ": android metadata sync", androidStaff / 7.0},
			{"monthly form", where + ": aggregate monthly form", 1.0 / workingDays},
		}
		if app == "eregisters" {
			ts = append(ts, term{"web staff", where + ": eregisters pull", webStaff})
		}
		if app == "capture" && strings.Contains(where, "local reads") {
			// With local reads Capture did not open a person it had just registered
			// (the hub answered "not enrolled"), so the registration unit lacks its
			// first visit: counted as one more visit.
			ts = append(ts, term{"web staff", where + ": capture visit", webStaff * regs * share})
		}
		return ts
	}
	hubTerms := func(prefix string) []term {
		return []term{
			{"hub", prefix + "sync cycle", cycles - hourlies},
			{"hub", prefix + "hourly refresh", hourlies},
			{"hub", "hub: weekly full pull", 1.0 / 7},
			{"hub", "hub: 60 pings", pingHours},
			{"hub", "hub: catch-up after half a day offline", 1},
		}
	}
	totals := []string{"| Web app | Scenario | DB time (s) | Statements | Rows | Blocks | Requests | Server time (s) | MB |",
		"|---|---|---|---|---|---|---|---|---|"}
	parts := []string{"| Web app | Scenario | Part | DB time (s) | Rows | Blocks | Requests | Server time (s) | MB |",
		"|---|---|---|---|---|---|---|---|---|"}
	for _, app := range []string{"eregisters", "capture"} {
		scen := []struct {
			name  string
			terms []term
		}{
			{"1. no hub", clientTerms("direct", app, 1)},
			{"2. hub passing through", append(clientTerms("via hub", app, 0.5), hubTerms("hub: ")...)},
			{"3. hub with local reads", append(clientTerms("via hub, local reads", app, 0.5), hubTerms("hub, local reads: ")...)},
		}
		if app == "eregisters" {
			// eRegisters did not start with local reads on: its units as passed through,
			// an upper bound (its saves reach central either way, its reads could only
			// shrink).
			ts := append(clientTerms("via hub, local reads", app, 0.5), hubTerms("hub, local reads: ")...)
			for i, tm := range ts {
				if strings.Contains(tm.unit, "eregisters") {
					ts[i].unit = strings.Replace(tm.unit, "via hub, local reads", "via hub", 1)
				}
			}
			scen = append(scen, struct {
				name  string
				terms []term
			}{"3. hub with local reads, eRegisters as passed through (upper bound)", ts})
		}
		for _, sc := range scen {
			var sum unitMedian
			group := map[string]unitMedian{}
			var order, missing []string
			for _, tm := range sc.terms {
				m, ok := med[tm.unit]
				if !ok {
					missing = append(missing, tm.unit)
					continue
				}
				sum = sum.plus(m, tm.times)
				if _, ok := group[tm.group]; !ok {
					order = append(order, tm.group)
				}
				group[tm.group] = group[tm.group].plus(m, tm.times)
			}
			if len(missing) > 0 {
				totals = append(totals, fmt.Sprintf("| %s | %s | not measured: %s |", app, sc.name, strings.Join(missing, "; ")))
				continue
			}
			totals = append(totals, fmt.Sprintf("| %s | %s | %.1f | %.0f | %.0f | %.0f | %.0f | %.1f | %.1f |",
				app, sc.name, sum.db/1000, sum.stmts, sum.rows, sum.blocks, sum.reqs, sum.server/1000, sum.bytes/1e6))
			for _, g := range order {
				m := group[g]
				parts = append(parts, fmt.Sprintf("| %s | %s | %s | %.1f | %.0f | %.0f | %.0f | %.1f | %.1f |",
					app, sc.name, g, m.db/1000, m.rows, m.blocks, m.reqs, m.server/1000, m.bytes/1e6))
			}
		}
	}
	out = append(out, "")
	out = append(out, totals...)
	out = append(out, "")
	out = append(out, parts...)
	report := strings.Join(out, "\n")
	t.Log("\n" + report)
	if p := os.Getenv("HUB_CENTRAL_REPORT"); p != "" {
		os.WriteFile(p, []byte(report+"\n"), 0o644)
	}
}
