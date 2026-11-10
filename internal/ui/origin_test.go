package ui

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

// R-35: with TLS on, only the https origin of the hub's own name is the hub's.
func TestSameOriginScheme(t *testing.T) {
	for _, c := range []struct {
		tls    bool
		origin string
		want   bool
	}{
		{true, "https://dhis2.local", true},
		{true, "http://dhis2.local", false},
		{true, "https://evil.example", false},
		{false, "http://dhis2.local", true},
		{false, "https://dhis2.local", false},
	} {
		r := httptest.NewRequest("POST", "/hub/ui/x", nil)
		r.Host = "dhis2.local"
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		r.Header.Set("Origin", c.origin)
		if got := sameOrigin(r); got != c.want {
			t.Errorf("tls %v, Origin %s: %v, want %v", c.tls, c.origin, got, c.want)
		}
	}
}
