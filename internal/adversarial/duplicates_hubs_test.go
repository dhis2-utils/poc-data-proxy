package adversarial

// Section C of docs/ADVERSARIAL-TESTING.md: ownership, conflicts and duplicates across
// two hubs.

import (
	"net/url"
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// c5SiblingHub builds a second hub serving the sibling facility, with its own admin and
// nurse (new users scoped to the sibling), and returns it with the nurse's Android
// client. It mirrors wave3b's siblingHub (G8), and drops the second hub's registry and
// heartbeat entries at the end so its users can be deleted.
func c5SiblingHub(t *testing.T, m *tk.Manifest) (*tk.Env, *tk.Client) {
	t.Helper()
	sib := m.Sibling.ID
	e2 := tk.Start(t)
	admin, nurse := e2.NewUserLike(tk.RoleAdmin), e2.NewUserLike(tk.RoleNurse)
	e2.SetUserOrgUnits(admin, sib)
	e2.SetUserOrgUnits(nurse, sib)
	e2.SetupOnlyAs(admin.Username, e2.M.Password)
	dropHubEntries(t, e2.Central, e2.App.Hub.HubID())
	e2.Sync()
	if f := e2.App.Hub.Facility(); f != sib {
		t.Fatalf("the second hub serves %s, not the sibling %s", f, sib)
	}
	n := e2.AndroidUser(nurse.Username)
	if st, b := n.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("%s online: %d %s", nurse.Username, st, b)
	}
	return e2, n
}

// atSibling moves a person's tracked entity and enrollment to the sibling org unit, for
// a registration made on the sibling's hub.
func atSibling(pr *tk.Person, sib string) {
	te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
	te["orgUnit"] = sib
	for _, en := range te["enrollments"].([]any) {
		en.(map[string]any)["orgUnit"] = sib
	}
}

// C5: the same person is registered offline at two facilities, on two hubs — the same
// unique attribute value, different tracked entities. Both hubs reconnect and push.
// Must hold: both pushed, or one refused by central's uniqueness; which is recorded; no
// merge loss (the refused registration is kept on its hub, the accepted one is whole on
// central), and no duplicate of the same tracked entity (I1, I2).
func TestC5SamePersonTwoHubs(t *testing.T) {
	s := begin(t)
	if !s.Live() {
		t.Skip("needs a real DHIS2")
	}
	dropHubEntries(t, s.Central, s.App.Hub.HubID())

	// A program with a unique attribute (the national ID of the maternal program).
	var p tk.Program
	for _, x := range s.M.Programs {
		if x.UniqueAttr != "" && !x.Event && x.AccessLevel == "OPEN" {
			p = x
			break
		}
	}
	if p.ID == "" {
		t.Skip("no open program with a unique attribute")
	}
	sib := s.M.Sibling.ID

	nurseA := s.connect(s.M.Users[tk.RoleNurse].Username)
	e2, nurseB := c5SiblingHub(t, s.M)
	l2 := e2.Ledger() // Ledger() makes a new one each call: keep it

	// The same person, as two separate registrations carrying one unique value.
	shared := "T-DUP-" + tk.UID()
	prA := s.M.NewPerson(p, "ZqC5"+strings.ToLower(tk.UID()[:5]))
	prA.SetAttr(p.UniqueAttr, shared)
	nurseA.Complete(p, prA)
	prB := s.M.NewPerson(p, "ZqC5"+strings.ToLower(tk.UID()[:5]))
	atSibling(prB, sib)
	prB.SetAttr(p.UniqueAttr, shared)
	nurseB.Complete(p, prB)

	// Both hubs offline; each nurse registers their copy.
	s.Offline()
	e2.Offline()
	stA, bA := s.save(nurseA, "register at the facility", prA.Payload)
	stB, bB := nurseB.Do("POST", tk.SyncImport, prB.Payload)
	if stB == 200 && accepted(stB, bB) {
		l2.Confirmed(nurseB.User, prB.Payload)
	}
	t.Logf("offline: facility hub %d, sibling hub %d (both should accept locally)", stA, stB)
	if stA != 200 || stB != 200 {
		t.Fatalf("a hub refused a valid offline registration: facility %d %.150s, sibling %d %.150s", stA, bA, stB, bB)
	}

	// Both reconnect and push.
	s.noRepeats(s.reconnect(3))
	e2.Online()
	e2.Sync()
	e2.Sync()

	// What reached central: the tracked entities, and the count carrying the unique value.
	onA := s.CentralObject("trackedEntities", prA.TE) != nil
	onB := s.CentralObject("trackedEntities", prB.TE) != nil
	n := s.countUnique(p, shared)
	t.Logf("C5: facility registration on central=%v, sibling registration on central=%v; tracked entities with the unique value %q on central: %d", onA, onB, shared, n)

	// No merge loss on either hub's ledger: each save is on central or refused and kept.
	t.Logf("-- facility hub ledger --")
	s.expect(nil, tk.OnCentral, tk.RefusedKept)
	t.Logf("-- sibling hub ledger --")
	sibLines := expectLedger(t, l2, tk.OnCentral, tk.RefusedKept)

	switch {
	case onA && onB:
		// Both accepted: central's uniqueness is per-org-unit (the national ID is not
		// globally unique here). No third copy of either.
		if n != 2 {
			t.Errorf("I2: both registrations accepted but %d tracked entities carry the unique value (want 2): a duplicate", n)
		}
		t.Logf("C5 result: both pushed (uniqueness is not global here); no merge loss, no duplicate")
	case onA != onB:
		// One refused by uniqueness; the other whole on central, the refused one kept.
		if n != 1 {
			t.Errorf("I2: one registration refused but %d carry the unique value (want 1)", n)
		}
		kept, lines := "facility", s.l.Check()
		if onA {
			kept, lines = "sibling", sibLines
		}
		refused := 0
		for _, ln := range lines {
			if ln.Outcome == tk.RefusedKept {
				refused++
			}
		}
		if refused == 0 {
			t.Errorf("I1: the %s hub's registration is not on central and not kept as refused", kept)
		}
		t.Logf("C5 result: one refused by central's uniqueness (the %s hub's), kept on its hub; the other whole on central; the unique value exists once", kept)
	default:
		t.Errorf("I1: neither registration reached central (unique value count %d): a loss", n)
	}
}

// countUnique counts the tracked entities central holds that carry value for the
// program's unique attribute, as the superuser across the whole hierarchy.
func (s *scene) countUnique(p tk.Program, value string) int {
	s.t.Helper()
	q := url.Values{"program": {p.ID}, "orgUnitMode": {"ALL"}, "fields": {"trackedEntity"},
		"filter": {p.UniqueAttr + ":eq:" + value}, "paging": {"false"}}
	var res struct {
		TEs []struct{ TrackedEntity string } `json:"trackedEntities"`
	}
	if err := s.Central.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &res); err != nil {
		s.t.Logf("counting the unique value on central: %v", err)
		return -1
	}
	return len(res.TEs)
}

// expectLedger checks another hub's ledger (the sibling's) the way scene.expect checks
// the primary's: every save has an allowed outcome. It returns the lines.
func expectLedger(t *testing.T, l *tk.Ledger, allowed ...string) []tk.Line {
	t.Helper()
	lines := l.Check()
	for _, ln := range lines {
		t.Logf("sibling ledger: %s", ln)
		ok := false
		for _, a := range allowed {
			if ln.Outcome == a {
				ok = true
			}
		}
		if !ok {
			t.Errorf("sibling %s %s: %s", ln.Kind, ln.UID, ln.Outcome)
		}
	}
	return lines
}
