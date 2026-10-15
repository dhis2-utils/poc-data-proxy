package live

import (
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// aggregateEnv starts a hub set up with the nurse and clerk logged in, or skips when
// this setup has no aggregate data sets.
func aggregateEnv(t *testing.T) (*tk.Env, *tk.Aggregate, *tk.Client, *tk.Client) {
	t.Helper()
	e := tk.Start(t)
	if e.M.Aggregate == nil {
		t.Skip("this setup has no aggregate data sets")
	}
	e.Setup()
	nurse, clerk := e.Web(tk.RoleNurse), e.Web(tk.RoleClerk)
	nurse.Login()
	clerk.Login()
	e.Sync()
	return e, e.M.Aggregate, nurse, clerk
}

// The access refresh reads each user's dataEntry/metadata: what central lets them
// enter, kept for offline use.
func TestAggregateAccessRefreshed(t *testing.T) {
	e, ag, nurse, _ := aggregateEnv(t)
	h := e.App.Hub
	u, _ := h.Store.UserByName(nurse.User)
	acc := h.AggAccess(u.ID)
	if acc == nil || acc.DataSets[ag.DataSet] == nil || acc.DataSets[ag.ValidDataSet] == nil {
		t.Fatalf("nurse's aggregate access: %+v", acc)
	}
	e.Offline()
	// The app's own metadata request is answered offline from what the refresh fetched.
	nurse.JSON("GET", "/api/"+h.APIMinor()+"/dataEntry/metadata", nil, 200)
}
