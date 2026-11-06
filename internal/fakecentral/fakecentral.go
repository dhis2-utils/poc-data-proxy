// Package fakecentral is a small stand-in for a DHIS2 2.42 server, shaped after the
// fixtures in testdata/2.42. It covers what the hub's tests need: login, /api/me,
// personal access tokens, program metadata and sharing, tracker read/write with the
// error codes central returns, data store, reserved values, and a "down" switch that
// drops connections like a cut uplink.
package fakecentral

import (
	"crypto/sha256"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// Fixed metadata (mirrors the TB program set-up in testdata/2.42/SETUP.md).
const (
	Facility      = "DiszpKrYNg8"
	OtherFacility = "g8upMTyEZGZ"
	District      = "FakeDistr01" // above both facilities (level 3)
	Program       = "ur1Edk5Oe2n"
	Stage1        = "EPEcjy3FWmI"
	Stage2        = "ZkbAXlQUYJG"
	Stage3        = "jdRD35YwbRH"
	TET           = "nEenWmSyUEp"
	AttrFirstName = "w75KJ2mc4zz"
	AttrLastName  = "zDhUuAYrxNC"
	AttrNationalI = "AuPLng5hLbE" // unique
	AttrTBID      = "xs8A6tQJY0s" // generated, unique
	DefaultAOC    = "HllvX50cXC0"
	SetupGroup    = "HubGrpSetup"
	Password      = "Hub-Test-2026!"
	Version       = "2.42.6"
)

type User struct {
	UID, Username  string
	OrgUnits       []string
	Groups         []string
	ProgramRead    bool
	ProgramWrite   bool
	StageRead      map[string]bool
	StageWrite     map[string]bool
	Disabled       bool
	SearchOrgUnits []string // defaults to OrgUnits
	NoProgramMeta  bool     // the program's metadata is not shared with this user
	NoPAT          bool     // this user may not create personal access tokens
	// Authorities, when not nil, are the user's (default: Capture's).
	Authorities      []string
	DataViewOrgUnits []string // defaults to OrgUnits
}

type Central struct {
	// TimeZone is the server's time zone (serverTimeZoneId); empty: UTC.
	TimeZone string
	// OnTokenCreate, when set, runs as a user's token is created, before central
	// answers (tests of what happens meanwhile).
	OnTokenCreate func(username string)
	// FailPaths makes a path answer this status (tests).
	FailPaths map[string]int
	// RoleAuthorities are the authorities of user roles (default: none).
	RoleAuthorities map[string][]string
	// LoseResponse ("PUT /api/...": n) carries out the next n such requests but answers
	// them with 502, as when the answer is lost on the way back.
	LoseResponse map[string]int
	// DropPaths closes the connection on requests to these paths without an answer,
	// as a flapping link does (tests).
	DropPaths map[string]bool
	// FailHTML makes a path answer this status with an HTML page, as a proxy or a
	// firewall in front of central does (tests).
	FailHTML map[string]int
	// CutResponse ("POST /api/...": n) carries out the next n such requests but cuts
	// their answer off halfway, as when the connection drops while the hub reads it.
	CutResponse map[string]int
	// StallResponse ("GET /apps/...": n) starts the next n such answers and then holds
	// them until the hub gives up on them, as when the client that asked went away.
	StallResponse map[string]int
	// AppFiles are app files served as central serves them: to logged-in users only,
	// with an ETag, and 304 to a request that names the current one (tests).
	AppFiles map[string]string
	// NotModified counts the 304s given for AppFiles.
	NotModified int
	delay         time.Duration // Struggle
	failShare     float64
	struggled     int                        // requests counted for failShare
	passwords     map[string]string          // users created through the API, with their own password
	readers       map[string]map[string]bool // dataStore entry id -> user UIDs it is shared with (read)
	writers       map[string]map[string]bool // dataStore entry id -> user UIDs it is shared with (write)
	creators      map[string]string          // dataStore entry id -> username who created it
	public        map[string]string          // dataStore entry id -> public access its sharing sets
	// HubWriters, when set, are the only users who may write the facility-hub-*
	// dataStore namespaces (protected by sharing, as DEPLOYMENT.md advises).
	HubWriters   map[string]bool
	facilityPath string // MoveFacility
	// OnlyEnrollOnce: the TB program allows one enrollment ever (central's E1016).
	OnlyEnrollOnce bool
	fetched        map[string]int // tracked entity -> times fetched by uid
	teLists        []string       // queries of tracked entity listings
	mu             sync.Mutex
	Users          map[string]*User
	tokens         map[string]string   // PAT key -> username
	tokenShares    map[string][]string // PAT uid -> user groups it is shared with
	tokenSeq       int
	tokenIDs       map[string]string // token uid -> key
	sessions       map[string]string // cookie -> username
	objects        map[string]tracker.M
	kinds          map[string]string
	order          []string
	DataStore      map[string]map[string]json.RawMessage
	reserved       int
	down           atomic.Bool
	Version        string
	// FutureEnrollmentDates and FutureIncidentDates are the TB program's
	// selectEnrollmentDatesInFuture and selectIncidentDatesInFuture. False, as on a new
	// program on central, refuses dates after central's today (E1020, E1021).
	FutureEnrollmentDates, FutureIncidentDates bool
	// Requests counts calls per "METHOD path" for assertions.
	Requests map[string]int
	gen      *generated // Generate: performance-test data

	// Program ownership (ownership.go): owner per tracked entity and program, and
	// temporary ownership ("break the glass") per user, tracked entity and program.
	owners map[string]string
	temp   map[string]time.Time
	// Protected makes the program PROTECTED: only users whose capture scope holds the
	// owner, or who broke the glass, may read a person's enrollment.
	Protected bool
	// TempAccess is how long temporary ownership lasts.
	TempAccess time.Duration
	jobs       map[string][]byte
	jobN       int

	// Aggregate data (aggregate.go).
	aggValues     map[string]*aggValue
	aggRegs       map[string]*aggReg
	approved      map[string]bool
	AggExpiryDays float64 // expiry days of AggDataSet
	// OnAggWrite runs as an aggregate write arrives, before it is handled (tests).
	OnAggWrite func()
	// CompactValueSets answers dataValueSets as 2.43 does: fields every value shares
	// (orgUnit, attributeOptionCombo, period) once at the top, not on each value.
	CompactValueSets bool
	// ClockSkew shifts central's clock against the hub's (tests).
	ClockSkew time.Duration
	// AggFail makes an aggregate path answer this status (tests).
	AggFail map[string]int
	// FailImportsOf makes a tracker import whose body holds this UID answer this
	// status, as when one payload trips a bug or a lock on central (tests).
	FailImportsOf map[string]int
}

// MoveFacility gives the facility a new org unit path ("/root/<district>/<facility>"),
// as when it moves to another district.
func (c *Central) MoveFacility(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.facilityPath = path
}

// ouPath is an org unit's path: "/root/<id>", or the facility's as MoveFacility set it.
func (c *Central) ouPath(ou string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ou == Facility && c.facilityPath != "" {
		return c.facilityPath
	}
	return "/root/" + ou
}

func New() *Central {
	all := map[string]bool{Stage1: true, Stage2: true, Stage3: true}
	no3 := map[string]bool{Stage1: true, Stage2: true}
	c := &Central{
		Users: map[string]*User{
			"hub_admin":  {UID: "HubUsrAdmin", Username: "hub_admin", OrgUnits: []string{Facility}, Groups: []string{SetupGroup}, ProgramRead: true, ProgramWrite: true, StageRead: all, StageWrite: all},
			"nurse_a":    {UID: "HubUsrNursA", Username: "nurse_a", OrgUnits: []string{Facility}, ProgramRead: true, ProgramWrite: true, StageRead: all, StageWrite: all},
			"nurse_b":    {UID: "HubUsrNursB", Username: "nurse_b", OrgUnits: []string{Facility}, ProgramRead: true, ProgramWrite: true, StageRead: no3, StageWrite: no3},
			"clerk_c":    {UID: "HubUsrClrkC", Username: "clerk_c", OrgUnits: []string{Facility}, ProgramRead: true, StageRead: no3, StageWrite: map[string]bool{}},
			"multi_g":    {UID: "HubUsrMultG", Username: "multi_g", OrgUnits: []string{Facility, OtherFacility}, ProgramRead: true, StageRead: no3, StageWrite: map[string]bool{}},
			"district_e": {UID: "HubUsrDistE", Username: "district_e", OrgUnits: []string{District}, ProgramRead: true, StageRead: no3, StageWrite: map[string]bool{}},
			"outsider_d": {UID: "HubUsrOutsD", Username: "outsider_d", OrgUnits: []string{OtherFacility}, ProgramRead: true, ProgramWrite: true, StageRead: no3, StageWrite: no3},
		},
		tokens: map[string]string{}, tokenIDs: map[string]string{}, tokenShares: map[string][]string{}, sessions: map[string]string{},
		objects: map[string]tracker.M{}, kinds: map[string]string{}, DataStore: map[string]map[string]json.RawMessage{},
		Version: Version, Requests: map[string]int{}, jobs: map[string][]byte{},
		owners: map[string]string{}, temp: map[string]time.Time{}, TempAccess: time.Hour,
		aggValues: map[string]*aggValue{}, aggRegs: map[string]*aggReg{}, approved: map[string]bool{},
	}
	return c
}

// SetDown makes every request fail at the connection level (a cut uplink).
func (c *Central) SetDown(down bool) { c.down.Store(down) }

// SetVersion changes the DHIS2 version central reports, as after an upgrade.
func (c *Central) SetVersion(v string) {
	c.mu.Lock()
	c.Version = v
	c.mu.Unlock()
}

// now is the current time as DHIS2 writes it: in the server's time zone, without an
// offset.
func (c *Central) now() string {
	return time.Now().Add(c.ClockSkew).In(c.zone()).Format("2006-01-02T15:04:05.000")
}

// zone is TimeZone loaded, or UTC.
func (c *Central) zone() *time.Location {
	if c.TimeZone != "" {
		if loc, err := time.LoadLocation(c.TimeZone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// Seed adds a tracked entity owned by the facility, enrolled in the program.
func (c *Central) Seed(te, enr, firstName, nationalID string, events map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := c.now()
	c.put("trackedEntity", te, tracker.M{"trackedEntity": te, "trackedEntityType": TET, "orgUnit": Facility, "createdAt": ts, "updatedAt": ts,
		"attributes": []any{tracker.M{"attribute": AttrFirstName, "value": firstName}, tracker.M{"attribute": AttrNationalI, "value": nationalID}}})
	c.put("enrollment", enr, tracker.M{"enrollment": enr, "trackedEntity": te, "program": Program, "orgUnit": Facility, "status": "ACTIVE",
		"enrolledAt": ts, "createdAt": ts, "updatedAt": ts})
	for ev, stage := range events {
		c.put("event", ev, tracker.M{"event": ev, "enrollment": enr, "trackedEntity": te, "program": Program, "programStage": stage,
			"orgUnit": Facility, "status": "ACTIVE", "occurredAt": ts, "createdAt": ts, "updatedAt": ts, "dataValues": []any{}})
	}
}

// SeedPerson adds a tracked entity registered at the facility, enrolled in nothing.
func (c *Central) SeedPerson(te, firstName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := c.now()
	c.put("trackedEntity", te, tracker.M{"trackedEntity": te, "trackedEntityType": TET, "orgUnit": Facility, "createdAt": ts, "updatedAt": ts,
		"attributes": []any{tracker.M{"attribute": AttrFirstName, "value": firstName}}})
}

func (c *Central) put(kind, uid string, m tracker.M) {
	if _, ok := c.objects[uid]; !ok {
		c.order = append(c.order, uid)
	}
	c.objects[uid] = m
	c.kinds[uid] = kind
}

// Object returns a stored object (copy) or nil.
func (c *Central) Object(uid string) tracker.M {
	c.mu.Lock()
	defer c.mu.Unlock()
	if o := c.objects[uid]; o != nil {
		b, _ := json.Marshal(o)
		var out tracker.M
		json.Unmarshal(b, &out)
		return out
	}
	return nil
}

// Order returns object UIDs in the order they were first stored.
func (c *Central) Order() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.order)
}

func (c *Central) DataStoreKeys(ns string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var keys []string
	for k := range c.DataStore[ns] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c *Central) DataStoreValue(ns, key string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var v map[string]any
	json.Unmarshal(c.DataStore[ns][key], &v)
	return v
}

func (c *Central) appFile(path string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.AppFiles[path]
}

// SetAppFile changes an app file, as an app update does.
func (c *Central) SetAppFile(path, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.AppFiles == nil {
		c.AppFiles = map[string]string{}
	}
	c.AppFiles[path] = body
}

// NotModifiedCount is the number of 304s given for AppFiles.
func (c *Central) NotModifiedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.NotModified
}

// RequestsUnder counts the requests whose "METHOD /path" starts with prefix.
func (c *Central) RequestsUnder(prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for k, v := range c.Requests {
		if strings.HasPrefix(k, prefix) {
			n += v
		}
	}
	return n
}

func (c *Central) RequestCount(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Requests[key]
}

// --- HTTP ------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func webMessage(w http.ResponseWriter, status int, msg, code string) {
	m := map[string]any{"httpStatus": http.StatusText(status), "httpStatusCode": status, "status": "ERROR", "message": msg}
	if code != "" {
		m["errorCode"] = code
	}
	writeJSON(w, status, m)
}

