package compat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// fixture loads testdata/2.42/<name> (recorded from a real 2.42.6 instance).
type fixture struct {
	Request struct {
		Query map[string]string `json:"query"`
	} `json:"request"`
	Response struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"response"`
}

func load(t *testing.T, name string) fixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "2.42", name))
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func roundTrip(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func TestListResponseMatchesFixtures(t *testing.T) {
	api := For("2.42.6")
	cases := []struct {
		fixture string
		res     *tracker.Result
	}{
		{"tracker_trackedEntities_list.json", &tracker.Result{Collection: "trackedEntities", Page: 1, PageSize: 50, Paging: true, Total: 63, Items: make([]tracker.M, 50)}},
		{"tracker_trackedEntities_totalPages.json", &tracker.Result{Collection: "trackedEntities", Page: 2, PageSize: 5, Paging: true, TotalPages: true, Total: 63, Items: make([]tracker.M, 5)}},
		{"tracker_trackedEntities_paging_false.json", &tracker.Result{Collection: "trackedEntities", Paging: false, Items: make([]tracker.M, 5)}},
	}
	for _, c := range cases {
		var want map[string]any
		json.Unmarshal(load(t, c.fixture).Response.Body, &want)
		got := roundTrip(api.ListResponse(c.res, "http://hub/api/tracker/trackedEntities?page=1"))
		if !slices.Equal(keys(got), keys(want)) {
			t.Errorf("%s: top-level keys %v, want %v", c.fixture, keys(got), keys(want))
		}
		wp, _ := want["pager"].(map[string]any)
		gp, _ := got["pager"].(map[string]any)
		if !slices.Equal(keys(gp), keys(wp)) {
			t.Errorf("%s: pager keys %v, want %v", c.fixture, keys(gp), keys(wp))
		}
		for _, k := range []string{"page", "pageSize", "total", "pageCount"} {
			if wp[k] != gp[k] {
				t.Errorf("%s: pager.%s = %v, want %v", c.fixture, k, gp[k], wp[k])
			}
		}
	}
}

func TestImportReportShape(t *testing.T) {
	api := For("2.42.6")
	var want map[string]any
	json.Unmarshal(load(t, "tracker_post_success_nested.json").Response.Body, &want)
	out := &tracker.ImportOutcome{
		Results:  []*tracker.ObjectResult{{Kind: "trackedEntity", UID: "HubFxTe0001", Created: true}, {Kind: "enrollment", UID: "HubFxEn0001", Created: true}, {Kind: "event", UID: "HubFxEv0001", Created: true}},
		Accepted: []*tracker.Obj{{}, {}, {}},
	}
	status, body := api.ImportReport(out)
	got := roundTrip(body)
	if status != 200 || !slices.Equal(keys(got), keys(want)) {
		t.Fatalf("status %d keys %v, want 200 %v", status, keys(got), keys(want))
	}
	if g, w := roundTrip(got["stats"]), roundTrip(want["stats"]); g["created"] != w["created"] || !slices.Equal(keys(g), keys(w)) {
		t.Fatalf("stats %v want %v", g, w)
	}
	gtm := got["bundleReport"].(map[string]any)["typeReportMap"].(map[string]any)
	wtm := want["bundleReport"].(map[string]any)["typeReportMap"].(map[string]any)
	if !slices.Equal(keys(gtm), keys(wtm)) {
		t.Fatalf("typeReportMap keys %v want %v", keys(gtm), keys(wtm))
	}
	// All rejected: 409, status ERROR, no bundleReport (as central with atomicMode=ALL).
	var wantErr map[string]any
	f := load(t, "tracker_post_access_denied.json")
	json.Unmarshal(f.Response.Body, &wantErr)
	rej := &tracker.ImportOutcome{Results: []*tracker.ObjectResult{{Kind: "event", UID: "X", Errors: []tracker.ErrorReport{{ErrorCode: "E1095"}}}},
		Errors: []tracker.ErrorReport{{ErrorCode: "E1095"}}}
	status, body = api.ImportReport(rej)
	got = roundTrip(body)
	if status != f.Response.Status || !slices.Equal(keys(got), keys(wantErr)) || got["status"] != "ERROR" {
		t.Fatalf("rejected: %d %v, want %d %v", status, keys(got), f.Response.Status, keys(wantErr))
	}
}

func TestTrackerResultFromFixtures(t *testing.T) {
	api := For("2.42.6")
	cases := map[string]struct {
		persisted []string
		rejected  []string
	}{
		"tracker_post_success_nested.json":  {persisted: []string{"HubFxTe0001", "HubFxEn0001", "HubFxEv0001"}},
		"tracker_post_partial_object.json":  {persisted: []string{"HubFxEv0004"}, rejected: []string{"HubFxEv0005"}},
		"tracker_post_unique_conflict.json": {rejected: []string{"HubFxTe0003", "HubFxEn0003"}},
		"tracker_post_access_denied.json":   {rejected: []string{"HubFxEv0010"}},
	}
	for name, c := range cases {
		f := load(t, name)
		persisted, errs, err := api.TrackerResult(f.Response.Status, f.Response.Body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, u := range c.persisted {
			if !persisted[u] {
				t.Errorf("%s: %s should be persisted", name, u)
			}
		}
		for _, u := range c.rejected {
			if persisted[u] || len(errs[u]) == 0 {
				t.Errorf("%s: %s should be rejected with an error", name, u)
			}
		}
	}
	// The nested event in the unique conflict was ignored without an error report:
	// it must come out as neither persisted nor errored.
	f := load(t, "tracker_post_unique_conflict.json")
	persisted, errs, _ := api.TrackerResult(f.Response.Status, f.Response.Body)
	if len(persisted) != 0 || len(errs) != 2 {
		t.Fatalf("unique conflict: persisted %v errs %v", persisted, errs)
	}
	// A report without bundleReport that is no refusal is of an unknown shape (N14).
	ok := load(t, "tracker_post_success_nested.json")
	reshaped := bytes.ReplaceAll(ok.Response.Body, []byte(`"bundleReport"`), []byte(`"importReport"`))
	if _, _, err := api.TrackerResult(ok.Response.Status, reshaped); err == nil {
		t.Fatal("a reshaped report read as an import report")
	}
}

func TestPATResponseFixture(t *testing.T) {
	uid, key, err := For("2.42.6").PATResponse(load(t, "apiToken_create.json").Response.Body)
	if err != nil || uid != "m7QsFKId7bC" || len(key) < 10 {
		t.Fatalf("uid %q key %q err %v", uid, key, err)
	}
}

func TestPullItemsFixture(t *testing.T) {
	f := load(t, "tracker_trackedEntities_list.json")
	items, more, err := For("2.42.6").PullItems(f.Response.Body, 1, 50)
	if err != nil || len(items) == 0 {
		t.Fatal(err)
	}
	_ = more // the fixture body is trimmed, so "more" depends on the trimmed length
	// An answer without the collection is not an empty listing (review R-34).
	for _, b := range []string{`{}`, `{"pager":{"page":1}}`} {
		if _, _, err := For("2.42.6").PullItems([]byte(b), 1, 50); err == nil {
			t.Errorf("%s read as a listing", b)
		}
	}
	if items, _, err := For("2.42.6").PullItems([]byte(`{"trackedEntities":[]}`), 1, 50); err != nil || len(items) != 0 {
		t.Errorf("empty listing: %v %v", items, err)
	}
}

func TestVersions(t *testing.T) {
	if For("2.42.6") == nil || For("2.43.0") == nil || For("2.41.3") != nil || For("2.44.0") != nil {
		t.Fatal("supported version mapping")
	}
}
