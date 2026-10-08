// Package fakecentral is a small stand-in for a DHIS2 2.42 server, shaped after the
// fixtures in testdata/2.42. It covers what the hub's tests need: login, /api/me,
// personal access tokens, program metadata and sharing, tracker read/write with the
// error codes central returns, data store, reserved values, and a "down" switch that
// drops connections like a cut uplink.
package fakecentral

import (
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
}

type Central struct {
	mu        sync.Mutex
	Users     map[string]*User
	tokens    map[string]string // PAT key -> username
	tokenSeq  int
	tokenIDs  map[string]string // token uid -> key
	sessions  map[string]string // cookie -> username
	objects   map[string]tracker.M
	kinds     map[string]string
	order     []string
	DataStore map[string]map[string]json.RawMessage
	reserved  int
	down      atomic.Bool
	Version   string
	// Requests counts calls per "METHOD path" for assertions.
	Requests map[string]int
	gen      *generated // Generate: performance-test data
	jobs     map[string][]byte
	jobN     int
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
			"outsider_d": {UID: "HubUsrOutsD", Username: "outsider_d", OrgUnits: []string{OtherFacility}, ProgramRead: true, ProgramWrite: true, StageRead: no3, StageWrite: no3},
		},
		tokens: map[string]string{}, tokenIDs: map[string]string{}, sessions: map[string]string{},
		objects: map[string]tracker.M{}, kinds: map[string]string{}, DataStore: map[string]map[string]json.RawMessage{},
		Version: Version, Requests: map[string]int{}, jobs: map[string][]byte{},
	}
	return c
}

// SetDown makes every request fail at the connection level (a cut uplink).
func (c *Central) SetDown(down bool) { c.down.Store(down) }

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000") }

// Seed adds a tracked entity owned by the facility, enrolled in the program.
func (c *Central) Seed(te, enr, firstName, nationalID string, events map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := now()
	c.put("trackedEntity", te, tracker.M{"trackedEntity": te, "trackedEntityType": TET, "orgUnit": Facility, "createdAt": ts, "updatedAt": ts,
		"attributes": []any{tracker.M{"attribute": AttrFirstName, "value": firstName}, tracker.M{"attribute": AttrNationalI, "value": nationalID}}})
	c.put("enrollment", enr, tracker.M{"enrollment": enr, "trackedEntity": te, "program": Program, "orgUnit": Facility, "status": "ACTIVE",
		"enrolledAt": ts, "createdAt": ts, "updatedAt": ts})
	for ev, stage := range events {
		c.put("event", ev, tracker.M{"event": ev, "enrollment": enr, "trackedEntity": te, "program": Program, "programStage": stage,
			"orgUnit": Facility, "status": "ACTIVE", "occurredAt": ts, "createdAt": ts, "updatedAt": ts, "dataValues": []any{}})
	}
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
		if p == Password {
			return c.Users[u]
		}
		return nil
	}
	if ck, err := r.Cookie("JSESSIONID_fake"); err == nil {
		return c.Users[c.sessions[ck.Value]]
	}
	return nil
}

