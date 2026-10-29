package e2e

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

const portalPage = `<!DOCTYPE html><html><head><title>Hotspot login</title></head><body>Log in to the hotspot</body></html>`

// D7 (adversarial, 29 October): a captive portal in front of the internet answers
// every request, before the hub's next ping notices. Its 200 page replaced a cached
// read, which was then served offline, and an aggregate value it swallowed was
// answered 200 and never sent. An /api/ answer that is HTML where the client asked
// for JSON, or a redirect to another host, is not central's: the hub takes central as
// unreachable, serves the request itself and keeps nothing of the answer.
func TestPortalAnswersAreNotCentrals(t *testing.T) {
	e := newEnv(t)
	var portal atomic.Value // "", "page" or "redirect"
	portal.Store("")
	e.cSrv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch portal.Load() {
		case "page":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(portalPage))
		case "redirect":
			http.Redirect(w, r, "http://hotspot.portal.example/login?dst=central", http.StatusFound)
		default:
			e.central.ServeHTTP(w, r)
		}
	})
	e.setup()
	nurse := e.web("nurse_a")
	nurse.login()
	e.sync()
	read := "/api/programs/" + fc.Program + "?fields=id,name"
	st, good := nurse.do("GET", read, nil)
	if st != 200 {
		t.Fatalf("online read: %d %s", st, good)
	}
	pe := time.Now().Format("200601")
	for i, kind := range []string{"page", "redirect"} {
		e.online()
		portal.Store(kind)
		st, b := nurse.do("GET", read, nil)
		if strings.Contains(string(b), "Hotspot") || st == http.StatusFound {
			t.Errorf("portal %s: the read is answered with the portal's answer: %d %.80s", kind, st, b)
		}
		if e.app.Hub.Online() {
			t.Errorf("portal %s: the hub still takes the portal for central", kind)
		}
		v := []string{"71", "72"}[i]
		q := url.Values{"de": {fc.AggDeNum}, "co": {fc.AggCOC}, "ds": {fc.AggDataSet}, "ou": {fc.Facility}, "pe": {pe}, "value": {v}}
		portal.Store("")
		e.online() // the hub has not noticed yet: the value goes to the portal first
		portal.Store(kind)
		st, b = nurse.do("POST", "/api/dataValues?"+q.Encode(), nil)
		if st/100 != 2 {
			t.Errorf("portal %s: the value %s: %d %.80s", kind, v, st, b)
		}
		portal.Store("")
		e.offline()
		if st, b := nurse.do("GET", read, nil); st != 200 || string(b) != string(good) {
			t.Errorf("portal %s, then offline: the read is %d %.80s, want central's answer", kind, st, b)
		}
		e.online()
		e.sync()
		if got, _, _ := e.central.AggValue(pe, fc.AggDeNum, fc.AggCOC, fc.AggCOC); got != v {
			t.Errorf("portal %s: the value %s answered %d is not on central after the next sync (central has %q)", kind, v, st, got)
		}
	}
}
