package e2e

import (
	"context"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	"github.com/dhis2/facility-hub/internal/store"
)

// Notes added offline to an existing event or enrollment (PENDING.md, item 24): the hub
// takes Capture web's request, shows the note at once, and at the push makes the same
// request as the author. Central's record is not otherwise touched. A push whose answer
// was lost is sent again and taken as done (central: E1003); a note on a record deleted
// on central is kept as a refusal.
func TestNotesOffline(t *testing.T) {
	e := newEnv(t)
	e.central.Seed("NtTe0000001", "NtEn0000001", "Nora", "NAT-6001", map[string]string{"NtEv0000001": fc.Stage1, "NtEv0000002": fc.Stage1})
	e.setup()
	a, b := e.web("nurse_a"), e.web("nurse_b")
	a.login()
	b.login()
	e.sync()
	before := e.central.Object("NtEv0000001")["updatedAt"]

	e.offline()
	rep := a.json("POST", "/api/42/tracker/events/NtEv0000001/note", map[string]any{"value": "seen offline"}, 200)
	noteUID, _ := rep["note"].(string)
	if len(noteUID) != 11 || rep["value"] != "seen offline" {
		t.Fatalf("answer: %v", rep)
	}
	a.json("POST", "/api/tracker/enrollments/NtEn0000001/note", map[string]any{"note": "NtNote00001", "value": "enrollment, offline"}, 200)
	a.json("POST", "/api/tracker/events/NtEv0000002/note", map[string]any{"value": "to a record deleted on central"}, 200)
	if st, body := a.do("POST", "/api/tracker/enrollments/NtEn0000001/note", map[string]any{"note": "NtNote00001", "value": "again"}); st != 400 {
		t.Errorf("a note UID the record has: %d %s", st, body)
	}
	if st, _ := a.do("POST", "/api/tracker/events/NtEvMissing/note", map[string]any{"value": "x"}); st != 404 {
		t.Errorf("a record the hub does not have: %d", st)
	}
	ev := b.json("GET", "/api/tracker/events/NtEv0000001?fields=event,notes", nil, 200)
	if notes, _ := ev["notes"].([]any); len(notes) != 1 {
		t.Errorf("nurse_b offline sees notes %v", ev["notes"])
	}

	e.online()
	e.central.DeleteProgramEvent("NtEv0000002")
	e.central.LoseResponse = map[string]int{"POST /api/tracker/events/NtEv0000001/note": 1}
	ctx := context.Background()
	e.app.Worker.Push(ctx) // the first note's answer is lost
	if err := e.app.Worker.Push(ctx); err != nil {
		t.Fatal(err)
	}
	notes, _ := e.central.Object("NtEv0000001")["notes"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["note"] != noteUID || notes[0].(map[string]any)["storedBy"] != "nurse_a" {
		t.Errorf("central's notes on the event: %v", notes)
	}
	if after := e.central.Object("NtEv0000001")["updatedAt"]; after != before {
		t.Errorf("the event itself changed on central: updatedAt %v, was %v", after, before)
	}
	if en, _ := e.central.Object("NtEn0000001")["notes"].([]any); len(en) != 1 {
		t.Errorf("central's notes on the enrollment: %v", en)
	}
	if n := e.app.Hub.Store.CountOutbox(store.OutboxPending); n != 0 {
		t.Errorf("%d entries still waiting", n)
	}
	issues, _ := e.app.Hub.Store.UnresolvedIssues(10)
	if len(issues) != 1 || issues[0].Kind != store.OutboxNote || issues[0].ObjectUID != "NtEv0000002" {
		t.Errorf("the note on the deleted event is not kept as a refusal: %+v", issues)
	}
}
