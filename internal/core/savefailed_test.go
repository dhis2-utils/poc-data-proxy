package core

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/store"
)

// E7 (session B, 9 November): a save that fails on a full disk says so, as a 503;
// other store failures stay a 500 with their own text.
func TestSaveFailedOnFullDisk(t *testing.T) {
	defer func(f func(string) (uint64, bool)) { diskSpace = f }(diskSpace)
	st, err := store.Open(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := New(config.Default(), "test", st)
	for _, c := range []struct {
		free   uint64
		known  bool
		status int
		text   string
	}{
		{0, true, 503, "the hub's disk is full"},
		{10 << 20, true, 503, "the hub's disk is full"},
		{5 << 30, true, 500, "could not store import: sqlite3: disk I/O error"},
		{0, false, 500, "could not store import: sqlite3: disk I/O error"},
	} {
		diskSpace = func(string) (uint64, bool) { return c.free, c.known }
		w := httptest.NewRecorder()
		h.SaveFailed(w, "could not store import: sqlite3: disk I/O error")
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.text) {
			t.Errorf("free %d (known %v): %d %s, want %d with %q", c.free, c.known, w.Code, w.Body, c.status, c.text)
		}
	}
}
