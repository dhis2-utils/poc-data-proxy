package e2e

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
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

// I8c (adversarial, 29 October): a 60 MB tracker payload took the hub from 174 to 605
// MB, six at once to 5.7 GB. A tracker import costs 7 to 15 times its size in memory
// while it is decoded and answered, so /api/tracker takes at most 16 MB, online and
// offline: some 2 000 people with their enrollments and events. Android posts all it
// holds unsynced in one request, so a lower limit could leave a tablet stuck.
func TestTrackerBodyLimit(t *testing.T) {
	e := newEnv(t)
	e.setup()
	events := func(mb int) []byte {
		var b bytes.Buffer
		b.WriteString(`{"events":[`)
		for i := 0; b.Len() < mb<<20; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"event":"Ev%09d","program":"NoSuchProg1","programStage":"NoSuchStag1","orgUnit":"%s",`+
				`"occurredAt":"2026-10-01","status":"ACTIVE"}`, i, fc.Facility)
		}
		b.WriteString(`]}`)
		return b.Bytes()
	}
	post := func(body []byte) int {
		req, _ := http.NewRequest("POST", e.srv.URL+postSync, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("nurse_a", fc.Password)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	e.android("nurse_a").do("GET", "/api/me", nil)
	big, fits := events(17), events(6)
	for _, down := range []bool{false, true} {
		if down {
			e.offline()
		}
		if st := post(big); st != http.StatusRequestEntityTooLarge {
			t.Errorf("offline=%v: a 17 MB tracker payload: %d, want 413", down, st)
		}
		if st := post(fits); st == http.StatusRequestEntityTooLarge {
			t.Errorf("offline=%v: a 6 MB tracker payload (a long time offline on a tablet) refused as too large", down)
		}
	}
}
