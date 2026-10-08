package live

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// When one user's access changes on central, their offline view changes at the next
// sync, and nobody else's does (docs/PERFORMANCE.md, the ID-list pull).
func TestAccessChangeOfOneUser(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	a, b := e.AndroidUser("nurse_a"), e.AndroidUser("nurse_b")
	for _, c := range []*tk.Client{a, b} {
		if st, body := c.Do("GET", "/api/me?fields=id", nil); st != 200 {
			t.Fatalf("%s: %d %s", c.User, st, body)
		}
	}
	events := func(c *tk.Client) []string {
		t.Helper()
		st, body := c.Do("GET", "/api/tracker/events?enrollments=SeedEn00001&fields=event&paging=false", nil)
		if st != 200 {
			return nil
		}
		var r struct {
			Events []struct{ Event string } `json:"events"`
		}
		json.Unmarshal(body, &r)
		var out []string
		for _, ev := range r.Events {
			out = append(out, ev.Event)
		}
		slices.Sort(out)
		return out
	}
	check := func(step string, wantA, wantB []string) {
		t.Helper()
		e.Sync()
		e.Offline()
		defer e.Online()
		if got := events(a); !slices.Equal(got, wantA) {
			t.Errorf("%s: nurse_a sees %v offline, want %v", step, got, wantA)
		}
		if got := events(b); !slices.Equal(got, wantB) {
			t.Errorf("%s: nurse_b sees %v offline, want %v", step, got, wantB)
		}
	}
	both, first := []string{"SeedEv00001", "SeedEv00003"}, []string{"SeedEv00001"}
	check("start", both, first)

	nb := e.Fake.Users["nurse_b"]
	nb.StageRead = maps.Clone(nb.StageRead) // the fake shares the map between users
	nb.StageRead[fc.Stage3] = true
	check("nurse_b granted stage 3", both, both)

	e.Fake.Users["nurse_a"].ProgramRead = false
	check("nurse_a lost the program", nil, both)
}
