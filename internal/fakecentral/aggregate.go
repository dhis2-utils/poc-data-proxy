package fakecentral

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Aggregate metadata: three monthly data sets at the facility.
const (
	AggDataSet   = "FakeDsMon01" // default attribute combo; AggDeReq compulsory for completion
	AggValidDS   = "FakeDsVal01" // validCompleteOnly
	AggDonorDS   = "FakeDsDon01" // attribute combo AggDonorCC
	AggDeNum     = "FakeDeNum01"
	AggDeInt     = "FakeDeInt01"
	AggDeReq     = "FakeDeReq01"
	AggCOC       = DefaultAOC // the default category option combo, also the default AOC
	AggCC        = "bjDvmb4bfuf"
	AggOption    = "xYerKDKCefk"
	AggDonorCC   = "FakeCcDon01"
	AggDonorOptA = "FakeCoDonA1" // shared with nurse_a only
	AggDonorOptB = "FakeCoDonB1"
	AggDonorAOCA = "FakeAocDonA"
	AggDonorAOCB = "FakeAocDonB"
)

type aggValue struct {
	Value, Comment, StoredBy, Created, LastUpdated string
	FollowUp, Deleted                              bool
}

type aggReg struct {
	Completed               bool
	StoredBy, Date, Created string
}

// aggUsers enter aggregate data; aggOptionsOf lists the donor options each may use.
var aggUsers = map[string][]string{"hub_admin": {AggDonorOptA, AggDonorOptB}, "nurse_a": {AggDonorOptA, AggDonorOptB},
	"clerk_c": {AggDonorOptB}}

var versioned = regexp.MustCompile(`^/api/[0-9]{2}/`)

// aggregate serves the aggregate endpoints; ok is false for other paths.
func (c *Central) aggregate(w http.ResponseWriter, r *http.Request, u *User) bool {
	p := r.URL.Path
	if m := versioned.FindString(p); m != "" {
		p = "/api/" + p[len(m):]
	}
	opts, enters := aggUsers[u.Username]
	c.mu.Lock()
	fail, hook := c.AggFail[p], c.OnAggWrite
	c.mu.Unlock()
	if fail != 0 {
		webMessage(w, fail, "failing as asked", "")
		return true
	}
	if hook != nil && p == "/api/dataValues" {
		hook()
	}
	switch p {
	case "/api/dataEntry/metadata":
		if !enters {
			writeJSON(w, 200, map[string]any{"dataSets": []any{}, "dataElements": []any{}, "categoryCombos": []any{},
				"categories": []any{}, "categoryOptions": []any{}, "optionSets": []any{}, "indicators": []any{}})
			return true
		}
		writeJSON(w, 200, c.aggMetadata(opts))
	case "/api/dataEntry/dataValues":
		q := r.URL.Query()
		aoc, ok := aggAOC(q.Get("ds"), q.Get("cc"), q.Get("cp"))
		if !enters || !ok {
			webMessage(w, 409, "Data set not found or not accessible", "E1102")
			return true
		}
		writeJSON(w, 200, c.aggForm(q.Get("ds"), q.Get("pe"), aoc))
	case "/api/dataValues":
		c.aggWrite(w, r, u, enters, opts)
	case "/api/dataEntry/dataSetCompletion":
		c.aggComplete(w, r, u, enters)
	case "/api/dataValueSets":
		c.aggValueSets(w, r)
	case "/api/completeDataSetRegistrations":
		c.aggRegistrations(w, r)
	case "/api/categoryOptionCombos":
		// The donor combination's option combinations: dataEntry/metadata leaves out
		// those of attribute combinations, as central does.
		if !strings.Contains(r.URL.Query().Get("filter"), AggDonorCC) {
			return false
		}
		writeJSON(w, 200, map[string]any{"categoryOptionCombos": []any{
			map[string]any{"id": AggDonorAOCA, "displayName": "Donor A", "categoryCombo": map[string]any{"id": AggDonorCC},
				"categoryOptions": []any{map[string]any{"id": AggDonorOptA}}},
			map[string]any{"id": AggDonorAOCB, "displayName": "Donor B", "categoryCombo": map[string]any{"id": AggDonorCC},
				"categoryOptions": []any{map[string]any{"id": AggDonorOptB}}}}})
	default:
		return false
	}
	return true
}

