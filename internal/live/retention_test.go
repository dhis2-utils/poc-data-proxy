package live

import (
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
	tk "github.com/dhis2/facility-hub/internal/testkit"
)

// The catalog records which programs allow one enrollment ever.
func TestCatalogOnlyEnrollOnce(t *testing.T) {
	e := tk.Start(t)
	e.NeedFake()
	e.Fake.OnlyEnrollOnce = true
	e.Setup()
	if pm := e.App.Hub.Catalog().Programs[fc.Program]; pm == nil || !pm.OnlyEnrollOnce {
		t.Fatalf("onlyEnrollOnce not in the catalog: %+v", pm)
	}
}
