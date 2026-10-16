package fakecentral

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// SetStatus sets an enrollment's status (ACTIVE, COMPLETED, CANCELLED).
func (c *Central) SetStatus(enrollment, status string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if o := c.objects[enrollment]; o != nil {
		o["status"] = status
	}
}

// Backdate sets updatedAt on an object and what is under it: for a tracked entity,
// its enrollments and their events; for an enrollment, its events. Data on a real
// central cannot be aged; the fake's can, for retention tests.
func (c *Central) Backdate(uid, updatedAt string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range c.order {
		o := c.objects[u]
		if u == uid || o["trackedEntity"] == uid || o["enrollment"] == uid {
			o["updatedAt"] = updatedAt
		}
	}
}

// enrollOnce is central's check of a new enrollment against the person's others in
// the program (E1015, E1016), counting those accepted earlier in the payload.
func (c *Central) enrollOnce(it *tracker.Item, accepted []*tracker.Item) (code, msg string) {
	status := cmp.Or(tracker.Str(it.Data, "status"), "ACTIVE")
	te, program := it.TE, tracker.Str(it.Data, "program")
	if status == "CANCELLED" {
		return "", ""
	}
	var others []tracker.M
	for _, u := range c.order {
		if o := c.objects[u]; c.kinds[u] == "enrollment" && o["trackedEntity"] == te && o["program"] == program && o["deleted"] != true {
			others = append(others, o)
		}
	}
	for _, x := range accepted {
		if x.Kind == "enrollment" && x.TE == te && tracker.Str(x.Data, "program") == program {
			others = append(others, x.Data)
		}
	}
	for _, o := range others {
		st := cmp.Or(tracker.Str(o, "status"), "ACTIVE")
		if status == "ACTIVE" && st == "ACTIVE" {
			return "E1015", fmt.Sprintf("TrackedEntity: `%s`, already has an active Enrollment in Program `%s`.", te, program)
		}
		if program == Program && c.OnlyEnrollOnce && (st == "ACTIVE" || st == "COMPLETED") {
			return "E1016", fmt.Sprintf("TrackedEntity: `%s`, already has an active enrollment in Program: `%s`, and this program only allows enrolling one time.", te, program)
		}
	}
	return "", ""
}

// FetchCount is how often a tracked entity was fetched by its identifier.
func (c *Central) FetchCount(te string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetched[te]
}

// TEListQueries returns the query strings of the tracked entity listings and fetches
// central answered, in order.
func (c *Central) TEListQueries() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.teLists)
}
