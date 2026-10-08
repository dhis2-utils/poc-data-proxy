package live

import (
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// A user whose capture org unit is above the facility (here its parent, "root") is
// facility staff, as on central: they may register and write offline (FINDINGS.md,
// test framework item 7).
func TestCaptureAboveFacility(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	all := map[string]bool{fc.Stage1: true, fc.Stage2: true, fc.Stage3: true}
	e.Fake.Users["district_e"] = &fc.User{UID: "HubUsrDistE", Username: "district_e", OrgUnits: []string{"root"},
		ProgramRead: true, ProgramWrite: true, StageRead: all, StageWrite: all}
	e.Setup()
	c := e.AndroidUser("district_e")
	if st, b := c.Do("GET", "/api/me?fields=id", nil); st != 200 {
		t.Fatalf("online login: %d %s", st, b)
	}
	e.Sync()
	e.Offline()
	p := e.M.Primary()
	pr := e.M.NewPerson(p, "Above", p.Stages[0])
	c.Complete(p, pr)
	if st, b := c.Do("POST", tk.SyncImport, pr.Payload); st != 200 {
		t.Fatalf("offline registration by a district-level user: %d %s", st, b)
	}
}
