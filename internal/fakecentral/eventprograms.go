package fakecentral

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

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
		return map[string]any{"id": id, "access": map[string]any{"data": map[string]any{
			"read": true, "write": slices.Contains(aggUsers[u.Username], id)}}}
	}
	writeJSON(w, 200, map[string]any{"categoryCombos": []any{map[string]any{"id": AggDonorCC,
		"categories": []any{map[string]any{"categoryOptions": []any{opt(AggDonorOptA), opt(AggDonorOptB)}}}}}})
}