func (c *Central) user(r *http.Request) *User {
	u := c.credentialUser(r)
	if u != nil && u.Disabled {
		return nil
	}
	return u
}

// SetDisabled disables or re-enables an account: every credential of it is refused.
func (c *Central) SetDisabled(username string, disabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Users[username].Disabled = disabled
}

func (c *Central) credentialUser(r *http.Request) *User {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(h, "ApiToken "):
		return c.Users[c.tokens[strings.TrimPrefix(h, "ApiToken ")]]
	case strings.HasPrefix(h, "Basic "):
		b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic "))
		u, p, _ := strings.Cut(string(b), ":")
		if own, ok := c.passwords[u]; ok {
			if p == own {
				return c.Users[u]
			}
		} else if p == Password {
			return c.Users[u]
		}
		return nil
	}
	if ck, err := r.Cookie("JSESSIONID_fake"); err == nil {
		return c.Users[c.sessions[ck.Value]]
	}
	return nil
}

// Struggle makes central overloaded: every answer is held for delay, and that share
// of requests is answered with 503 (tests of what a hub sends it then). Zeros end it.
func (c *Central) Struggle(delay time.Duration, failShare float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delay, c.failShare, c.struggled = delay, failShare, 0
}

func (c *Central) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	if c.ClockSkew != 0 { // central's clock in its Date header too, as a real server's
		w.Header().Set("Date", time.Now().Add(c.ClockSkew).UTC().Format(http.TimeFormat))
	}
	lose := c.LoseResponse[r.Method+" "+r.URL.Path] > 0
	if lose {
		c.LoseResponse[r.Method+" "+r.URL.Path]--
	}
	cut := c.CutResponse[r.Method+" "+r.URL.Path] > 0
	if cut {
		c.CutResponse[r.Method+" "+r.URL.Path]--
	}
	stall := c.StallResponse[r.Method+" "+r.URL.Path] > 0
	if stall {
		c.StallResponse[r.Method+" "+r.URL.Path]--
	}
	c.mu.Unlock()
	if stall {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("<html>"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	if cut { // done on central, but only half the answer reaches the hub
		rec := httptest.NewRecorder()
		c.serve(rec, r)
		b := rec.Body.Bytes()
		w.Header().Set("Content-Type", rec.Header().Get("Content-Type"))
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(rec.Code)
		w.Write(b[:len(b)/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	if lose { // done on central, but the answer never reaches the hub
		c.serve(httptest.NewRecorder(), r)
		webMessage(w, http.StatusBadGateway, "the answer was lost", "")
		return
	}
	c.serve(w, r)
}

func (c *Central) serve(w http.ResponseWriter, r *http.Request) {
	if c.down.Load() {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	c.mu.Lock()
	c.Requests[r.Method+" "+r.URL.Path]++
	fail := c.FailPaths[r.URL.Path]
	if len(c.FailImportsOf) > 0 && r.Method == "POST" && r.URL.Path == "/api/tracker" {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		for uid, st := range c.FailImportsOf {
			if bytes.Contains(b, []byte(uid)) {
				fail = st
			}
		}
	}
	failHTML := c.FailHTML[r.URL.Path]
	drop := c.DropPaths[r.URL.Path]
	delay := c.delay
	if c.failShare > 0 {
		// Every request whose count crosses a multiple of 1/failShare fails: the
		// share is exact, and the same in every run.
		c.struggled++
		if int(float64(c.struggled)*c.failShare) != int(float64(c.struggled-1)*c.failShare) {
			fail = http.StatusServiceUnavailable
		}
	}
	c.mu.Unlock()
	time.Sleep(delay)
	p := r.URL.Path
	if drop {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	if failHTML != 0 {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(failHTML)
		fmt.Fprintf(w, "<html><body><h1>%d %s</h1><hr>nginx</body></html>", failHTML, http.StatusText(failHTML))
		return
	}
	if fail != 0 {
		webMessage(w, fail, "failing as asked", "")
		return
	}
	switch {
	case p == "/api/system/ping":
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("pong"))
		return
	case p == "/api/auth/login":
		c.login(w, r)
		return
	case p == "/api/loginConfig": // public, as on 2.41+
		writeJSON(w, 200, map[string]any{"applicationTitle": "Fake central", "apiVersion": c.Version})
		return
	case strings.HasPrefix(p, "/apps/"):
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><meta name="dhis2-base-url" content="http://` + r.Host + `">global shell ` + p + "</html>"))
		return
	case strings.HasPrefix(p, "/dhis-web-login/"): // public
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>login app</html>"))
		return
	case p == "/" && c.user(r) == nil: // as central: anonymous, to the login app
		http.Redirect(w, r, "/dhis-web-login/", http.StatusFound)
		return
	case c.appFile(p) != "":
		body := c.appFile(p)
		if c.user(r) == nil {
			http.Redirect(w, r, "/dhis-web-login/", http.StatusFound)
			return
		}
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(body)))
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			c.mu.Lock()
			c.NotModified++
			c.mu.Unlock()
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(body))
		return
	case strings.HasPrefix(p, "/dhis-web-capture/"):
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><meta name="dhis2-base-url" content="http://` + r.Host + `">capture app ` + p + "</html>"))
		return
	}
	u := c.user(r)
	if u == nil {
		webMessage(w, 401, "Unauthorized", "")
		return
	}
	if c.aggregate(w, r, u) {
		return
	}
	switch {
	case p == "/api/userSettings" || p == "/api/42/userSettings":
		w.Header().Set("ETag", `"fake-settings"`)
		if r.Header.Get("If-None-Match") == `"fake-settings"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, 200, map[string]any{"keyUiLocale": "en"})
	case p == "/api/system/info":
		writeJSON(w, 200, map[string]any{"version": c.Version, "revision": "fake", "serverTimeZoneId": c.zone().String()})
	case p == "/api/me":
		ous := []any{}
		for _, ou := range u.OrgUnits {
			level := 4
			if ou == District {
				level = 3
			}
			ous = append(ous, map[string]any{"id": ou, "name": ou, "level": level, "code": "OU_" + ou, "path": c.ouPath(ou)})
		}
		groups := []any{}
		for _, g := range u.Groups {
			groups = append(groups, map[string]any{"id": g})
		}
		search := ous
		if u.SearchOrgUnits != nil {
			search = []any{}
			for _, ou := range u.SearchOrgUnits {
				search = append(search, map[string]any{"id": ou, "path": "/root/" + ou})
			}
		}
		view := ous
		if u.DataViewOrgUnits != nil {
			view = []any{}
			for _, ou := range u.DataViewOrgUnits {
				view = append(view, map[string]any{"id": ou})
			}
		}
		auths := []string{"M_dhis-web-capture"}
		if u.Authorities != nil {
			auths = u.Authorities
		}
		writeJSON(w, 200, map[string]any{"id": u.UID, "username": u.Username, "firstName": "Fake", "surname": u.Username, "organisationUnits": ous,
			"teiSearchOrganisationUnits": search, "dataViewOrganisationUnits": view, "authorities": auths, "userGroups": groups})
	case p == "/api/me/changePassword" && r.Method == "PUT":
		c.changePassword(w, r, u)
	case p == "/api/apiToken" && r.Method == "GET":
		c.mu.Lock()
		toks := []any{}
		for id, key := range c.tokenIDs {
			if c.tokens[key] == u.Username {
				toks = append(toks, map[string]any{"id": id})
			}
		}
		c.mu.Unlock()
		writeJSON(w, 200, map[string]any{"apiToken": toks})
	case p == "/api/apiToken" && r.Method == "POST" && u.NoPAT:
		webMessage(w, 403, "Access is denied", "")
	case p == "/api/apiToken" && r.Method == "POST":
		c.mu.Lock()
		c.tokenSeq++
		key := "d2p_" + strings.Repeat("x", 10) + strconv.Itoa(c.tokenSeq) + u.Username
		id := fmt.Sprintf("Tok%08d", c.tokenSeq)
		c.tokens[key], c.tokenIDs[id] = u.Username, key
		hook := c.OnTokenCreate
		c.mu.Unlock()
		if hook != nil {
			hook(u.Username)
		}
		writeJSON(w, 201, map[string]any{"httpStatus": "Created", "httpStatusCode": 201, "status": "OK",
			"response": map[string]any{"key": key, "uid": id, "responseType": "ObjectReport"}})
	case strings.HasPrefix(p, "/api/apiToken/") && r.Method == "DELETE":
		// As DHIS2 2.42: only the owner, or a member of a group the token is shared
		// with, finds the token; a superuser does not (FINDINGS.md).
		id := strings.TrimPrefix(p, "/api/apiToken/")
		c.mu.Lock()
		ok := c.tokenIDs[id] != "" && (c.tokens[c.tokenIDs[id]] == u.Username || slices.ContainsFunc(c.tokenShares[id], func(g string) bool { return slices.Contains(u.Groups, g) }))
		c.mu.Unlock()
		if !ok {
			webMessage(w, 404, "ApiToken with id "+id+" could not be found.", "E1005")
			return
		}
		c.RevokeToken(id)
		writeJSON(w, 200, map[string]any{"status": "OK"})
	case p == "/api/trackedEntityTypes":
		at := func(id string) map[string]any {
			return map[string]any{"trackedEntityAttribute": map[string]any{"id": id}}
		}
		writeJSON(w, 200, map[string]any{"trackedEntityTypes": []any{map[string]any{"id": TET,
			"trackedEntityTypeAttributes": []any{at(AttrFirstName), at(AttrLastName)}}}})
	case p == "/api/programs":
		c.programs(w, r, u)
	case p == "/api/relationshipTypes":
		writeJSON(w, 200, map[string]any{"relationshipTypes": []any{}})
	case p == "/api/programs/"+Program:
		writeJSON(w, 200, map[string]any{"id": Program, "name": "TB program"})
	case p == "/api/categoryOptionCombos":
		writeJSON(w, 200, map[string]any{"categoryOptionCombos": []any{map[string]any{"id": DefaultAOC}}})
	case p == "/api/organisationUnits/"+Facility:
		writeJSON(w, 200, map[string]any{"id": Facility, "name": "Ngelehun CHC", "code": "OU_559", "path": c.ouPath(Facility)})
	case p == "/api/tracker/events" && isEventProgram(r.URL.Query().Get("program")):
		c.listProgramEvents(w, r, u)
	case p == "/api/tracker/events" && c.gen != nil && strings.HasPrefix(r.URL.Query().Get("program"), "GenEvP"):
		c.gen.listEvents(w, r)
	case p == "/api/tracker/events" && c.gen.has(r.URL.Query().Get("enrollments")):
		c.gen.enrollmentEvents(w, r)
	case p == "/api/categoryCombos":
		c.categoryCombos(w, r, u)
	case p == "/api/tracker/trackedEntities":
		c.listTEs(w, r, u)
	case strings.HasPrefix(p, "/api/tracker/trackedEntities/"):
		if uid := strings.TrimPrefix(p, "/api/tracker/trackedEntities/"); !c.gen.getTE(w, r, uid) {
			c.getTE(w, r, u, uid)
		}
	case strings.HasPrefix(p, "/api/tracker/events/") && r.Method == "GET":
		c.getEvent(w, u, strings.TrimPrefix(p, "/api/tracker/events/"))
	case p == "/api/tracker/ownership/transfer" && r.Method == "PUT":
		c.transfer(w, r, u)
	case p == "/api/tracker/ownership/override" && r.Method == "POST":
		c.override(w, r, u)
	case strings.HasPrefix(p, "/api/tracker/jobs/") && strings.HasSuffix(p, "/report"):
		c.mu.Lock()
		b, ok := c.jobs[strings.Split(strings.TrimPrefix(p, "/api/tracker/jobs/"), "/")[0]]
		c.mu.Unlock()
		if !ok {
			webMessage(w, 404, "job not found", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	case p == "/api/tracker" && r.Method == "POST":
		c.importTracker(w, r, u)
	case strings.HasPrefix(p, "/api/trackedEntityAttributes/") && strings.HasSuffix(p, "/generateAndReserve"):
		n, _ := strconv.Atoi(r.URL.Query().Get("numberToReserve"))
		attr := strings.Split(p, "/")[3]
		var out []any
		c.mu.Lock()
		for i := 0; i < max(n, 1); i++ {
			c.reserved++
			out = append(out, map[string]any{"ownerObject": "TRACKEDENTITYATTRIBUTE", "ownerUid": attr, "key": "RANDOM(##########)",
				"value": fmt.Sprintf("%010d", c.reserved), "created": c.now(), "expiryDate": time.Now().AddDate(0, 0, 60).In(c.zone()).Format("2006-01-02T15:04:05.000")})
		}
		c.mu.Unlock()
		writeJSON(w, 200, out)
	case p == "/api/users" && r.Method == "POST":
		c.createUser(w, r)
	case p == "/api/users" && r.Method == "GET":
		c.findUser(w, r)
	case strings.HasPrefix(p, "/api/userRoles/") && r.Method == "GET":
		id := strings.TrimPrefix(p, "/api/userRoles/")
		c.mu.Lock()
		auths := c.RoleAuthorities[id]
		c.mu.Unlock()
		if auths == nil {
			auths = []string{}
		}
		writeJSON(w, 200, map[string]any{"id": id, "authorities": auths})
	case strings.HasPrefix(p, "/api/users/") && r.Method == "PATCH":
		c.patchUser(w, r, strings.TrimPrefix(p, "/api/users/"))
	case strings.HasPrefix(p, "/api/userGroups/") && r.Method == "POST":
		c.addToGroup(w, p)
	case p == "/api/sharing" && r.URL.Query().Get("type") == "apiToken":
		c.shareToken(w, r, u)
	case p == "/api/sharing" && r.URL.Query().Get("type") == "dataStore":
		c.shareEntry(w, r)
	case strings.HasPrefix(p, "/api/dataStore/"):
		c.dataStore(w, r, u)
	case strings.HasPrefix(p, "/api/validation/dataSet/") || strings.HasPrefix(p, "/api/42/validation/dataSet/"):
		// The Data Entry app's "Run validation": no rules here, so no violations.
		writeJSON(w, 200, map[string]any{"validationRuleViolations": []any{}, "commentRequiredViolations": []any{}})
	default:
		webMessage(w, 404, "not found in fake central: "+p, "")
	}
}

// shareToken handles POST /api/sharing?type=apiToken&id=…, by the token's owner only.
func (c *Central) shareToken(w http.ResponseWriter, r *http.Request, u *User) {
	id := r.URL.Query().Get("id")
	var req struct {
		Object struct {
			UserGroupAccesses []struct{ ID string } `json:"userGroupAccesses"`
		} `json:"object"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokenIDs[id] == "" || c.tokens[c.tokenIDs[id]] != u.Username {
		webMessage(w, 404, "ApiToken with id "+id+" could not be found.", "E1005")
		return
	}
	var groups []string
	for _, g := range req.Object.UserGroupAccesses {
		groups = append(groups, g.ID)
	}
	c.tokenShares[id] = groups
	writeJSON(w, 200, map[string]any{"status": "OK", "message": "Access control set"})
}

// TokenSharedWith returns the user groups a PAT is shared with.
func (c *Central) TokenSharedWith(id string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokenShares[id]
}

// RevokeToken deletes a PAT by uid (to test 401 on push).
// TokenValid reports whether a PAT key is still accepted.
func (c *Central) TokenValid(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens[key] != ""
}

func (c *Central) RevokeToken(id string) {
	c.mu.Lock()
	delete(c.tokens, c.tokenIDs[id])
	delete(c.tokenIDs, id)
	c.mu.Unlock()
}

// RevokeTokensOf deletes every PAT of a user.
func (c *Central) RevokeTokensOf(username string) {
	c.mu.Lock()
	for k, u := range c.tokens {
		if u == username {
			delete(c.tokens, k)
		}
	}
	c.mu.Unlock()
}

func (c *Central) login(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	json.NewDecoder(r.Body).Decode(&req)
	c.mu.Lock()
	u := c.Users[req.Username]
	pw := Password
	if own, ok := c.passwords[req.Username]; ok {
		pw = own // a user created through the API has its own password
	}
	c.mu.Unlock()
	if u == nil || u.Disabled || req.Password != pw {
		webMessage(w, 401, "Bad credentials", "")
		return
	}
	sid := fmt.Sprintf("s%d%s", time.Now().UnixNano(), u.Username)
	c.mu.Lock()
	c.sessions[sid] = u.Username
	c.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "JSESSIONID_fake", Value: sid, Path: "/", HttpOnly: true})
	writeJSON(w, 200, map[string]any{"loginStatus": "SUCCESS", "redirectUrl": "/"})
}

func dataPerm(read, write bool) map[string]any {
	return map[string]any{"data": map[string]any{"read": read, "write": write}}
}

func (c *Central) programs(w http.ResponseWriter, r *http.Request, u *User) {
	if u.NoProgramMeta {
		writeJSON(w, 200, map[string]any{"programs": []any{}})
		return
	}
	attr := func(id string, gen, uniq bool) map[string]any {
		return map[string]any{"trackedEntityAttribute": map[string]any{"id": id, "generated": gen, "unique": uniq,
			"pattern": map[bool]string{true: "RANDOM(##########)"}[gen]}}
	}
	var stages []any
	for i, s := range []string{Stage1, Stage2, Stage3} {
		stages = append(stages, map[string]any{"id": s, "sortOrder": i + 1, "repeatable": true, "access": dataPerm(u.StageRead[s], u.StageWrite[s])})
	}
	prog := map[string]any{"id": Program, "name": "TB program", "programType": "WITH_REGISTRATION", "access": dataPerm(u.ProgramRead, u.ProgramWrite),
		"onlyEnrollOnce": c.OnlyEnrollOnce, "selectEnrollmentDatesInFuture": c.FutureEnrollmentDates,
		"selectIncidentDatesInFuture": c.FutureIncidentDates,
		"trackedEntityType": map[string]any{"id": TET, "access": dataPerm(true, true),
			"trackedEntityTypeAttributes": []any{attr(AttrFirstName, false, false), attr(AttrLastName, false, false)}},
		"programStages":                  stages,
		"programTrackedEntityAttributes": []any{attr(AttrNationalI, false, true), attr(AttrTBID, true, true)}}
	all := []any{prog}
	if c.gen != nil {
		all = append(all, c.gen.programMeta()...)
	}
	all = append(all, eventProgramMeta(u)...)
	if c.gen != nil {
		all = append(all, c.gen.eventProgramMeta()...)
	}
	// The filters the hub uses: programType:eq:X and id:in:[a,b].
	var progs []any
	for _, x := range all {
		pm := x.(map[string]any)
		keep := true
		for _, f := range r.URL.Query()["filter"] {
			if t, ok := strings.CutPrefix(f, "programType:eq:"); ok {
				pt, _ := pm["programType"].(string)
				keep = keep && (pt == t || (pt == "" && t == "WITH_REGISTRATION"))
			}
			if l, ok := strings.CutPrefix(f, "id:in:["); ok {
				keep = keep && slices.Contains(strings.Split(strings.TrimSuffix(l, "]"), ","), pm["id"].(string))
			}
		}
		if keep {
			progs = append(progs, pm)
		}
	}
	writeJSON(w, 200, map[string]any{"programs": orEmpty(progs)})
}

// canSeeEvent applies stage read access (central drops unreadable events silently).
func canSeeEvent(u *User, ev tracker.M) bool { return u.StageRead[tracker.Str(ev, "programStage")] }

func (c *Central) listTEs(w http.ResponseWriter, r *http.Request, u *User) {
	if c.gen != nil && c.gen.listTEs(w, r) {
		return
	}
	q := r.URL.Query()
	byType := q.Get("program") == "" && q.Get("trackedEntityType") == TET // registered here, no enrollments
	if !byType && (q.Get("program") != Program || !u.ProgramRead) {
		webMessage(w, 400, "Program is specified but does not exist", "E1003")
		return
	}
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	page, size = max(page, 1), max(size, 1)
	after := q.Get("updatedAfter")
	withDeleted := q.Get("includeDeleted") == "true"
	if withDeleted && q.Get("trackedEntityType") != "" {
		// As 2.42: the query central builds for this is broken SQL.
		webMessage(w, 409, "Query failed because of a syntax error (SqlState: 42601)", "E7145")
		return
	}
	var only map[string]bool
	if v := q.Get("trackedEntities"); v != "" {
		only = map[string]bool{}
		for _, u := range strings.Split(v, ",") {
			only[u] = true
		}
	}
	c.mu.Lock()
	c.teLists = append(c.teLists, r.URL.RawQuery)
	// Each person's enrollments and each enrollment's events, in the order stored: one
	// pass, so a listing costs the same with tens of thousands of objects held.
	enrsOf, evsOf := map[string][]string{}, map[string][]string{}
	for _, x := range c.order {
		switch c.kinds[x] {
		case "enrollment":
			te := tracker.Str(c.objects[x], "trackedEntity")
			enrsOf[te] = append(enrsOf[te], x)
		case "event":
			en := tracker.Str(c.objects[x], "enrollment")
			evsOf[en] = append(evsOf[en], x)
		}
	}
	var tes []tracker.M
	for _, uid := range c.order {
		o := c.objects[uid]
		if c.kinds[uid] != "trackedEntity" || (only != nil && !only[uid]) || (o["deleted"] == true && !withDeleted) {
			continue
		}
		if byType && o["orgUnit"] != q.Get("orgUnits") {
			continue // registered elsewhere
		}
		if !byType && (c.owner(uid) != q.Get("orgUnits") || !c.canRead(u, uid)) {
			continue // owned elsewhere for the program, or protected from this user
		}
		te := deepCopy(o)
		var enrs []any
		latest := tracker.Str(te, "updatedAt")
		for _, eu := range enrsOf[uid] {
			e := c.objects[eu]
			if (e["deleted"] == true && !withDeleted) || (!byType && tracker.Str(e, "program") != q.Get("program")) {
				continue // a program's listing holds only its enrollments
			}
			en := deepCopy(e)
			latest = max(latest, tracker.Str(en, "updatedAt"))
			var evs []any
			for _, vu := range evsOf[eu] {
				ev := c.objects[vu]
				if canSeeEvent(u, ev) && (ev["deleted"] != true || withDeleted) {
					evs = append(evs, deepCopy(ev))
					latest = max(latest, tracker.Str(ev, "updatedAt"))
				}
			}
			en["events"] = orEmpty(evs)
			enrs = append(enrs, en)
		}
		if byType {
			if only != nil && (after == "" || latest >= after) {
				if c.fetched == nil {
					c.fetched = map[string]int{}
				}
				c.fetched[uid]++
			}
			if after == "" || latest >= after {
				te["relationships"] = []any{}
				tes = append(tes, te)
			}
			continue
		}
		if len(enrs) == 0 || (after != "" && latest < after) {
			continue
		}
		if es := q.Get("enrollmentStatus"); es != "" && !slices.ContainsFunc(enrs, func(x any) bool { return x.(tracker.M)["status"] == es }) {
			continue
		}
		if only != nil {
			if c.fetched == nil {
				c.fetched = map[string]int{}
			}
			c.fetched[uid]++
		}
		te["enrollments"] = enrs
		te["relationships"] = []any{}
		te["programOwners"] = []any{tracker.M{"orgUnit": c.owner(uid), "trackedEntity": uid, "program": Program}}
		tes = append(tes, te)
	}
	c.mu.Unlock()
	start := (page - 1) * size
	var items []any
	for i := start; i < len(tes) && i < start+size; i++ {
		items = append(items, tes[i])
	}
	pager := map[string]any{"page": page, "pageSize": size}
	if len(items) == size {
		next := *r.URL
		qq := next.Query()
		qq.Set("page", strconv.Itoa(page+1))
		next.RawQuery = qq.Encode()
		pager["nextPage"] = "http://" + r.Host + next.String()
	}
	writeJSON(w, 200, map[string]any{"pager": pager, "trackedEntities": orEmpty(items)})
}

func orEmpty(a []any) []any {
	if a == nil {
		return []any{}
	}
	return a
}

func deepCopy(m tracker.M) tracker.M {
	b, _ := json.Marshal(m)
	var out tracker.M
	json.Unmarshal(b, &out)
	return out
}

type errReport struct {
	Message     string `json:"message"`
	ErrorCode   string `json:"errorCode"`
	TrackerType string `json:"trackerType"`
	UID         string `json:"uid"`
}

// importTracker implements a synchronous import with central's 2.42 answers:
// OBJECT mode gives 200/OK even with errors; ALL with errors gives 409/ERROR.
func (c *Central) importTracker(w http.ResponseWriter, r *http.Request, u *User) {
	if r.URL.Query().Get("async") != "false" {
		// Run the import now and keep the report for GET /api/tracker/jobs/{id}/report.
		q := r.URL.Query()
		q.Set("async", "false")
		r2 := r.Clone(r.Context())
		r2.URL.RawQuery = q.Encode()
		rec := httptest.NewRecorder()
		c.importTracker(rec, r2, u)
		c.mu.Lock()
		c.jobN++
		id := fmt.Sprintf("FakeJob%04d", c.jobN)
		c.jobs[id] = rec.Body.Bytes()
		c.mu.Unlock()
		writeJSON(w, 200, map[string]any{"httpStatus": "OK", "httpStatusCode": 200, "status": "OK", "message": "Tracker job added",
			"response": map[string]any{"id": id, "location": "http://" + r.Host + "/tracker/jobs/" + id, "responseType": "TrackerJob"}})
		return
	}
	var payload tracker.M
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, &payload); err != nil {
		webMessage(w, 400, "bad json", "")
		return
	}
	atomicAll := r.URL.Query().Get("atomicMode") != "OBJECT"
	items := tracker.Flatten(payload)
	c.mu.Lock()
	defer c.mu.Unlock()
	failed := map[string]bool{}
	var errs []errReport
	var okItems []*tracker.Item
	for _, it := range items {
		tt := tracker.TrackerType(it.Kind)
		fail := func(code, msg string) {
			errs = append(errs, errReport{Message: msg, ErrorCode: code, TrackerType: tt, UID: it.UID})
			failed[it.UID] = true
		}
		if old := c.objects[it.UID]; old != nil && old["deleted"] == true {
			// Central's soft delete stands: a change of a deleted object is refused.
			code := map[string]string{"trackedEntity": "E1114", "enrollment": "E1113", "event": "E1082", "relationship": "E4017"}[it.Kind]
			label := map[string]string{"trackedEntity": "TrackedEntity", "enrollment": "Enrollment", "event": "Event", "relationship": "Relationship"}[it.Kind]
			fail(code, fmt.Sprintf("%s: `%s`, is already deleted and can't be modified.", label, it.UID))
			continue
		}
		switch it.Kind {
		case "trackedEntity":
			if !slices.Contains(u.OrgUnits, tracker.Str(it.Data, "orgUnit")) {
				fail("E1000", "User has no write access to OrganisationUnit")
				continue
			}
			for _, a := range it.Data["attributes"].([]any) {
				am := a.(tracker.M)
				if am["attribute"] != AttrNationalI {
					continue
				}
				for ou, o := range c.objects {
					if ou == it.UID || c.kinds[ou] != "trackedEntity" {
						continue
					}
					for _, oa := range o["attributes"].([]any) {
						if oa.(tracker.M)["attribute"] == AttrNationalI && oa.(tracker.M)["value"] == am["value"] {
							fail("E1064", fmt.Sprintf("Non-unique attribute value `%v` for attribute `%s`.", am["value"], AttrNationalI))
						}
					}
				}
			}
		case "enrollment":
			if failed[it.TE] {
				fail("E5000", fmt.Sprintf("enrollment `%s` cannot be created because trackedEntity `%s` referenced by it could not be created.", it.UID, it.TE))
			} else if c.objects[it.TE] == nil && !slices.ContainsFunc(okItems, func(x *tracker.Item) bool { return x.UID == it.TE }) {
				fail("E1068", "Could not find TrackedEntity")
			} else if !u.ProgramWrite {
				fail("E1091", "no data write access to Program")
			} else {
				if c.objects[it.UID] == nil {
					for _, f := range c.enrollOnce(it, okItems) {
						fail(f[0], f[1])
					}
				}
				for _, f := range c.futureDates(it) {
					fail(f[0], f[1])
				}
			}
		case "event":
			if prog := tracker.Str(it.Data, "program"); isEventProgram(prog) {
				if _, writes := aggUsers[u.Username]; !writes {
					fail("E1091", "User has no data write access to Program")
				} else if !mayUseEvent(u, it.Data) {
					fail("E1099", "User: `"+u.UID+"`, has no write access to CategoryOption: `"+donorOption(tracker.Str(it.Data, "attributeOptionCombo"))+"`.")
				}
				break
			}
			if failed[it.Enrollment] {
				failed[it.UID] = true // ignored without an error report of its own (as observed)
				continue
			}
			if c.gen.has(it.Enrollment) {
				break // a generated enrollment: every generated user may write in it
			}
			if !u.StageWrite[tracker.Str(it.Data, "programStage")] {
				fail("E1095", "User has no data write access to ProgramStage")
			} else if c.objects[it.Enrollment] == nil && !slices.ContainsFunc(okItems, func(x *tracker.Item) bool { return x.UID == it.Enrollment }) {
				fail("E1313", "Event of an Enrollment does not reference a TrackedEntity.")
			}
		}
		if !failed[it.UID] {
			okItems = append(okItems, it)
		}
	}
	if atomicAll && len(failed) > 0 {
		writeJSON(w, 409, map[string]any{"status": "ERROR", "validationReport": map[string]any{"errorReports": errs, "warningReports": []any{}},
			"stats": map[string]int{"ignored": len(items), "total": len(items)}})
		return
	}
	tm := map[string]any{}
	for _, tt := range []string{"TRACKED_ENTITY", "ENROLLMENT", "EVENT", "RELATIONSHIP"} {
		tm[tt] = map[string]any{"trackerType": tt, "objectReports": []any{}}
	}
	ts := c.now()
	for _, it := range okItems {
		data := deepCopy(it.Data)
		if old := c.objects[it.UID]; old != nil {
			data["createdBy"], data["createdAt"] = old["createdBy"], old["createdAt"]
		} else {
			data["createdBy"], data["createdAt"] = map[string]any{"uid": u.UID, "username": u.Username}, ts
		}
		data["updatedBy"], data["updatedAt"] = map[string]any{"uid": u.UID, "username": u.Username}, ts
		if it.Kind == "event" && data["trackedEntity"] == nil {
			if e := c.objects[it.Enrollment]; e != nil {
				data["trackedEntity"] = e["trackedEntity"]
			}
		}
		c.put(it.Kind, it.UID, data)
		tt := tracker.TrackerType(it.Kind)
		rep := tm[tt].(map[string]any)
		rep["objectReports"] = append(rep["objectReports"].([]any), map[string]any{"trackerType": tt, "uid": it.UID, "errorReports": []any{}})
	}
	writeJSON(w, 200, map[string]any{"status": "OK", "validationReport": map[string]any{"errorReports": orEmptyErr(errs), "warningReports": []any{}},
		"stats":        map[string]int{"created": len(okItems), "ignored": len(failed), "total": len(items)},
		"bundleReport": map[string]any{"typeReportMap": tm}})
}

func orEmptyErr(e []errReport) []errReport {
	if e == nil {
		return []errReport{}
	}
	return e
}

func (c *Central) dataStore(w http.ResponseWriter, r *http.Request, u *User) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/dataStore/"), "/")
	ns := parts[0]
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.HubWriters != nil && r.Method != "GET" && strings.HasPrefix(ns, "facility-hub-") && !c.HubWriters[u.Username] {
		// The namespaces protected as DEPLOYMENT.md advises: only some accounts write.
		webMessage(w, 403, "Access denied for key", "E1102")
		return
	}
	if c.DataStore[ns] == nil {
		c.DataStore[ns] = map[string]json.RawMessage{}
	}
	if len(parts) == 1 { // query: ?fields=.&filter=path:eq:value
		var entries []any
		f := strings.SplitN(r.URL.Query().Get("filter"), ":", 3)
		keys := make([]string, 0)
		for k := range c.DataStore[ns] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			var v map[string]any
			json.Unmarshal(c.DataStore[ns][k], &v)
			if len(f) == 3 && fmt.Sprint(v[f[0]]) != f[2] {
				continue
			}
			entries = append(entries, map[string]any{"key": k, "value": v})
		}
		writeJSON(w, 200, map[string]any{"pager": map[string]any{"page": 1, "pageSize": 50}, "entries": orEmpty(entries)})
		return
	}
	key, _ := url.PathUnescape(parts[1])
	id := entryID(ns, key)
	// Public access as 2.42 reports it: read and write until sharing sets it, read only
	// in the namespaces protected with HubWriters.
	pub, shared := c.public[id]
	if !shared {
		pub = "rw------"
		if c.HubWriters != nil && strings.HasPrefix(ns, "facility-hub-") {
			pub = "r-------"
		}
	}
	if len(parts) == 3 && parts[2] == "metaData" {
		if _, ok := c.DataStore[ns][key]; !ok {
			webMessage(w, 404, "Key not found", "E1005")
			return
		}
		var ownerUID string
		if cr := c.Users[c.creators[id]]; cr != nil {
			ownerUID = cr.UID
		}
		writeJSON(w, 200, map[string]any{"id": id, "namespace": ns, "key": key,
			"createdBy": map[string]any{"username": c.creators[id]},
			"sharing":   map[string]any{"owner": ownerUID, "public": pub}})
		return
	}
	// Shared with some users only; the superuser and the entry's owner (its creator)
	// always have access, as on DHIS2.
	owner := u.Username == "hub_admin" || c.creators[id] == u.Username
	if rs := c.readers[id]; rs != nil && !rs[u.UID] && !owner && pub[0] != 'r' {
		webMessage(w, 403, "Access denied for key", "E1102")
		return
	}
	if ws := c.writers[id]; c.readers[id] != nil && r.Method != "GET" && !ws[u.UID] && !owner && pub[1] != 'w' {
		webMessage(w, 403, "Access denied for key", "E1102")
		return
	}
	if _, ok := c.DataStore[ns][key]; !ok && (r.Method == "POST" || r.Method == "PUT") {
		if c.creators == nil {
			c.creators = map[string]string{}
		}
		c.creators[id] = u.Username
	}
	b, _ := io.ReadAll(r.Body)
	switch r.Method {
	case "GET":
		v, ok := c.DataStore[ns][key]
		if !ok {
			webMessage(w, 404, "Key not found", "")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(v)
	case "POST":
		if _, ok := c.DataStore[ns][key]; ok {
			webMessage(w, 409, "Key already exists", "E1004")
			return
		}
		c.DataStore[ns][key] = b
		writeJSON(w, 201, map[string]any{"status": "OK"})
	case "PUT":
		c.DataStore[ns][key] = b
		writeJSON(w, 200, map[string]any{"status": "OK"})
	case "DELETE":
		if _, ok := c.DataStore[ns][key]; !ok {
			webMessage(w, 404, "Key not found", "")
			return
		}
		delete(c.DataStore[ns], key)
		delete(c.creators, id)
		delete(c.readers, id)
		delete(c.writers, id)
		delete(c.public, id)
		writeJSON(w, 200, map[string]any{"status": "OK"})
	}
}