func (c *Central) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	c.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/api/system/ping":
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("pong"))
		return
	case p == "/api/auth/login":
		c.login(w, r)
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
	switch {
	case p == "/api/system/info":
		writeJSON(w, 200, map[string]any{"version": c.Version, "revision": "fake"})
	case p == "/api/me":
		ous := []any{}
		for _, ou := range u.OrgUnits {
			ous = append(ous, map[string]any{"id": ou, "name": ou, "level": 4, "code": "OU_" + ou, "path": "/root/" + ou})
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
		writeJSON(w, 200, map[string]any{"id": u.UID, "username": u.Username, "organisationUnits": ous,
			"teiSearchOrganisationUnits": search, "authorities": []string{"M_dhis-web-capture"}, "userGroups": groups})
	case p == "/api/apiToken" && r.Method == "POST" && u.NoPAT:
		webMessage(w, 403, "Access is denied", "")
	case p == "/api/apiToken" && r.Method == "POST":
		c.mu.Lock()
		c.tokenSeq++
		key := "d2p_" + strings.Repeat("x", 10) + strconv.Itoa(c.tokenSeq) + u.Username
		id := fmt.Sprintf("Tok%08d", c.tokenSeq)
		c.tokens[key], c.tokenIDs[id] = u.Username, key
		c.mu.Unlock()
		writeJSON(w, 201, map[string]any{"httpStatus": "Created", "httpStatusCode": 201, "status": "OK",
			"response": map[string]any{"key": key, "uid": id, "responseType": "ObjectReport"}})
	case strings.HasPrefix(p, "/api/apiToken/") && r.Method == "DELETE":
		c.RevokeToken(strings.TrimPrefix(p, "/api/apiToken/"))
		writeJSON(w, 200, map[string]any{"status": "OK"})
	case p == "/api/trackedEntityTypes":
		at := func(id string) map[string]any {
			return map[string]any{"trackedEntityAttribute": map[string]any{"id": id}}
		}
		writeJSON(w, 200, map[string]any{"trackedEntityTypes": []any{map[string]any{"id": TET,
			"trackedEntityTypeAttributes": []any{at(AttrFirstName), at(AttrLastName)}}}})
	case p == "/api/programs":
		c.programs(w, r, u)
	case p == "/api/programs/"+Program:
		writeJSON(w, 200, map[string]any{"id": Program, "name": "TB program"})
	case p == "/api/categoryOptionCombos":
		writeJSON(w, 200, map[string]any{"categoryOptionCombos": []any{map[string]any{"id": DefaultAOC}}})
	case p == "/api/organisationUnits/"+Facility:
		writeJSON(w, 200, map[string]any{"id": Facility, "name": "Ngelehun CHC", "code": "OU_559"})
	case p == "/api/tracker/trackedEntities":
		c.listTEs(w, r, u)
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
				"value": fmt.Sprintf("%010d", c.reserved), "created": now(), "expiryDate": time.Now().AddDate(0, 0, 60).UTC().Format("2006-01-02T15:04:05.000")})
		}
		c.mu.Unlock()
		writeJSON(w, 200, out)
	case strings.HasPrefix(p, "/api/dataStore/"):
		c.dataStore(w, r)
	default:
		webMessage(w, 404, "not found in fake central: "+p, "")
	}
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
	c.mu.Unlock()
	if u == nil || u.Disabled || req.Password != Password {
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
	prog := map[string]any{"id": Program, "name": "TB program", "access": dataPerm(u.ProgramRead, u.ProgramWrite),
		"trackedEntityType": map[string]any{"id": TET, "access": dataPerm(true, true),
			"trackedEntityTypeAttributes": []any{attr(AttrFirstName, false, false), attr(AttrLastName, false, false)}},
		"programStages":                  stages,
		"programTrackedEntityAttributes": []any{attr(AttrNationalI, false, true), attr(AttrTBID, true, true)}}
	progs := []any{prog}
	if c.gen != nil {
		progs = append(progs, c.gen.programMeta()...)
	}
	writeJSON(w, 200, map[string]any{"programs": progs})
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
	var only map[string]bool
	if v := q.Get("trackedEntities"); v != "" {
		only = map[string]bool{}
		for _, u := range strings.Split(v, ",") {
			only[u] = true
		}
	}
	c.mu.Lock()
	var tes []tracker.M
	for _, uid := range c.order {
		o := c.objects[uid]
		if c.kinds[uid] != "trackedEntity" || o["orgUnit"] != q.Get("orgUnits") || (only != nil && !only[uid]) {
			continue
		}
		te := deepCopy(o)
		var enrs []any
		latest := tracker.Str(te, "updatedAt")
		for _, eu := range c.order {
			e := c.objects[eu]
			if c.kinds[eu] != "enrollment" || e["trackedEntity"] != uid {
				continue
			}
			en := deepCopy(e)
			latest = max(latest, tracker.Str(en, "updatedAt"))
			var evs []any
			for _, vu := range c.order {
				ev := c.objects[vu]
				if c.kinds[vu] == "event" && ev["enrollment"] == eu && canSeeEvent(u, ev) {
					evs = append(evs, deepCopy(ev))
					latest = max(latest, tracker.Str(ev, "updatedAt"))
				}
			}
			en["events"] = orEmpty(evs)
			enrs = append(enrs, en)
		}
		if byType {
			if after == "" || latest >= after {
				te["relationships"] = []any{}
				tes = append(tes, te)
			}
			continue
		}
		if len(enrs) == 0 || (after != "" && latest < after) {
			continue
		}
		te["enrollments"] = enrs
		te["relationships"] = []any{}
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
			}
		case "event":
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
	ts := now()
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

func (c *Central) dataStore(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/dataStore/"), "/")
	ns := parts[0]
	c.mu.Lock()
	defer c.mu.Unlock()
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
	}
}