func aggAOC(ds, cc, cp string) (string, bool) {
	if ds == AggDonorDS {
		if cc != AggDonorCC {
			return "", false
		}
		switch cp {
		case AggDonorOptA:
			return AggDonorAOCA, true
		case AggDonorOptB:
			return AggDonorAOCB, true
		}
		return "", false
	}
	return AggCOC, cc == "" || cc == AggCC
}

func (c *Central) aggMetadata(opts []string) map[string]any {
	ref := func(id string) map[string]any { return map[string]any{"id": id} }
	dse := func(ids ...string) []any {
		var out []any
		for _, id := range ids {
			out = append(out, map[string]any{"dataElement": ref(id)})
		}
		return out
	}
	ds := func(id, name, cc string, extra map[string]any) map[string]any {
		m := map[string]any{"id": id, "displayName": name, "periodType": "Monthly", "categoryCombo": ref(cc),
			"expiryDays": 0.0, "openFuturePeriods": 1, "dataInputPeriods": []any{}, "compulsoryDataElementOperands": []any{},
			"dataSetElements": dse(AggDeNum, AggDeInt, AggDeReq), "organisationUnits": []any{Facility}}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	c.mu.Lock()
	expiry := c.AggExpiryDays
	c.mu.Unlock()
	options := []any{ref(AggOption)} // the options the user may write, as central lists them
	for _, o := range opts {
		options = append(options, ref(o))
	}
	return map[string]any{
		"dataSets": []any{
			ds(AggDataSet, "Fake monthly", AggCC, map[string]any{"expiryDays": expiry, "compulsoryFieldsCompleteOnly": true,
				"compulsoryDataElementOperands": []any{map[string]any{"dataElement": ref(AggDeReq), "categoryOptionCombo": ref(AggCOC)}}}),
			ds(AggValidDS, "Fake validated", AggCC, map[string]any{"validCompleteOnly": true}),
			ds(AggDonorDS, "Fake donor", AggDonorCC, nil),
		},
		"dataElements": []any{
			map[string]any{"id": AggDeNum, "displayName": "Fake number", "valueType": "NUMBER", "categoryCombo": ref(AggCC)},
			map[string]any{"id": AggDeInt, "displayName": "Fake integer", "valueType": "INTEGER", "categoryCombo": ref(AggCC)},
			map[string]any{"id": AggDeReq, "displayName": "Fake required", "valueType": "NUMBER", "categoryCombo": ref(AggCC)},
		},
		"categoryCombos": []any{
			map[string]any{"id": AggCC, "isDefault": true, "categoryOptionCombos": []any{
				map[string]any{"id": AggCOC, "displayName": "default", "categoryOptions": []string{AggOption}}}},
			map[string]any{"id": AggDonorCC, "isDefault": false}, // no option combinations, as on central
		},
		"categories": []any{}, "categoryOptions": options, "optionSets": []any{}, "indicators": []any{},
	}
}

func aggKey(pe, de, coc, aoc string) string { return pe + "|" + de + "|" + coc + "|" + aoc }

func (c *Central) aggLocked(ds, pe string) bool {
	if c.approved[ds+"|"+pe] {
		return true
	}
	if ds != AggDataSet || c.AggExpiryDays <= 0 {
		return false
	}
	start, err := time.Parse("200601", pe)
	if err != nil {
		return false
	}
	end := start.AddDate(0, 1, -1)
	return time.Now().After(end.Add(time.Duration(c.AggExpiryDays * float64(24*time.Hour))))
}

func (c *Central) aggForm(ds, pe, aoc string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	combo, options := AggCC, []string{AggOption}
	if ds == AggDonorDS {
		combo, options = AggDonorCC, []string{map[string]string{AggDonorAOCA: AggDonorOptA, AggDonorAOCB: AggDonorOptB}[aoc]}
	}
	vals := []any{}
	keys := make([]string, 0, len(c.aggValues))
	for k := range c.aggValues {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts := strings.Split(k, "|")
		v := c.aggValues[k]
		if parts[0] != pe || parts[3] != aoc || v.Deleted || !aggInDataSet(parts[1]) {
			continue
		}
		m := map[string]any{"dataElement": parts[1], "period": pe, "orgUnit": Facility, "categoryOptionCombo": parts[2],
			"attribute": map[string]any{"combo": combo, "options": options}, "value": v.Value, "followUp": v.FollowUp,
			"storedBy": v.StoredBy, "created": v.Created, "lastUpdated": v.LastUpdated}
		if v.Comment != "" {
			m["comment"] = v.Comment
		}
		vals = append(vals, m)
	}
	lock := "OPEN"
	if c.approved[ds+"|"+pe] {
		lock = "APPROVED"
	} else if c.aggLocked(ds, pe) {
		lock = "LOCKED"
	}
	cs := map[string]any{"complete": false}
	if reg, ok := c.aggRegs[ds+"|"+pe+"|"+aoc]; ok {
		cs = map[string]any{"complete": reg.Completed, "created": reg.Created, "createdBy": reg.StoredBy,
			"lastUpdated": reg.Created, "lastUpdatedBy": reg.StoredBy}
	}
	return map[string]any{"dataValues": vals, "minMaxValues": []any{map[string]any{"dataElement": AggDeNum,
		"orgUnit": Facility, "categoryOptionCombo": AggCOC, "minValue": 0, "maxValue": 1000}}, "lockStatus": lock,
		"completeStatus": cs}
}

func aggInDataSet(de string) bool { return de == AggDeNum || de == AggDeInt || de == AggDeReq }

// params merges the query with a urlencoded or multipart body, as central reads them.
func params(r *http.Request) url.Values {
	q := r.URL.Query()
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch ct {
	case "multipart/form-data":
		if r.ParseMultipartForm(1<<20) == nil {
			for k, v := range r.MultipartForm.Value {
				q[k] = v
			}
		}
	case "application/x-www-form-urlencoded":
		if r.ParseForm() == nil {
			for k, v := range r.PostForm {
				q[k] = v
			}
		}
	}
	return q
}

func (c *Central) aggWrite(w http.ResponseWriter, r *http.Request, u *User, enters bool, opts []string) {
	q := params(r)
	ds, pe, de, coc := q.Get("ds"), q.Get("pe"), q.Get("de"), q.Get("co")
	aoc, ok := aggAOC(ds, q.Get("cc"), q.Get("cp"))
	if !enters || !ok || !aggInDataSet(de) || q.Get("ou") != Facility {
		webMessage(w, 409, "Data element not found or not accessible: `"+de+"`", "E1100")
		return
	}
	if ds == AggDonorDS && !strings.Contains(strings.Join(opts, ","), q.Get("cp")) {
		webMessage(w, 409, "User does not have write access to category option combo: `"+aoc+"`", "E2031")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.aggLocked(ds, pe) {
		webMessage(w, 409, "Data set is locked", "E2017")
		return
	}
	k := aggKey(pe, de, coc, aoc)
	v := c.aggValues[k]
	now := c.now()
	if v == nil || v.Deleted {
		v = &aggValue{Created: now}
		c.aggValues[k] = v
	}
	if r.Method == http.MethodDelete {
		if v.Value == "" && v.Comment == "" {
			delete(c.aggValues, k)
			webMessage(w, 409, "Data value cannot be deleted because it does not exist", "")
			return
		}
		v.Deleted, v.LastUpdated = true, now
		w.WriteHeader(204)
		return
	}
	if q.Has("value") {
		typ := map[string]string{AggDeNum: "NUMBER", AggDeInt: "INTEGER", AggDeReq: "NUMBER"}[de]
		val := q.Get("value")
		_, ferr := strconv.ParseFloat(val, 64)
		_, ierr := strconv.Atoi(val)
		if (typ == "NUMBER" && ferr != nil) || (typ == "INTEGER" && ierr != nil) {
			webMessage(w, 409, "Data value must match data element value type: `"+typ+"`", "E2030")
			return
		}
		v.Value = val
	}
	if q.Has("comment") {
		v.Comment = q.Get("comment")
	}
	if q.Has("followUp") {
		v.FollowUp = q.Get("followUp") == "true"
	}
	v.StoredBy, v.LastUpdated, v.Deleted = u.Username, now, false
	w.WriteHeader(201)
}

func (c *Central) aggComplete(w http.ResponseWriter, r *http.Request, u *User, enters bool) {
	var req struct {
		DataSet, Period, OrgUnit string
		Attribute                *struct {
			Combo   string   `json:"combo"`
			Options []string `json:"options"`
		} `json:"attribute"`
		Completed bool `json:"completed"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || !enters {
		webMessage(w, 409, "Bad completion", "")
		return
	}
	cc, cp := "", ""
	if req.Attribute != nil {
		cc, cp = req.Attribute.Combo, strings.Join(req.Attribute.Options, ";")
	}
	aoc, ok := aggAOC(req.DataSet, cc, cp)
	if !ok {
		webMessage(w, 409, "Attribute option combo not found", "")
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if req.Completed && req.DataSet == AggDataSet {
		if v := c.aggValues[aggKey(req.Period, AggDeReq, AggCOC, aoc)]; v == nil || v.Deleted || v.Value == "" {
			webMessage(w, 409, "All compulsory data element operands need to be filled: [Fake required default]", "")
			return
		}
	}
	c.aggRegs[req.DataSet+"|"+req.Period+"|"+aoc] = &aggReg{Completed: req.Completed, StoredBy: u.Username,
		Date: c.now()[:10], Created: c.now()}
	w.WriteHeader(200)
}

func (c *Central) aggValueSets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since := q.Get("lastUpdated")
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []any{}
	for k, v := range c.aggValues {
		parts := strings.Split(k, "|")
		if since != "" && v.LastUpdated < since {
			continue
		}
		if pe := q.Get("period"); pe != "" && parts[0] != pe {
			continue
		}
		if v.Deleted && q.Get("includeDeleted") != "true" {
			continue
		}
		m := map[string]any{"dataElement": parts[1], "period": parts[0], "orgUnit": Facility, "categoryOptionCombo": parts[2],
			"attributeOptionCombo": parts[3], "value": v.Value, "storedBy": v.StoredBy, "created": v.Created + "+0000",
			"lastUpdated": v.LastUpdated + "+0000", "followup": v.FollowUp}
		if v.Deleted {
			m["deleted"] = true
		}
		out = append(out, m)
	}
	res := map[string]any{"dataValues": out}
	if c.CompactValueSets && len(out) > 0 {
		for _, f := range []string{"orgUnit", "attributeOptionCombo", "period"} {
			same := true
			for _, v := range out {
				same = same && v.(map[string]any)[f] == out[0].(map[string]any)[f]
			}
			if same {
				res[f] = out[0].(map[string]any)[f]
				for _, v := range out {
					delete(v.(map[string]any), f)
				}
			}
		}
	}
	writeJSON(w, 200, res)
}

func (c *Central) aggRegistrations(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []any{}
	for k, reg := range c.aggRegs {
		parts := strings.Split(k, "|")
		out = append(out, map[string]any{"period": parts[1], "dataSet": parts[0], "organisationUnit": Facility,
			"attributeOptionCombo": parts[2], "date": reg.Date, "storedBy": reg.StoredBy, "completed": reg.Completed})
	}
	writeJSON(w, 200, map[string]any{"completeDataSetRegistrations": out})
}

// AggValue reads a stored value (not deleted).
func (c *Central) AggValue(pe, de, coc, aoc string) (value, storedBy string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.aggValues[aggKey(pe, de, coc, aoc)]
	if v == nil || v.Deleted {
		return "", "", false
	}
	return v.Value, v.StoredBy, true
}

// SetAggValue stores a value as if a user had entered it on central.
func (c *Central) SetAggValue(pe, de, coc, aoc, value, storedBy string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.aggValues[aggKey(pe, de, coc, aoc)] = &aggValue{Value: value, StoredBy: storedBy, Created: now, LastUpdated: now}
}

// Approve approves a data set's period: writes are refused, forms say APPROVED.
func (c *Central) Approve(ds, pe string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.approved[ds+"|"+pe] = true
}

// Unapprove withdraws an approval (central was fixed).
func (c *Central) Unapprove(ds, pe string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.approved, ds+"|"+pe)
}

// Complete reports whether the form is registered complete.
func (c *Central) Complete(ds, pe, aoc string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	reg := c.aggRegs[ds+"|"+pe+"|"+aoc]
	return reg != nil && reg.Completed
}

// GenerateAggregate stores values for the performance tests: for each of the last
// periods months, cells values of the three data elements (cycled) under generated
// category option combinations, entered by nurse_a. Every fake data set holds the
// same three elements, so each form shows all of a period's values: a form far
// larger than real ones, the worst case for building a form offline.
func (c *Central) GenerateAggregate(periods, cells int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	ts := c.now()
	des := []string{AggDeNum, AggDeInt, AggDeReq}
	for p := 0; p < periods; p++ {
		pe := now.AddDate(0, -p, 0).Format("200601")
		for i := 0; i < cells; i++ {
			coc := fmt.Sprintf("FakeCoc%04d", i/len(des))
			c.aggValues[aggKey(pe, des[i%len(des)], coc, AggCOC)] = &aggValue{Value: strconv.Itoa(i % 97),
				StoredBy: "nurse_a", Created: ts, LastUpdated: ts}
		}
	}
}
