package compat

// DHIS2 2.42 request and response shapes. Every shape here was checked against the
// fixtures in testdata/2.42 (see testdata/2.42/SETUP.md).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/dhis2/facility-hub/internal/tracker"
)

type v42 struct{}

func (v42) Major() int { return 42 }

// ListResponse: {"pager":{page,pageSize[,total,pageCount][,prevPage][,nextPage]}, "<collection>":[...]}.
// nextPage is present when the page is full, prevPage when page > 1, total and
// pageCount only with totalPages=true. paging=false drops the pager entirely.
func (v42) ListResponse(r *tracker.Result, selfURL string) any {
	out := map[string]any{r.Collection: r.Items}
	if !r.Paging {
		return out
	}
	pager := map[string]any{"page": r.Page, "pageSize": r.PageSize}
	if r.TotalPages {
		pager["total"] = r.Total
		pageCount := (r.Total + r.PageSize - 1) / r.PageSize
		pager["pageCount"] = pageCount
	}
	if r.Page > 1 {
		pager["prevPage"] = pageURL(selfURL, r.Page-1)
	}
	if len(r.Items) == r.PageSize && r.Page*r.PageSize < r.Total {
		pager["nextPage"] = pageURL(selfURL, r.Page+1)
	}
	out["pager"] = pager
	return out
}

func pageURL(self string, page int) string {
	u, err := url.Parse(self)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()
	return u.String()
}

var typeOrder = []string{"TRACKED_ENTITY", "ENROLLMENT", "EVENT", "RELATIONSHIP"}

// ImportReport mirrors a synchronous POST /api/tracker answer. Offline the hub always
// behaves as atomicMode=OBJECT (spec 10.2): if anything was accepted the answer is
// 200 / status OK with a bundleReport, as central does for OBJECT; if nothing was
// accepted it is 409 / status ERROR without a bundleReport, as central does for ALL.
func (v42) ImportReport(o *tracker.ImportOutcome) (int, any) {
	total, byType := o.Stats()
	errs := o.Errors
	if errs == nil {
		errs = []tracker.ErrorReport{}
	}
	body := map[string]any{
		"status":           "OK",
		"validationReport": map[string]any{"errorReports": errs, "warningReports": []any{}},
		"stats":            total,
	}
	if len(o.Accepted) == 0 && len(o.Errors) > 0 {
		body["status"] = "ERROR"
		return http.StatusConflict, body
	}
	tm := map[string]any{}
	for _, tt := range typeOrder {
		reports := []any{}
		for _, r := range o.Results {
			if tracker.TrackerType(r.Kind) == tt && len(r.Errors) == 0 {
				reports = append(reports, map[string]any{"trackerType": tt, "uid": r.UID, "errorReports": []any{}})
			}
		}
		var s tracker.Stats
		for k, st := range byType {
			if tracker.TrackerType(k) == tt {
				s = st
			}
		}
		tm[tt] = map[string]any{"trackerType": tt, "stats": s, "objectReports": reports}
	}
	body["bundleReport"] = map[string]any{"typeReportMap": tm}
	return http.StatusOK, body
}

func (v42) WebMessage(status int, message, errorCode string) any {
	m := map[string]any{"httpStatus": http.StatusText(status), "httpStatusCode": status, "status": "ERROR", "message": message}
	if errorCode != "" {
		m["errorCode"] = errorCode
	}
	return m
}

func (v v42) NotFound(kind, uid string) any {
	name := map[string]string{"trackedEntity": "TrackedEntity", "enrollment": "Enrollment", "event": "Event",
		"relationship": "Relationship"}[kind]
	return v.WebMessage(404, fmt.Sprintf("%s with id %s could not be found.", name, uid), "E1005")
}

type importReport struct {
	Status           string `json:"status"`
	ValidationReport struct {
		ErrorReports []tracker.ErrorReport `json:"errorReports"`
	} `json:"validationReport"`
	BundleReport *struct {
		TypeReportMap map[string]struct {
			ObjectReports []struct {
				UID          string                `json:"uid"`
				ErrorReports []tracker.ErrorReport `json:"errorReports"`
			} `json:"objectReports"`
		} `json:"typeReportMap"`
	} `json:"bundleReport"`
}

