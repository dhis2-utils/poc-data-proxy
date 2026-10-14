// Package compat keeps all DHIS2-version-specific request and response handling in
// one place per version (spec 3, 12a). Code elsewhere asks For(version) for an API.
package compat

import (
	"strconv"
	"strings"

	"github.com/dhis2/facility-hub/internal/tracker"
)

// API is what the hub needs to speak one DHIS2 major version.
type API interface {
	Major() int
	// ListResponse renders a tracker collection page.
	ListResponse(r *tracker.Result, selfURL string) any
	// ImportReport renders an offline import report and its HTTP status.
	ImportReport(o *tracker.ImportOutcome) (int, any)
	// WebMessage renders an error response.
	WebMessage(status int, message, errorCode string) any
	// TrackerResult reads central's synchronous import report: which object UIDs were
	// persisted, and the error reports per rejected UID. An object in neither set was
	// not persisted because something it depends on failed.
	TrackerResult(status int, body []byte) (persisted map[string]bool, errs map[string][]tracker.ErrorReport, err error)
	// NotFound renders the 404 for a single-object tracker GET.
	NotFound(kind, uid string) any
	// LoginSuccess renders the hub's /api/auth/login answer for offline login.
	LoginSuccess(redirectURL string) any
	LoginFailure() (int, any)
	// PATRequest is the body for POST /api/apiToken; PATResponse extracts uid and key.
	PATRequest(expiresAtMillis int64) any
	PATResponse(body []byte) (uid, key string, err error)
	// PullPath is the tracked entity pull URL for one user and program (spec 11.3).
	PullPath(facility, program, updatedAfter string, page, pageSize int) string
	// ListPath is the same query returning only identifiers and update times of the
	// tracked entities, enrollments, events and relationships (the ID-list pull).
	ListPath(facility, program, updatedAfter string, page, pageSize int) string
	// FetchPath returns these tracked entities in full, as PullPath would.
	FetchPath(facility, program string, trackedEntities []string) string
	// OwnedPath lists the identifiers of the tracked entities the facility owns in the
	// program (the ownership check).
	OwnedPath(facility, program string, page, pageSize int) string
	// PullItems extracts the tracked entities and whether another page exists.
	PullItems(body []byte, page, pageSize int) (items []tracker.M, more bool, err error)
	// AsyncAccepted is the answer to POST /api/tracker?async=true; JobNotifications
	// is GET /api/tracker/jobs/{id} for a finished job (the hub imports synchronously).
	AsyncAccepted(jobID, location string) any
	JobNotifications(jobID, status string, s tracker.Stats, at string) any
	// ReservePath reserves n values of a generated attribute on central (spec 10.3).
	ReservePath(attr string, n int, orgUnitCode string) string
	// TransferOrgUnit is the parameter of PUT /api/tracker/ownership/transfer that
	// names the new owner.
	TransferOrgUnit() string
	// ProgramFields is the field selection for the facility's program catalog;
	// attrFields selects each tracked entity attribute.
	ProgramFields(attrFields string) string
}

// Supported lists the DHIS2 majors this hub release handles (current and next).
var registry = map[int]API{
	42: v42{},
	// 2.43: the 2.42 shapes, with the differences the live suite found on 2.43 (v43.go).
	43: v43{},
}

// ParseMajor returns 42 for "2.42.6", "2.42-SNAPSHOT" or "42".
func ParseMajor(version string) int {
	parts := strings.Split(version, ".")
	if len(parts) >= 2 && parts[0] == "2" {
		n, _ := strconv.Atoi(strings.TrimFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
		return n
	}
	n, _ := strconv.Atoi(parts[0])
	return n
}

// For returns the API for a server version string, or nil if unsupported.
func For(version string) API { return registry[ParseMajor(version)] }

func SupportedMajors() []int { return []int{42, 43} }
