package ui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/dhis2/facility-hub/internal/aggregate"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// The three answers to "what closes it" (docs/design/2026-11-04-refused-
// changes.md, "Refusals by who can fix them").
const (
	ClosedByAuthor  = "Fixed by the author"
	ClosedOnCentral = "Fixed on central"
	ClosedByNobody  = "Fixed by nobody"
	// A refusal superseded by a later change on central (store.SyncSuperseded).
	ClosedByManager = "Central has changed this record since; the hub shows central's version. A hub manager resends or discards this change"
)

var (
	authorCodes = codeSet("E1302 E1007 E1125 E1303 E1064 E1020 E1021 E1015 E1016 E1039")
	nobodyCodes = codeSet("E1082 E1113 E1114 E4017")
)

func codeSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, c := range strings.Fields(s) {
		m[c] = true
	}
	return m
}

// closedBy is who closes a refusal with these codes. Codes the plan does not name go
// to central, where a supervisor sees them on the Refusals page.
func closedBy(codes []string) string {
	for _, c := range codes {
		if nobodyCodes[c] {
			return ClosedByNobody
		}
	}
	for _, c := range codes {
		if authorCodes[c] {
			return ClosedByAuthor
		}
	}
	return ClosedOnCentral
}

// ownValue is one value the author entered.
type ownValue struct{ Name, Value string }

// ownRefusal is one of the signed-in user's refused changes, with what they entered:
// they are its author, and the values are on this hub already.
type ownRefusal struct {
	When, OrgUnit, Kind, Object, Program string
	Messages                             []string
	Codes                                []string
	Values                               []ownValue
	// ValuesGone: the outbox entry with the values is no longer on the hub.
	ValuesGone bool
	Closes     string
	// WaitsFor is the parent the change waits for ("blocked by", E5000): it closes
	// with that parent.
	WaitsFor string
	// Capture opens the record in Capture through this hub, which answers the author's
	// reads of it from its copy (trackerapi.ownRefusedRecord): for those the author
	// fixes, and not for a change that waits for its parent.
	Capture string
}

