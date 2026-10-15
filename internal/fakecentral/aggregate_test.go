package fakecentral

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFakeAggregate(t *testing.T) {
	c := New()
	srv := httptest.NewServer(c)
	defer srv.Close()
	do := func(user, method, path string, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.SetBasicAuth(user, Password)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
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
	pe := time.Now().Format("200601")
	if st, md := do("nurse_a", "GET", "/api/42/dataEntry/metadata", ""); st != 200 || len(md["dataSets"].([]any)) != 3 {
		t.Fatalf("metadata for nurse_a: %d %v", st, md)
	}
	if st, md := do("nurse_b", "GET", "/api/42/dataEntry/metadata", ""); st != 200 || len(md["dataSets"].([]any)) != 0 {
		t.Fatalf("nurse_b enters no aggregate data: %d %v", st, md)
	}
	q := url.Values{"de": {AggDeNum}, "co": {AggCOC}, "ds": {AggDataSet}, "ou": {Facility}, "pe": {pe}, "value": {"7"}}
	if st, _ := do("nurse_a", "POST", "/api/42/dataValues?"+q.Encode(), ""); st != 201 {
		t.Fatalf("POST value: %d", st)
	}
	if v, by, ok := c.AggValue(pe, AggDeNum, AggCOC, AggCOC); !ok || v != "7" || by != "nurse_a" {
		t.Fatalf("stored: %q %q %v", v, by, ok)
	}
	q.Set("value", "x")
	if st, out := do("nurse_a", "POST", "/api/dataValues?"+q.Encode(), ""); st != 409 || out["errorCode"] != "E2030" {
		t.Fatalf("bad number: %d %v", st, out)
	}
	_, form := do("clerk_c", "GET", "/api/42/dataEntry/dataValues?ds="+AggDataSet+"&pe="+pe+"&ou="+Facility, "")
	if form["lockStatus"] != "OPEN" || len(form["dataValues"].([]any)) != 1 {
		t.Fatalf("form: %v", form)
	}
	_, set := do("clerk_c", "GET", "/api/dataValueSets?dataSet="+AggDataSet+"&orgUnit="+Facility+"&startDate=2000-01-01&endDate=2100-01-01", "")
	if len(set["dataValues"].([]any)) != 1 {
		t.Fatalf("dataValueSets: %v", set)
	}
	if st, _ := do("nurse_a", "POST", "/api/dataEntry/dataSetCompletion",
		`{"dataSet":"`+AggDataSet+`","period":"`+pe+`","orgUnit":"`+Facility+`","completed":true}`); st != 409 {
		t.Fatalf("completion without the compulsory field: %d", st)
	}
	c.Approve(AggDataSet, pe)
	q.Set("value", "8")
	if st, out := do("nurse_a", "POST", "/api/dataValues?"+q.Encode(), ""); st != 409 || out["errorCode"] != "E2017" {
		t.Fatalf("approved: %d %v", st, out)
	}
}
