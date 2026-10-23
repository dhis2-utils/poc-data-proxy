package fakecentral

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A generated person and an enrollment's events can be read one at a time, as the
// web app's dashboard does while the hub is online (hubload -flap).
func TestGeneratedSingleReads(t *testing.T) {
	c := New()
	c.Generate(Scale{TEs: 20, Enrollments: 30, Events: 60, Programs: 2, Users: 1, Years: 1})
	srv := httptest.NewServer(c)
	defer srv.Close()
	get := func(path string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.SetBasicAuth(GenUser(0), Password)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out map[string]any
		json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}
	g := c.gen
	j := 3 // an enrollment, its person and program
	te, prog := genTE(g.te(j)), GenProgram(g.program(j))

	st, out := get("/api/tracker/trackedEntities/" + te + "?program=" + prog + "&fields=*")
	if st != 200 || out["trackedEntity"] != te {
		t.Fatalf("person: %d %v", st, out)
	}
	enrs, _ := out["enrollments"].([]any)
	if len(enrs) != 1 || enrs[0].(map[string]any)["enrollment"] != genEnrollment(j) {
		t.Fatalf("person's enrollments in %s: %v", prog, enrs)
	}

	st, out = get("/api/tracker/events?enrollments=" + genEnrollment(j) + "&fields=*&paging=false")
	evs, _ := out["events"].([]any)
	if st != 200 || len(evs) != g.eventsOf(j) {
		t.Fatalf("events of %s: %d, %d events, want %d: %v", genEnrollment(j), st, len(evs), g.eventsOf(j), out)
	}

	// Android's download names the facility as orgUnit (with DESCENDANTS), the web app
	// and the hub as orgUnits.
	st, out = get("/api/tracker/trackedEntities?program=" + prog + "&orgUnit=" + Facility + "&orgUnitMode=DESCENDANTS&page=1&pageSize=50")
	if tes, _ := out["trackedEntities"].([]any); st != 200 || len(tes) == 0 {
		t.Fatalf("Android's download online: %d %v", st, out)
	}

	if st, _ := get("/api/tracker/trackedEntities/" + genTE(20)); st != 404 {
		t.Fatalf("a person past the generated ones: %d", st)
	}
}
