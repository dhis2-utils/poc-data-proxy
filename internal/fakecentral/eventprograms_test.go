package fakecentral

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFakeEventPrograms(t *testing.T) {
	c := New()
	srv := httptest.NewServer(c)
	defer srv.Close()
	do := func(user, method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.SetBasicAuth(user, Password)
		req.Header.Set("Content-Type", "application/json")
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
	ids := func(out map[string]any, key, field string) []string {
		var r []string
		list, _ := out[key].([]any)
		for _, x := range list {
			r = append(r, x.(map[string]any)[field].(string))
		}
		return r
	}
	q := func(v url.Values) string { return v.Encode() }
	_, ev := do("nurse_a", "GET", "/api/programs?"+q(url.Values{"filter": {"programType:eq:WITHOUT_REGISTRATION",
		"organisationUnits.id:eq:" + Facility}, "fields": {"id"}}), "")
	if got := ids(ev, "programs", "id"); len(got) != 2 || got[0] != EventProgram || got[1] != EventDonorProgram {
		t.Fatalf("event programs: %v", got)
	}
	_, tr := do("nurse_a", "GET", "/api/programs?"+q(url.Values{"filter": {"programType:eq:WITH_REGISTRATION"}}), "")
	if got := ids(tr, "programs", "id"); len(got) != 1 || got[0] != Program {
		t.Fatalf("tracker programs: %v", got)
	}
	_, byID := do("nurse_a", "GET", "/api/programs?"+q(url.Values{"filter": {"id:in:[" + Program + "," + EventProgram + "]"}}), "")
	if got := ids(byID, "programs", "id"); len(got) != 2 {
		t.Fatalf("programs by id: %v", got)
	}
	c.SeedProgramEvent("FakeEv00001", EventProgram, EventStage, "2026-10-01", DefaultAOC)
	c.SeedProgramEvent("FakeEvD0001", EventDonorProgram, EventDonorStage, "2026-10-01", AggDonorAOCA)
	evs := func(user, program string, extra url.Values) []string {
		v := url.Values{"program": {program}, "orgUnit": {Facility}}
		for k, x := range extra {
			v[k] = x
		}
		_, out := do(user, "GET", "/api/tracker/events?"+v.Encode(), "")
		return ids(out, "events", "event")
	}
	if n, m := len(evs("clerk_c", EventDonorProgram, nil)), len(evs("nurse_a", EventDonorProgram, nil)); n != 0 || m != 1 {
		t.Fatalf("donor events: clerk %d, nurse %d", n, m)
	}
	post := func(user, aoc string) (int, string) {
		body := `{"events":[{"event":"` + strings.Repeat("Q", 3) + aoc[:8] + `","program":"` + EventDonorProgram +
			`","programStage":"` + EventDonorStage + `","orgUnit":"` + Facility + `","attributeOptionCombo":"` + aoc +
			`","occurredAt":"2026-10-02","status":"ACTIVE"}]}`
		st, out := do(user, "POST", "/api/tracker?async=false", body)
		b, _ := json.Marshal(out)
		return st, string(b)
	}
	if st, b := post("clerk_c", AggDonorAOCA); st != 409 || !strings.Contains(b, "E1099") {
		t.Fatalf("clerk with option A: %d %s", st, b)
	}
	if st, b := post("clerk_c", AggDonorAOCB); st != 200 {
		t.Fatalf("clerk with option B: %d %s", st, b)
	}
	c.DeleteProgramEvent("FakeEv00001")
	if got := evs("nurse_a", EventProgram, url.Values{"includeDeleted": {"true"}}); len(got) != 1 {
		t.Fatalf("with deleted: %v", got)
	}
	if got := evs("nurse_a", EventProgram, nil); len(got) != 0 {
		t.Fatalf("without deleted: %v", got)
	}
	_, cc := do("clerk_c", "GET", "/api/categoryCombos?"+q(url.Values{"filter": {"id:in:[" + AggDonorCC + "]"}}), "")
	opts := cc["categoryCombos"].([]any)[0].(map[string]any)["categories"].([]any)[0].(map[string]any)["categoryOptions"].([]any)
	w := map[string]bool{}
	for _, o := range opts {
		m := o.(map[string]any)
		w[m["id"].(string)] = m["access"].(map[string]any)["data"].(map[string]any)["write"].(bool)
	}
	if w[AggDonorOptA] || !w[AggDonorOptB] {
		t.Fatalf("clerk's option write access: %v", w)
	}
}
