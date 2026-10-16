package live

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	tk "github.com/dhis2/facility-hub/internal/testkit"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// TestListingBytesLive measures, live only, what the pull's ID listing of the
// primary program costs on the wire: compressed (the hub's HTTP client asks for gzip)
// and not, per listed tracked entity, enrollment and event, and the same listing
// without events. Numbers for docs/PERFORMANCE.md ("Weekly full pull").
func TestListingBytesLive(t *testing.T) {
	e := tk.Start(t)
	if e.Fake != nil {
		t.Skip("measures a real DHIS2")
	}
	e.Setup()
	api := e.App.Hub.API()
	c := &http.Client{Timeout: 10 * time.Minute}
	get := func(path, encoding string) (wire, raw []byte, enc string) {
		t.Helper()
		req, _ := http.NewRequest("GET", strings.TrimRight(e.M.CentralURL, "/")+path, nil)
		req.SetBasicAuth(e.M.AdminUser, e.M.AdminPassword)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Encoding", encoding) // set by hand: Go then leaves the body as sent
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		wire, _ = io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d %.300s", path, res.StatusCode, wire)
		}
		raw, enc = wire, res.Header.Get("Content-Encoding")
		if enc == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(wire))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ = io.ReadAll(zr)
		}
		return wire, raw, enc
	}
	for _, p := range e.M.Programs {
		full := api.ListPath(e.M.Facility.ID, p.ID, "", 1, 10000)
		u, _ := url.Parse(full)
		q := u.Query()
		q.Set("fields", "trackedEntity,updatedAt,enrollments[enrollment,updatedAt]")
		noEvents := u.Path + "?" + q.Encode()
		var tes, ens, evs, plainBytes int
		var lines []string
		for _, l := range []struct{ name, path string }{{"listing", full}, {"without events", noEvents}} {
			plain, _, _ := get(l.path, "identity")
			gz, raw, enc := get(l.path, "gzip")
			if l.name == "listing" {
				plainBytes = len(plain)
				var body map[string]any
				json.Unmarshal(raw, &body)
				for _, x := range asList(body["trackedEntities"]) {
					tes++
					for _, en := range asList(x.(map[string]any)["enrollments"]) {
						ens++
						evs += len(asList(en.(map[string]any)["events"]))
					}
				}
			}
			// What it would be on the wire from a server that compresses (nginx's gzip,
			// level 1 by default).
			var zb bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&zb, 1)
			zw.Write(plain)
			zw.Close()
			lines = append(lines, fmt.Sprintf("%s: %d bytes plain, %d on the wire when asking for gzip (Content-Encoding %q); gzip level 1 would send %d (%.1fx smaller)",
				l.name, len(plain), len(gz), enc, zb.Len(), float64(len(plain))/float64(max(zb.Len(), 1))))
		}
		per := ""
		if tes > 0 {
			per = fmt.Sprintf("; %.0f bytes plain per listed object", float64(plainBytes)/float64(tes+ens+evs))
		}
		t.Logf("%s (%s): %d people, %d enrollments, %d events%s\n  %s", p.Name, p.ID, tes, ens, evs, per, strings.Join(lines, "\n  "))
	}
}

// TestEventEditMovesUpdatedAtLive checks, live only, whether changing an event moves
// its enrollment's and its tracked entity's updatedAt, as central lists them: if it
// does, a listing without events still shows every change.
func TestEventEditMovesUpdatedAtLive(t *testing.T) {
	e := tk.Start(t)
	if e.Fake != nil {
		t.Skip("measures a real DHIS2")
	}
	program := e.M.Primary().ID
	list := func() map[string]any {
		t.Helper()
		st, b, err := e.Central.Do("GET", "/api/tracker/trackedEntities?program="+program+"&orgUnits="+e.M.Facility.ID+
			"&orgUnitMode=SELECTED&pageSize=50&fields=trackedEntity,updatedAt,enrollments[enrollment,updatedAt,events[event,updatedAt]]", nil)
		if err != nil || st != 200 {
			t.Fatalf("listing: %d %v %.300s", st, err, b)
		}
		var body map[string]any
		json.Unmarshal(b, &body)
		return body
	}
	var te, en, ev map[string]any
	for _, x := range asList(list()["trackedEntities"]) {
		for _, y := range asList(x.(map[string]any)["enrollments"]) {
			if evs := asList(y.(map[string]any)["events"]); len(evs) > 0 && te == nil {
				te, en, ev = x.(map[string]any), y.(map[string]any), evs[0].(map[string]any)
			}
		}
	}
	if te == nil {
		t.Skip("no event at the facility in the primary program")
	}
	evUID := tracker.Str(ev, "event")
	st, b, err := e.Central.Do("GET", "/api/tracker/events/"+evUID+"?fields=*", nil)
	if err != nil || st != 200 {
		t.Fatalf("read event: %d %v", st, err)
	}
	var full map[string]any
	json.Unmarshal(b, &full)
	time.Sleep(1100 * time.Millisecond) // update times are to the millisecond; be sure they would move
	st, b, err = e.Central.Do("POST", "/api/tracker?async=false&importStrategy=UPDATE", map[string]any{"events": []any{full}})
	if err != nil || st != 200 {
		t.Fatalf("re-posting the event unchanged: %d %v %.500s", st, err, b)
	}
	var te2, en2, ev2 map[string]any
	for _, x := range asList(list()["trackedEntities"]) {
		if x.(map[string]any)["trackedEntity"] != te["trackedEntity"] {
			continue
		}
		te2 = x.(map[string]any)
		for _, y := range asList(te2["enrollments"]) {
			if y.(map[string]any)["enrollment"] == en["enrollment"] {
				en2 = y.(map[string]any)
				for _, z := range asList(en2["events"]) {
					if z.(map[string]any)["event"] == evUID {
						ev2 = z.(map[string]any)
					}
				}
			}
		}
	}
	if te2 == nil || en2 == nil || ev2 == nil {
		t.Fatal("the person, enrollment or event left the listing")
	}
	moved := func(a, b map[string]any) string {
		if tracker.Str(a, "updatedAt") == tracker.Str(b, "updatedAt") {
			return "unchanged " + tracker.Str(a, "updatedAt")
		}
		return "moved " + tracker.Str(a, "updatedAt") + " -> " + tracker.Str(b, "updatedAt")
	}
	t.Logf("after an event update: event %s; enrollment %s; tracked entity %s", moved(ev, ev2), moved(en, en2), moved(te, te2))
}
