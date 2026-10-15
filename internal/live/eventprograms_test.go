package live

import (
	"context"
	"encoding/json"
	"testing"

	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The retention table is read from central's dataStore at the catalog refresh.
func TestRetentionTableRead(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.DataStore["facility-hub-config"] = map[string]json.RawMessage{
		"retention": json.RawMessage(`{"defaultMonths": 24, "programs": {"X": 6}}`)}
	e.Setup()
	if err := e.App.Hub.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := e.App.Hub.Retention()
	if r.Months("X") != 6 || r.Months("Y") != 24 {
		t.Fatalf("retention: %+v", r)
	}
}
