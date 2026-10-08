package testkit

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Central talks to DHIS2 directly, not through the hub: as the superuser to
// provision and inspect, or as a role user for the differential oracle.
type Central struct {
	Base     string
	User     string
	Password string
	HTTP     *http.Client
}

func NewCentral(base, user, password string) *Central {
	return &Central{Base: strings.TrimRight(base, "/"), User: user, Password: password,
		HTTP: &http.Client{Timeout: 10 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// As returns a client for another user on the same server.
func (c *Central) As(user, password string) *Central {
	n := *c
	n.User, n.Password = user, password
	return &n
}

// Do sends a request and returns status and body. body may be nil, []byte or any
// JSON-encodable value.
func (c *Central) Do(method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, err := json.Marshal(b)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequest(method, c.Base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(c.User, c.Password)
	req.Header.Set("Accept", "application/json")
	if rd != nil {
		ct := "application/json"
		if method == http.MethodPatch {
			ct = "application/json-patch+json"
		}
		req.Header.Set("Content-Type", ct)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, err
}

// JSON sends a request, fails on a non-2xx answer, and decodes into out if non-nil.
func (c *Central) JSON(method, path string, body, out any) error {
	st, b, err := c.Do(method, path, body)
	if err != nil {
		return err
	}
	if st/100 != 2 {
		return fmt.Errorf("%s %s: HTTP %d: %.1000s", method, path, st, b)
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

const uidChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// UID returns a random DHIS2 UID. Live tests create objects with fresh UIDs, so
// runs on the same instance do not collide.
func UID() string {
	b := make([]byte, 11)
	rand.Read(b)
	out := make([]byte, 11)
	out[0] = uidChars[int(b[0])%52]
	for i := 1; i < 11; i++ {
		out[i] = uidChars[int(b[i])%62]
	}
	return string(out)
}
