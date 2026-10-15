package aggregate

import (
	"encoding/json"
	"net/url"
	"strings"
)

// FormKey names one form: a data set, period, org unit and attribute option
// combination.
type FormKey struct{ DataSet, Period, OrgUnit, AOC string }

func (k FormKey) String() string {
	return "agg:" + k.DataSet + "|" + k.Period + "|" + k.OrgUnit + "|" + k.AOC
}

// Edit ops.
const (
	OpValue    = "value" // POST dataValues: value, comment or followUp
	OpDelete   = "delete"
	OpComplete = "complete" // dataEntry/dataSetCompletion, completed true or false
)

// Edit is one change a user made offline, as queued in the outbox. CC and CP are the
// attribute combination and options as the app sent them (empty for the default).
type Edit struct {
	Op          string   `json:"op"`
	Form        FormKey  `json:"form"`
	CC          string   `json:"cc,omitempty"`
	CP          []string `json:"cp,omitempty"`
	DataElement string   `json:"de,omitempty"`
	COC         string   `json:"co,omitempty"`
	Value       *string  `json:"value,omitempty"`
	Comment     *string  `json:"comment,omitempty"`
	FollowUp    *bool    `json:"followUp,omitempty"`
	Completed   bool     `json:"completed,omitempty"`
	By          string   `json:"by"` // username, for the form shown offline
	At          string   `json:"at"` // central's time, "2006-01-02T15:04:05.000"
}

// Field is what part of a cell the edit sets: edits of the same field replace each
// other in the outbox (store.AppendAggregate); a delete replaces the value.
func (e *Edit) Field() string {
	switch {
	case e.Op == OpComplete:
		return "complete"
	case e.Op == OpDelete || e.Value != nil:
		return "value"
	case e.Comment != nil:
		return "comment"
	}
	return "followUp"
}

func (e *Edit) CellKey() string {
	if e.Op == OpComplete {
		return e.Form.String() + "|complete"
	}
	return e.Form.String() + "|" + e.DataElement + "|" + e.COC + "|" + e.Field()
}

// Form is central's dataEntry/dataValues answer for one form.
type Form map[string]any

// EmptyForm is a form central has no data for.
func EmptyForm() Form {
	return Form{"dataValues": []any{}, "minMaxValues": []any{}, "lockStatus": Open,
		"completeStatus": map[string]any{"complete": false}}
}

func (f Form) values() []any {
	vs, _ := f["dataValues"].([]any)
	return vs
}

func (f Form) find(de, coc string) (int, map[string]any) {
	for i, v := range f.values() {
		if m, ok := v.(map[string]any); ok && m["dataElement"] == de && m["categoryOptionCombo"] == coc {
			return i, m
		}
	}
	return -1, nil
}

// Value is a cell's value in the form.
func (f Form) Value(de, coc string) (string, bool) {
	_, m := f.find(de, coc)
	if m == nil {
		return "", false
	}
	v, ok := m["value"].(string)
	return v, ok
}

// Apply puts an edit into the form as central would show it afterwards. combo and
// options are the form's attribute ("attribute": {"combo", "options"}), for new cells.
func (f Form) Apply(e *Edit, combo string, options []string) {
	switch e.Op {
	case OpComplete:
		cs, _ := f["completeStatus"].(map[string]any)
		if cs == nil {
			cs = map[string]any{}
		}
		if _, ok := cs["created"]; !ok {
			cs["created"], cs["createdBy"] = e.At, e.By
		}
		cs["complete"], cs["lastUpdated"], cs["lastUpdatedBy"] = e.Completed, e.At, e.By
		f["completeStatus"] = cs
	case OpDelete:
		if i, _ := f.find(e.DataElement, e.COC); i >= 0 {
			vs := f.values()
			f["dataValues"] = append(vs[:i:i], vs[i+1:]...)
		}
	case OpValue:
		_, m := f.find(e.DataElement, e.COC)
		if m == nil {
			m = map[string]any{"dataElement": e.DataElement, "period": e.Form.Period, "orgUnit": e.Form.OrgUnit,
				"categoryOptionCombo": e.COC, "attribute": map[string]any{"combo": combo, "options": options},
				"followUp": false, "created": e.At}
			f["dataValues"] = append(f.values(), m)
		}
		if e.Value != nil {
			m["value"] = *e.Value
		}
		if e.Comment != nil {
			m["comment"] = *e.Comment
		}
		if e.FollowUp != nil {
			m["followUp"] = *e.FollowUp
		}
		m["storedBy"], m["lastUpdated"] = e.By, e.At
	}
}

// Request is how the edit is pushed to central, as its author.
func (e *Edit) Request() (method, path string, body []byte) {
	if e.Op == OpComplete {
		req := map[string]any{"dataSet": e.Form.DataSet, "period": e.Form.Period, "orgUnit": e.Form.OrgUnit,
			"completed": e.Completed}
		if e.CC != "" {
			req["attribute"] = map[string]any{"combo": e.CC, "options": e.CP}
		}
		b, _ := json.Marshal(req)
		return "POST", "/api/dataEntry/dataSetCompletion", b
	}
	q := url.Values{"de": {e.DataElement}, "co": {e.COC}, "ds": {e.Form.DataSet}, "ou": {e.Form.OrgUnit}, "pe": {e.Form.Period}}
	if e.CC != "" {
		q.Set("cc", e.CC)
		q.Set("cp", strings.Join(e.CP, ";"))
	}
	if e.Op == OpDelete {
		return "DELETE", "/api/dataValues?" + q.Encode(), nil
	}
	switch {
	case e.Value != nil:
		q.Set("value", *e.Value)
	case e.Comment != nil:
		q.Set("comment", *e.Comment)
	case e.FollowUp != nil:
		q.Set("followUp", map[bool]string{true: "true", false: "false"}[*e.FollowUp])
	}
	return "POST", "/api/dataValues?" + q.Encode(), nil
}
