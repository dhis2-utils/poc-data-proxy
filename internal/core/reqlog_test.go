package core

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

// Requests with made-up paths must not grow the request log without bound (review
// R-06): past maxKeys distinct keys the rest count under "other", and long paths and
// parameter names are cut.
func TestRequestLogIsCapped(t *testing.T) {
	l := NewRequestLog()
	long := strings.Repeat("x", 10000)
	for i := range maxKeys + 500 {
		r := httptest.NewRequest("GET", fmt.Sprintf("/api/%d/%s?%s=1", i, long, long), nil)
		l.Record(r, "web", "central", 200)
		l.Miss(r)
		l.Unsupported(r.URL.Path, long)
	}
	counts, unsupp := l.Snapshot()
	for name, m := range map[string]map[string]int{"counts": counts, "unsupported": unsupp, "misses": l.misses} {
		if len(m) > maxKeys+1 {
			t.Errorf("%s: %d keys, want at most %d", name, len(m), maxKeys+1)
		}
		if m[otherKey] != 500 {
			t.Errorf("%s: other = %d, want 500", name, m[otherKey])
		}
		for k := range m {
			if len(k) > 3*(maxKeyPart+3)+20 {
				t.Fatalf("%s: key of %d bytes", name, len(k))
			}
		}
	}
	// A key already in the map is still counted on its own once the map is full.
	r := httptest.NewRequest("GET", fmt.Sprintf("/api/0/%s?%s=1", long, long), nil)
	l.Record(r, "web", "central", 200)
	if counts, _ := l.Snapshot(); counts[key(r, "web")] != 2 {
		t.Errorf("existing key: %d, want 2", counts[key(r, "web")])
	}
}
