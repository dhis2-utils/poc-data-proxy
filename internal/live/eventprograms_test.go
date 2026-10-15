package live

import (
	"context"
	"encoding/json"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
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

// The catalog holds the facility's event programs and the option combinations of
// attribute category combinations.
func TestEventProgramsInCatalog(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Setup()
	cat := e.App.Hub.Catalog()
	ev, donor := cat.Programs[fc.EventProgram], cat.Programs[fc.EventDonorProgram]
	if ev == nil || !ev.Event || ev.TET != "" || len(ev.Stages) != 1 || ev.AttributeCombo {
		t.Fatalf("event program: %+v", ev)
	}
	if donor == nil || !donor.AttributeCombo || donor.CategoryCombo != fc.AggDonorCC {
		t.Fatalf("donor event program: %+v", donor)
	}
	if opts := cat.AOCs[fc.AggDonorAOCA]; len(opts) != 1 || opts[0] != fc.AggDonorOptA {
		t.Fatalf("option combinations: %+v", cat.AOCs)
	}
	if tb := cat.Programs[fc.Program]; tb == nil || tb.Event {
		t.Fatalf("tracker program: %+v", tb)
	}
}
