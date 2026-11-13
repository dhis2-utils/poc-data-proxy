// Package catalog writes the generated part of docs/SUPPORTED.md, what the hub
// answers offline, from the tables the hub itself decides by, so the document cannot
// drift from the code: TestSupportedDocUpToDate fails when it does
// (go test ./internal/catalog -update rewrites it).
package catalog

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dhis2/facility-hub/internal/aggregateapi"
	"github.com/dhis2/facility-hub/internal/proxy"
	"github.com/dhis2/facility-hub/internal/tracker"
	"github.com/dhis2/facility-hub/internal/trackerapi"
)

func code(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "`" + n + "`"
	}
	return strings.Join(out, ", ")
}

// Appendix is the generated part of docs/SUPPORTED.md.
func Appendix() string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	params, ignored := tracker.OfflineParams()

	w("### Tracker reads\n\n")
	w("Answered offline from the hub's store, for the facility's data. The API version may be in the path (`/api/42/tracker/…`). Any other parameter gets 400 \"not supported offline: <name>\" and is counted on the hub's endpoints page. Ignored, as central ignores it: %s.\n\n", code(ignored))
	w("| Endpoint | Parameters accepted offline |\n| --- | --- |\n")
	for _, c := range []string{"trackedEntities", "enrollments", "events", "relationships"} {
		w("| `GET /api/tracker/%s` | %s |\n", c, code(params[c]))
	}
	w("| `GET /api/tracker/{trackedEntities,enrollments,events,relationships}/{uid}` | %s |\n\n", code(params["single"]))
	w("`orgUnitMode` offline: `SELECTED` with the facility; `DESCENDANTS` or `CHILDREN` of the facility; `ACCESSIBLE` or `CAPTURE` only for a user whose scope is the facility alone. Other modes and other org units: \"needs connection\".\n\n")

	w("### Tracker writes\n\n")
	w("`POST /api/tracker` is stored on the hub and queued for central, sent later as its author. Parameters accepted offline: %s; `importStrategy` `CREATE`, `UPDATE` or `CREATE_AND_UPDATE` (not `DELETE`), `importMode` `COMMIT` only, `idScheme` `UID` only. `GET /api/tracker/jobs/{id}` and `…/report` answer an offline import made with `async=true`.\n\n", code(trackerapi.ImportFlags()))

	w("### Aggregate data entry\n\n")
	routes := slices.Sorted(slices.Values(aggregateapi.OfflineRoutes))
	w("Answered offline from the hub's copy of each user's forms: %s. Other methods on these paths (file values, min-max values) get 409 \"needs connection\".\n\n", code(routes))

	w("### Everything else: copies of what was opened online\n\n")
	w("Other reads (metadata, settings, the apps menu, data store entries) are answered offline from the user's own copy of central's answer to the same request: one they made online through the hub, or one the hub loaded for them at their online login because someone at the facility opened it online in the last 14 days. A request nobody opened online gets \"needs connection\".\n\n")
	w("Never loaded at login (the first part of the path after `/api/`): %s; also `/api/me` (kept at login), `generate` and `requestValues` (they reserve values), and requests naming another user.\n\n", code(proxy.NotLoadedAtLogin()))
	w("Shared between users, since central answers anyone the same: the login page and its files (%s, kept in the public file and served even while the hub is locked), %s under `/api/`, and the apps' static files.\n\n", code(proxy.LoginPaths()), code(proxy.SharedAnswers()))
	w("Always \"needs connection\" offline: central's validation (`/api/validation/…`), and every other write (`POST`, `PUT`, `PATCH`, `DELETE`), data store entries included.\n")
	return b.String()
}
