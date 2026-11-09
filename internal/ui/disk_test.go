package ui

import (
	"strings"
	"testing"
)

// E7 (session B, 9 November): the hub's page warns under 2 GB free, the Facility Hub
// app's threshold, also offline, when the central flag cannot show.
func TestDiskWarning(t *testing.T) {
	for _, c := range []struct {
		free  uint64
		known bool
		want  string
	}{
		{25e9, true, ""},
		{2e9, true, ""},
		{1.5e9, true, "1.5 GB"},
		{0, true, "0.0 GB"}, // full: known, not "unknown"
		{0, false, ""},      // could not be read
	} {
		if got := diskText(c.free, c.known); got != c.want {
			t.Errorf("diskText(%d, %v) = %q, want %q", c.free, c.known, got, c.want)
		}
	}
	var b strings.Builder
	tmpl.ExecuteTemplate(&b, "admin.html", map[string]any{"DiskLow": diskText(1.5e9, true)}) // the rest of the page needs a hub
	if !strings.Contains(b.String(), "The hub's disk is nearly full: 1.5 GB free.") {
		t.Errorf("the page does not warn at 1.5 GB free:\n%.600s", b.String())
	}
}
