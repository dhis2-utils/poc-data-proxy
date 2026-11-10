package core

import (
	"path/filepath"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
)

// Settings from facility-hub-config are kept within the Facility Hub app's limits,
// whatever the entry holds (review R-07).
func TestConfigSettingsClamped(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dk, _ := hubcrypto.NewKey()
	if err := st.SetDK(dk); err != nil {
		t.Fatal(err)
	}
	h := New(config.Default(), "test", st)
	for key, v := range map[string]string{
		retentionKey:    `{"defaultMonths":-3,"programs":{"p":99999},"trackedEntityTypes":{"t":-1}}`,
		reservedPoolKey: `{"default":1000000000,"attributes":{"a":2000000000}}`,
		aggRetentionKey: `{"retentionPeriods":50000,"dataSets":{"d":1000000}}`,
	} {
		if err := st.SetState(key, []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	if r := h.Retention(); r.DefaultMonths != 0 || r.Programs["p"] != maxMonths || r.TrackedEntityTypes["t"] != 0 {
		t.Errorf("retention %+v", r)
	}
	if n, m := h.PoolTarget("a"), h.PoolTarget("other"); n != maxPool || m != maxPool {
		t.Errorf("pool targets %d %d", n, m)
	}
	if r := h.AggRetention(); r.Default != maxPeriods || r.For("d") != maxPeriods {
		t.Errorf("aggregate retention %+v", r)
	}
}
