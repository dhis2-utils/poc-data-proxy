package e2e

import (
	"io"
	"net/url"
	"strings"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// A set-up form submitted twice runs set-up once: the second submit, while the first
// is still completing, is told so (review R-38).
func TestSetupSubmittedTwice(t *testing.T) {
	e := newEnv(t)
	started, release := make(chan struct{}), make(chan struct{})
	e.central.OnTokenCreate = func(string) {
		select {
		case <-started:
		default:
			close(started)
			<-release
		}
	}
	submit := func() string {
		form := url.Values{"step": {"login"}, "central_url": {e.cSrv.URL}, "username": {"hub_admin"}, "password": {fc.Password}}
		resp, err := e.web("hub_admin").postForm("/hub/setup", form)
		if err != nil {
			t.Error(err)
			return ""
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	first := make(chan string)
	go func() { first <- submit() }()
	<-started
	if b := submit(); !strings.Contains(b, "set-up already running") {
		t.Errorf("second submit: %.400s", b)
	}
	close(release)
	if b := <-first; !strings.Contains(b, "This hub now serves") {
		t.Fatalf("first submit: %.400s", b)
	}
}