// ownRefusals are the signed-in user's open refused changes, newest first, unmasked.
// Nobody else's: the page's issue table shows those, masked.
func (u *UI) ownRefusals(userID int64) []*ownRefusal {
	h := u.H
	issues, _ := h.Store.OpenIssuesOf(userID, 100)
	facility, facilityName := h.Facility(), ""
	if rt := h.Runtime(); rt != nil {
		facilityName = rt.FacilityName
	}
	ouName := func(ou string) string {
		if ou == facility && facilityName != "" {
			return facilityName
		}
		return ou
	}
	cat := h.Catalog()
	items := map[int64]map[string]*tracker.Item{}
	itemsOf := func(seq int64) map[string]*tracker.Item {
		if m, ok := items[seq]; ok {
			return m
		}
		var m map[string]*tracker.Item
		if b, err := h.Store.OutboxPayload(seq); err == nil {
			var p tracker.M
			if json.Unmarshal(b, &p) == nil {
				m = map[string]*tracker.Item{}
				for _, it := range tracker.Flatten(p) {
					m[it.UID] = it
				}
			}
		}
		items[seq] = m
		return m
	}
	var out []*ownRefusal
	byUID := map[string]*ownRefusal{}
	for _, is := range issues {
		var e struct {
			Message string                `json:"message"`
			Reports []tracker.ErrorReport `json:"errorReports"`
			Edit    *aggregate.Edit       `json:"edit"`
		}
		json.Unmarshal(is.Error, &e)
		r := &ownRefusal{When: is.CreatedAt, Kind: is.Kind, Object: is.ObjectUID}
		if len(r.When) > 16 {
			r.When = strings.Replace(r.When[:16], "T", " ", 1)
		}
		for _, rep := range e.Reports {
			r.Messages = append(r.Messages, rep.Message)
			r.Codes = append(r.Codes, rep.ErrorCode)
		}
		if len(r.Messages) == 0 && e.Message != "" {
			r.Messages = []string{e.Message}
		}
		if by, ok := strings.CutPrefix(e.Message, "blocked by "); ok {
			r.WaitsFor = by
		}
		if is.Kind == store.OutboxAggregate {
			r.Kind, r.Object = "data value", h.AggLabel(is.ObjectUID)
			if e.Edit != nil {
				r.OrgUnit = ouName(e.Edit.Form.OrgUnit)
				if v := e.Edit.Value; v != nil {
					r.Values = append(r.Values, ownValue{"value", *v})
				}
				if c := e.Edit.Comment; c != nil {
					r.Values = append(r.Values, ownValue{"comment", *c})
				}
			}
		} else if it := itemsOf(is.OutboxSeq)[is.ObjectUID]; it != nil {
			r.OrgUnit = ouName(tracker.Str(it.Data, "orgUnit"))
			prog := tracker.Str(it.Data, "program")
			if prog == "" {
				prog = cat.StageProgram[tracker.Str(it.Data, "programStage")]
			}
			if p := cat.Programs[prog]; p != nil {
				r.Program = p.Name
			}
			r.Values = itemValues(it)
			r.Capture = captureLink(h.Index, it)
			if r.WaitsFor == "" && slices.Contains(r.Codes, "E5000") {
				switch it.Kind {
				case store.KindEnrollment:
					r.WaitsFor = it.TE
				case store.KindEvent:
					r.WaitsFor = it.Enrollment
				}
			}
		} else {
			r.ValuesGone = true
		}
		r.Closes = closedBy(r.Codes)
		if o := h.Index.Get(is.ObjectUID); o != nil && o.SyncState == store.SyncSuperseded {
			r.Closes = ClosedByManager
		}
		if r.Closes != ClosedByAuthor || r.WaitsFor != "" {
			r.Capture = ""
		}
		out = append(out, r)
		byUID[r.Object] = r
	}
	// A change that waits for its parent closes with it: event, enrollment, person.
	for range 3 {
		for _, r := range out {
			if p := byUID[r.WaitsFor]; r.WaitsFor != "" && p != nil {
				r.Closes = p.Closes
			}
		}
	}
	return out
}

// itemValues are the dates and values a tracker object carries, in the payload's order.
func itemValues(it *tracker.Item) []ownValue {
	var vs []ownValue
	for _, k := range []string{"enrolledAt", "occurredAt", "scheduledAt", "completedAt"} {
		if v := tracker.Str(it.Data, k); v != "" {
			if len(v) > 10 && strings.HasSuffix(v, "T00:00:00.000") {
				v = v[:10]
			}
			vs = append(vs, ownValue{k, v})
		}
	}
	for _, f := range []struct{ list, key string }{{"attributes", "attribute"}, {"dataValues", "dataElement"}} {
		arr, _ := it.Data[f.list].([]any)
		for _, a := range arr {
			m, _ := a.(tracker.M)
			if m == nil {
				continue
			}
			v := ""
			if m["value"] != nil {
				v = fmt.Sprint(m["value"])
			}
			vs = append(vs, ownValue{tracker.Str(m, f.key), v})
		}
	}
	if it.Kind == store.KindRelationship {
		vs = append(vs, ownValue{"from", it.From.UID}, ownValue{"to", it.To.UID})
	}
	return vs
}

// captureLink is the record's page in Capture at this hub's address, with the routes
// the Facility Hub app links to on central (apps/facility-hub/src/hubs/issues.ts).
func captureLink(ix *tracker.Index, it *tracker.Item) string {
	app := "/dhis-web-capture/index.html#/"
	switch it.Kind {
	case store.KindTrackedEntity:
		if enrs := ix.EnrollmentsOf(it.UID); len(enrs) > 0 {
			return app + "enrollment?teiId=" + it.UID + "&enrollmentId=" + enrs[0].UID
		}
		return app + "enrollment?teiId=" + it.UID
	case store.KindEnrollment:
		return app + "enrollment?enrollmentId=" + it.UID
	case store.KindEvent:
		if it.Enrollment != "" {
			return app + "enrollmentEventEdit?eventId=" + it.UID
		}
		return app + "viewEvent?viewEventId=" + it.UID
	}
	return ""
}
