package fakecentral

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// Event programs (WITHOUT_REGISTRATION) at the facility.
const (
	EventProgram      = "FakePrgEvt1" // default category combination
	EventStage        = "FakeStEvt01"
	EventDonorProgram = "FakePrgEvD1" // category combination AggDonorCC
	EventDonorStage   = "FakeStEvD01"
)

func eventProgramMeta(u *User) []any {
	_, writes := aggUsers[u.Username]
	stage := func(id string) []any {
		return []any{map[string]any{"id": id, "sortOrder": 1, "repeatable": true, "access": dataPerm(u.ProgramRead, writes)}}
	}
	return []any{
		map[string]any{"id": EventProgram, "name": "Fake event program", "programType": "WITHOUT_REGISTRATION",
			"categoryCombo": map[string]any{"id": AggCC, "isDefault": true}, "access": dataPerm(u.ProgramRead, writes),
			"programStages": stage(EventStage)},
		map[string]any{"id": EventDonorProgram, "name": "Fake donor events", "programType": "WITHOUT_REGISTRATION",
			"categoryCombo": map[string]any{"id": AggDonorCC, "isDefault": false}, "access": dataPerm(u.ProgramRead, writes),
			"programStages": stage(EventDonorStage)},
	}
}

func isEventProgram(p string) bool { return p == EventProgram || p == EventDonorProgram }

// donorOption is the category option of a donor attribute option combination.
func donorOption(aoc string) string {
	return map[string]string{AggDonorAOCA: AggDonorOptA, AggDonorAOCB: AggDonorOptB}[aoc]
}

// mayUseEvent: the event's attribute option (donor program) is one of the user's.
func mayUseEvent(u *User, ev tracker.M) bool {
	if tracker.Str(ev, "program") != EventDonorProgram {
		return true
	}
	return slices.Contains(aggUsers[u.Username], donorOption(tracker.Str(ev, "attributeOptionCombo")))
}

// SeedProgramEvent adds an event of an event program at the facility.
func (c *Central) SeedProgramEvent(uid, program, stage, occurredAt, aoc string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := c.now()
	c.put("event", uid, tracker.M{"event": uid, "program": program, "programStage": stage, "orgUnit": Facility,
		"status": "ACTIVE", "occurredAt": occurredAt, "attributeOptionCombo": aoc, "createdAt": ts, "updatedAt": ts,
		"dataValues": []any{}})
}

// DeleteProgramEvent marks an event deleted, as central's soft delete does.
func (c *Central) DeleteProgramEvent(uid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ev := c.objects[uid]; ev != nil {
		ev["deleted"], ev["updatedAt"] = true, c.now()
	}
}

// Hide makes central answer 404 for an object, as it does for one the user may not
// see as well as for one it deleted, without a tombstone for the pull.
func (c *Central) Hide(uid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.objects, uid)
	delete(c.kinds, uid)
}

// listProgramEvents answers /api/tracker/events for event programs.
func (c *Central) listProgramEvents(w http.ResponseWriter, r *http.Request, u *User) {
	q := r.URL.Query()
	program := q.Get("program")
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	page, size = max(page, 1), max(size, 50)
	c.mu.Lock()
	var out []any
	for _, uid := range c.order {
		ev := c.objects[uid]
		if c.kinds[uid] != "event" || tracker.Str(ev, "program") != program || tracker.Str(ev, "orgUnit") != Facility {
			continue
		}
		if ev["deleted"] == true && q.Get("includeDeleted") != "true" {
			continue
		}
		if a := q.Get("updatedAfter"); a != "" && tracker.Str(ev, "updatedAt") < a {
			continue
		}
		if a := q.Get("occurredAfter"); a != "" && tracker.Str(ev, "occurredAt") < a {
			continue
		}
		if !u.ProgramRead || !mayUseEvent(u, ev) {
			continue
		}
		out = append(out, ev)
	}
	c.mu.Unlock()
	start := (page - 1) * size
	pageItems := []any{}
	if start < len(out) {
		pageItems = out[start:min(start+size, len(out))]
	}
	pager := map[string]any{"page": page, "pageSize": size}
	if start+size < len(out) {
		pager["nextPage"] = "next"
	}
	writeJSON(w, 200, map[string]any{"pager": pager, "events": pageItems})
}