// TrackerResult: an object was persisted iff it is in bundleReport objectReports with
// no errors. With atomicMode=OBJECT central answers 200/OK even when everything failed,
// and children of a failed parent may be ignored without an error report of their own.
func (v42) TrackerResult(status int, body []byte) (map[string]bool, map[string][]tracker.ErrorReport, error) {
	var r importReport
	if err := json.Unmarshal(body, &r); err != nil || r.Status == "" {
		return nil, nil, fmt.Errorf("unexpected import response (HTTP %d)", status)
	}
	persisted := map[string]bool{}
	errs := map[string][]tracker.ErrorReport{}
	for _, e := range r.ValidationReport.ErrorReports {
		errs[e.UID] = append(errs[e.UID], e)
	}
	if r.BundleReport != nil {
		for _, tr := range r.BundleReport.TypeReportMap {
			for _, or := range tr.ObjectReports {
				if len(or.ErrorReports) > 0 {
					errs[or.UID] = append(errs[or.UID], or.ErrorReports...)
				} else if len(errs[or.UID]) == 0 {
					persisted[or.UID] = true
				}
			}
		}
	}
	return persisted, errs, nil
}

// LoginSuccess: POST /api/auth/login answers {loginStatus, redirectUrl}.
func (v42) LoginSuccess(redirectURL string) any {
	return map[string]any{"loginStatus": "SUCCESS", "redirectUrl": redirectURL}
}

// LoginFailure: 401 WebMessage "Bad credentials" for wrong password and unknown user.
func (v v42) LoginFailure() (int, any) {
	return http.StatusUnauthorized, v.WebMessage(401, "Bad credentials", "")
}

// PATRequest: expire is epoch milliseconds; type defaults to PERSONAL_ACCESS_TOKEN_V2.
// Normal users may create their own token (no authority needed in 2.42).
func (v42) PATRequest(expiresAtMillis int64) any {
	return map[string]any{"type": "PERSONAL_ACCESS_TOKEN_V2", "expire": expiresAtMillis, "attributes": []any{}}
}

