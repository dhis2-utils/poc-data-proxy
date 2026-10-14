package live

import (
	"strings"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// TestImportFlagsOnPush writes offline with the flags eRegisters sends
// (skipPatternValidation, skipSideEffects): the hub must accept them, and send them
// along when it pushes the write to central, so central skips what the client asked.
func TestImportFlagsOnPush(t *testing.T) {
	e := tk.Start(t)
	m := e.M
	p := m.Primary()
	e.Setup()
	nurse := e.Web(tk.RoleNurse)
	nurse.Login()
	e.Sync()

	e.Offline()
	pr := m.NewPerson(p, "Zqflags"+strings.ToLower(tk.UID()[:5]), p.Stages[0])
	nurse.Complete(p, pr)
	path := "/api/tracker?async=false&atomicMode=OBJECT&importStrategy=CREATE_AND_UPDATE&skipPatternValidation=true&skipSideEffects=true"
	nurse.JSON("POST", path, pr.Payload, 200)
	e.Net.Calls()
	e.Online()
	e.Sync()
	var pushed []string
	for _, c := range e.Net.Calls() {
		if c.Method == "POST" && c.URL.Path == "/api/tracker" {
			pushed = append(pushed, c.URL.RawQuery)
		}
	}
	if len(pushed) == 0 {
		t.Fatal("nothing pushed to central")
	}
	for _, q := range pushed {
		if !strings.Contains(q, "skipPatternValidation=true") || !strings.Contains(q, "skipSideEffects=true") {
			t.Errorf("pushed without the client's flags: %s", q)
		}
	}
	if o := e.CentralObject("trackedEntities", pr.TE); o == nil {
		t.Error("the offline registration is not on central")
	}
}