// categoryCombos answers the access refresh's question: which options of these
// combinations the user may write.
func (c *Central) categoryCombos(w http.ResponseWriter, r *http.Request, u *User) {
	if !strings.Contains(r.URL.Query().Get("filter"), AggDonorCC) {
		writeJSON(w, 200, map[string]any{"categoryCombos": []any{}})
		return
	}
	opt := func(id string) map[string]any {
		mine := slices.Contains(aggUsers[u.Username], id) // what the user may read and write (mayUseEvent)
		return map[string]any{"id": id, "access": map[string]any{"data": map[string]any{"read": mine, "write": mine}}}
	}
	writeJSON(w, 200, map[string]any{"categoryCombos": []any{map[string]any{"id": AggDonorCC,
		"categories": []any{map[string]any{"categoryOptions": []any{opt(AggDonorOptA), opt(AggDonorOptB)}}}}}})
}

func genEventProgram(p int) string { return fmt.Sprintf("GenEvP%05d", p) }
func genEventStage(p int) string   { return fmt.Sprintf("GenEvS%05d", p) }

func (g *generated) eventProgramMeta() []any {
	var out []any
	for p := 0; p < g.EventPrograms; p++ {
		out = append(out, map[string]any{"id": genEventProgram(p), "name": fmt.Sprintf("Generated event program %d", p+1),
			"programType": "WITHOUT_REGISTRATION", "categoryCombo": map[string]any{"id": AggCC, "isDefault": true},
			"access": dataPerm(true, true), "programStages": []any{map[string]any{"id": genEventStage(p), "sortOrder": 1,
				"repeatable": true, "access": dataPerm(true, true)}}})
	}
	return out
}

// eventDate is generated event i's date: the newest today, the oldest 36 months back.
func (g *generated) eventDate(i int) time.Time {
	span := 36 * 30 * 24 * time.Hour
	return time.Now().Add(-time.Duration(int64(span) / int64(max(g.ProgramEvents, 1)) * int64(i)))
}

// listEvents answers a generated event program's listing, rendering each page as asked.
func (g *generated) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	program := q.Get("program")
	var p int
	fmt.Sscanf(program, "GenEvP%05d", &p)
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	page, size = max(page, 1), max(size, 50)
	n := g.ProgramEvents
	if a := q.Get("updatedAfter"); a != "" && a >= g.updatedAt { // as the generated tracked entities
		n = 0
	}
	if a := q.Get("occurredAfter"); a != "" {
		cut, _ := time.Parse(time.DateOnly, a)
		for n > 0 && g.eventDate(n-1).Before(cut) { // dates fall with i
			n--
		}
	}
	events := []any{}
	for i := (page - 1) * size; i < min(page*size, n); i++ {
		events = append(events, tracker.M{"event": fmt.Sprintf("Gv%09d", p*g.ProgramEvents+i), "program": program,
			"programStage": genEventStage(p), "orgUnit": Facility, "status": "COMPLETED",
			"occurredAt": g.eventDate(i).Format("2006-01-02T00:00:00.000"), "attributeOptionCombo": DefaultAOC,
			"createdAt": g.updatedAt, "updatedAt": g.updatedAt, "deleted": false,
			"dataValues": []any{tracker.M{"dataElement": genDE(0), "value": strconv.Itoa(i % 97)},
				tracker.M{"dataElement": genDE(1), "value": "generated"}}})
	}
	writeJSON(w, 200, map[string]any{"pager": map[string]any{"page": page, "pageSize": size}, "events": events})
}

// SoftDelete marks a tracker object deleted, as central's soft delete does: a tracked
// entity with its enrollments and events, an enrollment with its events.
func (c *Central) SoftDelete(uid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := c.now()
	mark := func(u string) {
		if o := c.objects[u]; o != nil {
			o["deleted"], o["updatedAt"] = true, ts
		}
	}
	mark(uid)
	for _, u := range c.order {
		o := c.objects[u]
		if o["trackedEntity"] == uid || o["enrollment"] == uid {
			mark(u)
		}
	}
}
