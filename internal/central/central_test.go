package central

import (
	"strings"
	"testing"
)

// Review M6: the error text names the status and code, never central's body.
func TestHTTPErrorOmitsBody(t *testing.T) {
	e := &HTTPError{Status: 409, Body: []byte(`{"errorCode":"E1064","message":"value 'Amina' already exists"}`)}
	if got := e.Error(); got != "central returned 409 (E1064)" {
		t.Fatal(got)
	}
	e = &HTTPError{Status: 400, Body: []byte(`Bad request: Amina`)}
	if got := e.Error(); strings.Contains(got, "Amina") {
		t.Fatal(got)
	}
}
