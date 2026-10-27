package core

import (
	"encoding/json"
	"errors"
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

// ReadBody reads a request body up to limit bytes.
// ErrTooLarge is returned by ReadBody for a body over the limit.
var ErrTooLarge = errors.New("request body too large")

// ReadBody buffers a request body of at most limit bytes. A larger body is an error,
// never silently cut.
func ReadBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		return nil, ErrTooLarge
	}
	return b, err
}

// MaxBody is the largest request body the hub buffers (tracker imports, proxied
// requests). Everything is held in RAM on a small device.
const MaxBody = 64 << 20

// MaxAnonymousBody is the body limit for a request without a login: one is a login
// or the start of one, a few hundred bytes. Anyone on the facility network can send
// these, so they must not each buffer MaxBody.
const MaxAnonymousBody = 64 << 10

// NewUID returns a random DHIS2-style UID.
func NewUID() string { return tracker.NewUID() }
