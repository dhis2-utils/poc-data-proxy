package fakecentral

import (
	"cmp"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// Scale is generated facility data for the performance test (docs/PERFORMANCE.md):
// tracked entities, enrollments spread over programs and years, and events, all owned
// by the facility, plus users who may read and write all of it. Nothing is held in
// memory but per-program indexes; each pull page is rendered when asked for, the same
// for the same Scale.
type Scale struct {
	TEs, Enrollments, Events int
	Programs                 int // generated tracker programs, each with three repeatable stages
	Users                    int // perf_001 … with Password
	Years                    int // enrollment dates spread over this many years up to now
	// EventPrograms generated event programs (GenEvP…), each with ProgramEvents events
	// whose dates are spread over the last 36 months.
	EventPrograms, ProgramEvents int
}

type generated struct {
	Scale
	start, span time.Duration // enrollment dates: base - start + j*span/Enrollments
	base        time.Time     // when the data was generated: dates are fixed from it
	updatedAt   string        // the newest update time: active enrollments and their people
	byProgram   [][]int32     // program -> enrollment indexes, in order
	mu          sync.Mutex
	filtered    map[string][]int32 // listings with updatedAfter or enrollmentStatus, by query
}

func GenProgram(p int) string       { return fmt.Sprintf("GenPrg%05d", p) }
func GenStage(p, s int) string      { return fmt.Sprintf("GenSt%03d%03d", p, s) }
func genDE(i int) string            { return fmt.Sprintf("GenDe%06d", i) }
func GenUser(i int) string          { return fmt.Sprintf("perf_%03d", i+1) }
func genTE(i int) string            { return fmt.Sprintf("Te%09d", i) }
func genEnrollment(j int) string    { return fmt.Sprintf("En%09d", j) }
func genEvent(j, k, per int) string { return fmt.Sprintf("Ev%09d", j*per+k) }

var firstNames = []string{"Amina", "Bongani", "Chen", "Dalia", "Emeka", "Farida", "Goran", "Hana", "Ivan", "Jamal", "Kofi", "Lina", "Moses", "Nadia", "Omar", "Priya"}
var lastNames = []string{"Abebe", "Banda", "Chanda", "Diallo", "Eze", "Fofana", "Gueye", "Hassan", "Ibrahim", "Juma", "Kamara", "Lungu", "Mensah", "Ndlovu", "Okafor", "Phiri"}

// Generate switches the fake to generated data at this scale. Call it before serving.
func (c *Central) Generate(s Scale) {
	s.Programs, s.Years = max(s.Programs, 1), max(s.Years, 1)
	s.TEs, s.Enrollments = max(s.TEs, 1), max(s.Enrollments, 1)
	g := &generated{Scale: s, start: time.Duration(s.Years) * 365 * 24 * time.Hour,
		updatedAt: c.now(), byProgram: make([][]int32, s.Programs), filtered: map[string][]int32{}}
	g.base, _ = time.Parse("2006-01-02T15:04:05.000", g.updatedAt)
	g.span = g.start
	for j := 0; j < s.Enrollments; j++ {
		p := g.program(j)
		g.byProgram[p] = append(g.byProgram[p], int32(j))
	}
	all := map[string]bool{Stage1: true, Stage2: true, Stage3: true}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 0; i < s.Users; i++ {
		u := GenUser(i)
		c.Users[u] = &User{UID: fmt.Sprintf("PerfUsr%04d", i+1), Username: u, OrgUnits: []string{Facility},
			ProgramRead: true, ProgramWrite: true, StageRead: all, StageWrite: all}
	}
	c.gen = g
}

// Enrollment j belongs to tracked entity j mod TEs, and the program shifts with each
// round, so a person is never enrolled twice in one program while rounds < programs.
func (g *generated) te(j int) int      { return j % g.TEs }
func (g *generated) program(j int) int { return (j + j/g.TEs) % g.Programs }

// eventsOf is how many events enrollment j has: an even share, the rest to the first.
func (g *generated) eventsOf(j int) int {
	per := g.Events / g.Enrollments
	if j < g.Events%g.Enrollments {
		per++
	}
	return per
}

func (g *generated) perMax() int { return g.Events/g.Enrollments + 1 }

func (g *generated) date(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000") }

// enrolled is enrollment j's date; an enrollment over a year old is completed, a
// year after it began, and was last changed then (with its events). Active ones and
// their people carry the generation time.
func (g *generated) enrolled(j int) time.Time {
	return g.base.Add(-g.start + time.Duration(int64(g.span)/int64(g.Enrollments)*int64(j)))
}

func (g *generated) status(j int) string {
	if g.enrolled(j).Before(g.base.AddDate(-1, 0, 0)) {
		return "COMPLETED"
	}
	return "ACTIVE"
}

func (g *generated) enrollmentUpdated(j int) string {
	if g.status(j) == "COMPLETED" {
		return g.date(g.enrolled(j).AddDate(1, 0, 0))
	}
	return g.updatedAt
}

// teUpdated is a person's last change: the newest of their enrollments'.
func (g *generated) teUpdated(i int) string {
	out := ""
	for j := i; j < g.Enrollments; j += g.TEs {
		out = max(out, g.enrollmentUpdated(j))
	}
	return cmp.Or(out, g.updatedAt)
}

func (g *generated) renderTE(i int) tracker.M {
	upd := g.teUpdated(i)
	return tracker.M{"trackedEntity": genTE(i), "trackedEntityType": TET, "orgUnit": Facility,
		"createdAt": g.updatedAt, "updatedAt": upd, "deleted": false, "inactive": false,
		"attributes": []any{
			tracker.M{"attribute": AttrFirstName, "value": firstNames[i%len(firstNames)]},
			tracker.M{"attribute": AttrLastName, "value": lastNames[(i/len(firstNames))%len(lastNames)]},
		},
		"relationships": []any{}}
}

func (g *generated) renderEnrollment(j int) tracker.M {
	p, te := g.program(j), genTE(g.te(j))
	enrolled, status, upd := g.enrolled(j), g.status(j), g.enrollmentUpdated(j)
	var events []any
	per := g.perMax()
	for k := 0; k < g.eventsOf(j); k++ {
		at := g.date(enrolled.AddDate(0, 0, 30*k))
		var dvs []any
		for d := 0; d < 10; d++ {
			dvs = append(dvs, tracker.M{"dataElement": genDE(d), "value": strconv.Itoa((j*7+k*13+d)%997) + " units",
				"createdAt": at, "updatedAt": at, "storedBy": "perf"})
		}
		events = append(events, tracker.M{"event": genEvent(j, k, per), "enrollment": genEnrollment(j), "trackedEntity": te,
			"program": GenProgram(p), "programStage": GenStage(p, k%3), "orgUnit": Facility, "status": "COMPLETED",
			"occurredAt": at, "createdAt": at, "updatedAt": upd, "attributeOptionCombo": DefaultAOC,
			"deleted": false, "dataValues": dvs, "notes": []any{}, "relationships": []any{}})
	}
	return tracker.M{"enrollment": genEnrollment(j), "trackedEntity": te, "program": GenProgram(p), "orgUnit": Facility,
		"status": status, "enrolledAt": g.date(enrolled), "occurredAt": g.date(enrolled), "createdAt": g.date(enrolled),
		"updatedAt": upd, "deleted": false, "events": orEmpty(events), "notes": []any{}, "relationships": []any{}}
}

// listing is what a listing returns, in order: enrollment indexes of a program, or
// person indexes for the type (program < 0), kept to those whose person changed at or
// after updatedAfter and, for a program, whose enrollment has enrollmentStatus.
func (g *generated) listing(program int, after, status string) []int32 {
	if program >= 0 && after == "" && status == "" {
		return g.byProgram[program]
	}
	key := fmt.Sprint(program, "|", after, "|", status)
	g.mu.Lock()
	defer g.mu.Unlock()
	if l, ok := g.filtered[key]; ok {
		return l
	}
	var out []int32
	if program < 0 {
		for i := 0; i < g.TEs; i++ {
			if after == "" || g.teUpdated(i) >= after {
				out = append(out, int32(i))
			}
		}
	} else {
		for _, j := range g.byProgram[program] {
			if (after == "" || g.teUpdated(g.te(int(j))) >= after) && (status == "" || g.status(int(j)) == status) {
				out = append(out, j)
			}
		}
	}
	g.filtered[key] = out
	return out
}

// programMeta is the generated programs' metadata, with full access for u.
func (g *generated) programMeta() []any {
	attr := func(id string) map[string]any {
		return map[string]any{"trackedEntityAttribute": map[string]any{"id": id, "generated": false, "unique": false}}
	}
	var out []any
	for p := 0; p < g.Programs; p++ {
		var stages []any
		for s := 0; s < 3; s++ {
			stages = append(stages, map[string]any{"id": GenStage(p, s), "sortOrder": s + 1, "repeatable": true, "access": dataPerm(true, true)})
		}
		out = append(out, map[string]any{"id": GenProgram(p), "name": fmt.Sprintf("Generated program %d", p+1),
			"access": dataPerm(true, true),
			"trackedEntityType": map[string]any{"id": TET, "access": dataPerm(true, true),
				"trackedEntityTypeAttributes": []any{attr(AttrFirstName), attr(AttrLastName)}},
			"programStages": stages, "programTrackedEntityAttributes": []any{}})
	}
	return out
}

// listTEs answers a facility pull: of one generated program (each tracked entity with
// its one enrollment in it and the events), or of the type (without enrollments).
// It reports false for queries it does not serve.
func (g *generated) listTEs(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	program, byType := -1, q.Get("program") == "" && q.Get("trackedEntityType") == TET
	for p := 0; p < g.Programs && !byType; p++ {
		if q.Get("program") == GenProgram(p) {
			program = p
		}
	}
	if program < 0 && !byType {
		return false
	}
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	page, size = max(page, 1), max(size, 1)
	var list []int32 // nil for the type without a filter: every person, in order
	total := g.TEs
	if !byType || q.Get("updatedAfter") != "" {
		list = g.listing(program, q.Get("updatedAfter"), q.Get("enrollmentStatus"))
		total = len(list)
	}
	// Every generated object has the same update time, so "updated after" the newest
	// one the hub has seen means nothing changed (DHIS2 returns the few at that exact
	// time again; here they would be everything).
	ou := q.Get("orgUnits") // Android's download says orgUnit (with DESCENDANTS)
	if ou == "" {
		ou = q.Get("orgUnit")
	}
	if ou != Facility || (q.Get("updatedAfter") != "" && q.Get("updatedAfter") >= g.updatedAt) {
		total = 0
	}
	idsOnly := strings.HasPrefix(q.Get("fields"), "trackedEntity,updatedAt") || q.Get("fields") == "trackedEntity"
	render := func(te, j int) tracker.M {
		t := g.renderTE(te)
		if j >= 0 {
			t["enrollments"] = []any{g.renderEnrollment(j)}
		}
		if q.Get("fields") == "trackedEntity" { // the ownership check: identifiers only
			return tracker.M{"trackedEntity": t["trackedEntity"]}
		}
		if idsOnly {
			t = idsOf(t)
		}
		return t
	}
	items := []any{}
	if v := q.Get("trackedEntities"); v != "" {
		// A fetch of named tracked entities: each with its enrollment in the program.
		for _, uid := range strings.Split(v, ",") {
			var i int
			if _, err := fmt.Sscanf(uid, "Te%09d", &i); err != nil || i < 0 || i >= g.TEs {
				continue
			}
			if byType {
				items = append(items, render(i, -1))
				continue
			}
			for r := 0; i+r*g.TEs < g.Enrollments; r++ {
				if j := i + r*g.TEs; g.program(j) == program {
					items = append(items, render(i, j))
				}
			}
		}
		total = 0
	}
	for n := (page - 1) * size; n < total && n < page*size; n++ {
		if byType {
			i := n
			if list != nil {
				i = int(list[n])
			}
			items = append(items, render(i, -1))
			continue
		}
		j := int(list[n])
		items = append(items, render(g.te(j), j))
	}
	pager := map[string]any{"page": page, "pageSize": size}
	if page*size < total {
		next := *r.URL
		qq := next.Query()
		qq.Set("page", strconv.Itoa(page+1))
		next.RawQuery = qq.Encode()
		pager["nextPage"] = "http://" + r.Host + next.String()
	}
	writeJSON(w, 200, map[string]any{"pager": pager, "trackedEntities": items})
	return true
}

// has reports whether uid is a generated enrollment (nil-safe).
func (g *generated) has(uid string) bool {
	if g == nil {
		return false
	}
	var j int
	if _, err := fmt.Sscanf(uid, "En%09d", &j); err != nil || len(uid) != 11 {
		return false
	}
	return j >= 0 && j < g.Enrollments
}

// idsOf keeps only identifiers and update times, as central answers the ID listing.
func idsOf(m tracker.M) tracker.M {
	out := tracker.M{}
	for _, k := range []string{"trackedEntity", "enrollment", "event", "relationship", "updatedAt"} {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	for _, k := range []string{"enrollments", "events", "relationships"} {
		if l, ok := m[k].([]any); ok {
			var sub []any
			for _, x := range l {
				if xm, ok := x.(tracker.M); ok {
					sub = append(sub, idsOf(xm))
				}
			}
			out[k] = orEmpty(sub)
		}
	}
	return out
}

// getTE answers GET /api/tracker/trackedEntities/{uid} for a generated person, with
// its enrollments in the requested program, as the web app's dashboard reads it
// online. It reports false for any other uid.
func (g *generated) getTE(w http.ResponseWriter, r *http.Request, uid string) bool {
	if g == nil {
		return false
	}
	var i int
	if _, err := fmt.Sscanf(uid, "Te%09d", &i); err != nil || len(uid) != 11 || i < 0 || i >= g.TEs {
		return false
	}
	t := g.renderTE(i)
	for p := 0; p < g.Programs; p++ {
		if r.URL.Query().Get("program") != GenProgram(p) {
			continue
		}
		var enrs []any
		for n := 0; i+n*g.TEs < g.Enrollments; n++ {
			if j := i + n*g.TEs; g.program(j) == p {
				enrs = append(enrs, g.renderEnrollment(j))
			}
		}
		t["enrollments"] = orEmpty(enrs)
	}
	writeJSON(w, 200, t)
	return true
}

// enrollmentEvents answers GET /api/tracker/events?enrollments={uid} for one generated
// enrollment: its generated events. Events pushed to it later are not among them; a
// load test reads them back from the hub.
func (g *generated) enrollmentEvents(w http.ResponseWriter, r *http.Request) {
	var j int
	fmt.Sscanf(r.URL.Query().Get("enrollments"), "En%09d", &j)
	writeJSON(w, 200, map[string]any{"events": g.renderEnrollment(j)["events"]})
}
