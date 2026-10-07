package core

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/dhis2/facility-hub/internal/tracker"
)

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// MustJSON marshals v and panics on error (only used for values the hub builds).
func MustJSON(v any) []byte { return mustJSON(v) }

// ReadBody reads a request body up to limit bytes.
func ReadBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(r.Body, limit))
}

// NewUID returns a random DHIS2-style UID.
func NewUID() string { return tracker.NewUID() }
