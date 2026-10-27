package e2e

import (
	"bytes"
	"net/http"
	"testing"
)

// A request without a login gets a small body limit: a login needs a few hundred
// bytes, and buffering up to MaxBody for anyone on the facility network could run a
// 1 GB Pi out of memory (docs/reviews/CRITICAL-REVIEW_2026-10-27_ML4.md, L21).
func TestAnonymousBodyLimit(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for _, path := range []string{"/api/dataStore/x/y", "/api/dataValues"} {
		resp, err := http.Post(e.srv.URL+path, "application/json", bytes.NewReader(make([]byte, 2<<20)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("anonymous 2 MiB body to %s: %d, want 413", path, resp.StatusCode)
		}
	}
	a := e.web("nurse_a")
	a.login() // a normal login still works
}