// PATResponse: 201 {"response":{"key":"d2p_...","uid":"..."}}. The key is shown once.
func (v42) PATResponse(body []byte) (string, string, error) {
	var r struct {
		Response struct{ Key, UID string } `json:"response"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", "", err
	}
	if r.Response.Key == "" {
		return "", "", fmt.Errorf("no key in apiToken response")
	}
	return r.Response.UID, r.Response.Key, nil
}

func (v42) PullPath(facility, program, updatedAfter string, page, pageSize int) string {
	return pullQuery(facility, program, updatedAfter, page, pageSize, false)
}

func (v42) ListPath(facility, program, updatedAfter string, page, pageSize int) string {
	return pullQuery(facility, program, updatedAfter, page, pageSize, true)
}

func (v42) FetchPath(facility, program string, trackedEntities []string) string {
	q := pullValues(facility, program, false)
	withDeleted(q, program)
	q.Set("trackedEntities", strings.Join(trackedEntities, ","))
	q.Set("pageSize", strconv.Itoa(len(trackedEntities)))
	return "/api/tracker/trackedEntities?" + q.Encode()
}

func (v42) EventsPath(facility, program, updatedAfter, occurredAfter string, page, pageSize int) string {
	q := url.Values{"program": {program}, "orgUnit": {facility}, "orgUnitMode": {"SELECTED"}, "includeDeleted": {"true"},
		"fields": {"*"}, "page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(pageSize)}, "order": {"createdAt:asc"}}
	if updatedAfter != "" {
		q.Set("updatedAfter", updatedAfter)
	}
	if occurredAfter != "" {
		q.Set("occurredAfter", occurredAfter)
	}
	return "/api/tracker/events?" + q.Encode()
}

func (v42) OwnedPath(facility, program string, page, pageSize int) string {
	q := pullValues(facility, program, true)
	q.Set("fields", "trackedEntity")
	q.Set("page", strconv.Itoa(page))
	q.Set("pageSize", strconv.Itoa(pageSize))
	return "/api/tracker/trackedEntities?" + q.Encode()
}

func pullQuery(facility, program, updatedAfter string, page, pageSize int, idsOnly bool) string {
	q := pullValues(facility, program, idsOnly)
	withDeleted(q, program)
	if updatedAfter != "" {
		q.Set("updatedAfter", updatedAfter)
	}
	q.Set("page", strconv.Itoa(page))
	q.Set("pageSize", strconv.Itoa(pageSize))
	return "/api/tracker/trackedEntities?" + q.Encode()
}

// withDeleted asks for deletions too, as tombstones (deleted: true) for Android's
// download. Not for a type pull: 2.42 answers trackedEntityType with includeDeleted
// with broken SQL (409 E7145); a person deleted on central leaves a type listing at the
// weekly full pull instead.
func withDeleted(q url.Values, program string) {
	if !strings.HasPrefix(program, "tet:") {
		q.Set("includeDeleted", "true")
	}
}

func pullValues(facility, program string, idsOnly bool) url.Values {
	q := url.Values{}
	q.Set("orgUnits", facility)
	q.Set("orgUnitMode", "SELECTED")
	q.Set("fields", "*,enrollments[*,events[*]],relationships[*]")
	if idsOnly {
		q.Set("fields", "trackedEntity,updatedAt,enrollments[enrollment,updatedAt,events[event,updatedAt]],relationships[relationship,updatedAt]")
	}
	if tet, ok := strings.CutPrefix(program, "tet:"); ok {
		// People of this type registered at the facility, including those enrolled in
		// nothing the user can read. Without enrollments: through a type search central
		// also shows enrollments other facilities own.
		q.Set("trackedEntityType", tet)
		q.Set("fields", "*,!enrollments,!programOwners,relationships[*]")
		if idsOnly {
			q.Set("fields", "trackedEntity,updatedAt,relationships[relationship,updatedAt]")
		}
	} else {
		q.Set("program", program)
	}
	return q
}

func (v42) PullItems(body []byte, page, pageSize int) ([]tracker.M, bool, error) {
	var r struct {
		Pager *struct {
			NextPage string `json:"nextPage"`
		} `json:"pager"`
		TrackedEntities []tracker.M `json:"trackedEntities"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, false, err
	}
	more := r.Pager != nil && r.Pager.NextPage != "" && len(r.TrackedEntities) == pageSize
	return r.TrackedEntities, more, nil
}

// ReservePath: Android and the hub's pool use generateAndReserve (a bare JSON array,
// values valid 60 days by default). requestValues answers {} on 2.42.6.
func (v42) ReservePath(attr string, n int, orgUnitCode string) string {
	q := url.Values{}
	q.Set("numberToReserve", strconv.Itoa(n))
	if orgUnitCode != "" {
		q.Set("ORG_UNIT_CODE", orgUnitCode)
	}
	return "/api/trackedEntityAttributes/" + attr + "/generateAndReserve?" + q.Encode()
}

// AsyncAccepted: 200 WebMessage "Tracker job added" with response {id, location, responseType}.
func (v42) AsyncAccepted(jobID, location string) any {
	return map[string]any{"httpStatus": "OK", "httpStatusCode": 200, "status": "OK", "message": "Tracker job added",
		"response": map[string]any{"id": jobID, "location": location, "responseType": "TrackerJob"}}
}

// JobNotifications: newest first; the completed entry carries the summary message.
func (v42) JobNotifications(jobID, status string, s tracker.Stats, at string) any {
	return []any{map[string]any{"level": "INFO", "category": "TRACKER_IMPORT_JOB", "completed": true, "id": jobID,
		"uid": jobID, "time": at, "message": fmt.Sprintf("Import complete with status %s, %d created, %d updated, %d deleted, %d ignored",
			status, s.Created, s.Updated, s.Deleted, s.Ignored)}}
}

// ProgramFields: categoryCombo decides whether events carry an attribute option combo.
func (v42) ProgramFields(af string) string {
	return "id,name,trackedEntityType[id,trackedEntityTypeAttributes[" + af + "]],programStages[id,sortOrder,repeatable]," +
		"categoryCombo[id,isDefault],programTrackedEntityAttributes[" + af + "]"
}

// TransferOrgUnit: 2.42 names the new owner with ou=.
func (v42) TransferOrgUnit() string { return "ou" }
