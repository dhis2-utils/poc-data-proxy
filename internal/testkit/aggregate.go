package testkit

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
)

// PostDataValue posts one cell as the Aggregate Data Entry app does: multipart form
// fields (de, co, ds, ou, pe, cc, cp, and value, comment or followUp).
func (c *Client) PostDataValue(path string, fields map[string]string) (int, []byte) {
	c.e.T.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", c.e.Srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c.basic {
		req.SetBasicAuth(c.User, c.e.M.Password)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		c.e.T.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// CentralDataValue reads one value from central as the superuser.
func (e *Env) CentralDataValue(ds, pe, de, coc, aoc string) (value, storedBy string, ok bool) {
	e.T.Helper()
	var res struct {
		DataValues []struct {
			DataElement, Period, CategoryOptionCombo, AttributeOptionCombo, Value, StoredBy string
			Deleted                                                                         bool
		} `json:"dataValues"`
	}
	q := url.Values{"dataSet": {ds}, "period": {pe}, "orgUnit": {e.M.Facility.ID}}
	if err := e.Central.JSON("GET", "/api/dataValueSets?"+q.Encode(), nil, &res); err != nil {
		e.T.Fatal(err)
	}
	for _, v := range res.DataValues {
		if v.DataElement == de && v.Period == pe && v.CategoryOptionCombo == coc && v.AttributeOptionCombo == aoc && !v.Deleted {
			return v.Value, v.StoredBy, true
		}
	}
	return "", "", false
}

// SetCentralDataValue enters a value on central as the superuser (default attribute).
func (e *Env) SetCentralDataValue(ds, pe, de, coc, value string) {
	e.T.Helper()
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {e.M.Facility.ID}, "de": {de}, "co": {coc}, "value": {value}}
	if st, b, err := e.Central.Do("POST", "/api/dataValues?"+q.Encode(), nil); err != nil || st/100 != 2 {
		e.T.Fatalf("set central value: %d %s %v", st, b, err)
	}
}

// CurrentMonth is this month's period ID in central's time zone.
func CurrentMonth(e *Env) string { return e.App.Hub.CentralNow().Format("200601") }

// DeleteCentralDataValue deletes a value on central as the superuser (default
// attribute); a missing value is fine.
func (e *Env) DeleteCentralDataValue(ds, pe, de, coc string) {
	e.T.Helper()
	q := url.Values{"ds": {ds}, "pe": {pe}, "ou": {e.M.Facility.ID}, "de": {de}, "co": {coc}}
	if st, b, err := e.Central.Do("DELETE", "/api/dataValues?"+q.Encode(), nil); err != nil || (st/100 != 2 && st != 404 && st != 409) {
		e.T.Fatalf("delete central value: %d %s %v", st, b, err)
	}
}
