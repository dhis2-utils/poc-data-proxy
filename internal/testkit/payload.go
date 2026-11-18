package testkit

import (
	"encoding/json"
	"time"
)

// Person is a tracked entity enrolled in a program, built so the profile's validation
// passes: mandatory attributes and compulsory data elements come from the manifest,
// unique values are fresh, and the search attribute carries name.
type Person struct {
	TE, Enrollment string
	Events         []string // one per stage in stages, in order
	Unique         string   // the value of the program's unique attribute, if any
	Payload        map[string]any
}

// NewPerson builds a person at the facility, enrolled in p, with one event in each
// of the given stages (none by default).
func (m *Manifest) NewPerson(p Program, name string, stages ...string) *Person {
	today := time.Now().UTC().Format("2006-01-02")
	pr := &Person{TE: UID(), Enrollment: UID()}
	values := map[string]string{}
	for a, v := range p.RequiredAttrs {
		values[a] = v
	}
	if p.SearchAttr != "" {
		values[p.SearchAttr] = name
	}
	if p.UniqueAttr != "" {
		pr.Unique = "T-" + UID()
		values[p.UniqueAttr] = pr.Unique
	}
	for _, a := range p.OtherUnique {
		values[a] = "T-" + UID()
	}
	attrs := []any{}
	for a, v := range values {
		attrs = append(attrs, map[string]any{"attribute": a, "value": v})
	}
	events := []any{}
	for _, st := range stages {
		ev := UID()
		pr.Events = append(pr.Events, ev)
		events = append(events, m.event(p, st, ev, pr.Enrollment, today))
	}
	pr.Payload = map[string]any{"trackedEntities": []any{map[string]any{
		"trackedEntity": pr.TE, "trackedEntityType": p.TET, "orgUnit": m.Facility.ID, "attributes": attrs,
		"enrollments": []any{map[string]any{"enrollment": pr.Enrollment, "program": p.ID, "orgUnit": m.Facility.ID,
			"enrolledAt": today, "occurredAt": today, "status": "ACTIVE", "events": events}}}}}
	return pr
}

func (m *Manifest) event(p Program, stage, ev, enrollment, date string) map[string]any {
	dvs := []any{}
	for d, v := range p.EventValues[stage] {
		dvs = append(dvs, map[string]any{"dataElement": d, "value": v})
	}
	return map[string]any{"event": ev, "enrollment": enrollment, "program": p.ID, "programStage": stage,
		"orgUnit": m.Facility.ID, "occurredAt": date, "status": "ACTIVE", "dataValues": dvs}
}

// NewEvent builds a payload adding one event in stage to an existing enrollment.
func (m *Manifest) NewEvent(p Program, stage, enrollment string) (string, map[string]any) {
	ev := UID()
	return ev, map[string]any{"events": []any{m.event(p, stage, ev, enrollment, time.Now().UTC().Format("2006-01-02"))}}
}

// SyncImport is the tracker import path the web app uses (synchronous, atomic).
const SyncImport = "/api/tracker?async=false&importStrategy=CREATE_AND_UPDATE&atomicMode=ALL"

// SetAttr sets an attribute value on the person's tracked entity.
func (pr *Person) SetAttr(attr, value string) {
	te := pr.Payload["trackedEntities"].([]any)[0].(map[string]any)
	attrs := te["attributes"].([]any)
	for _, a := range attrs {
		if am := a.(map[string]any); am["attribute"] == attr {
			am["value"] = value
			return
		}
	}
	te["attributes"] = append(attrs, map[string]any{"attribute": attr, "value": value})
}

// Complete fills the person's mandatory generated attributes with values reserved
// through the hub as this client, as the Capture apps do before registering.
func (c *Client) Complete(p Program, pr *Person) {
	c.e.T.Helper()
	for _, attr := range p.MandatoryGenerated {
		st, b := c.Do("GET", "/api/trackedEntityAttributes/"+attr+"/generate", nil)
		var v struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(b, &v); st != 200 || v.Value == "" {
			c.e.T.Fatalf("reserving a value of %s as %s: %d %.300s", attr, c.Role, st, b)
		}
		pr.SetAttr(attr, v.Value)
	}
}

// CentralComplete does the same directly on central, for records written there.
func (e *Env) CentralComplete(p Program, pr *Person) {
	e.T.Helper()
	for _, attr := range p.MandatoryGenerated {
		var v struct {
			Value string `json:"value"`
		}
		if err := e.Central.JSON("GET", "/api/trackedEntityAttributes/"+attr+"/generate", nil, &v); err != nil {
			e.T.Fatal(err)
		}
		pr.SetAttr(attr, v.Value)
	}
}
