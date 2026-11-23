package trackerapi

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/tracker"
)

// noteRe is Capture web's request adding a note to an existing event or enrollment.
var noteRe = regexp.MustCompile(`^/api/tracker/(events|enrollments)/([A-Za-z0-9]{11})/note$`)

// serveNote takes a note on an existing event or enrollment while the hub answers
// itself (PENDING.md, item 24): the note shows at once on the hub's copy, and the push
// makes Capture's request again as the author (sync/notes.go). The answer is
// central's: the note's UID and value. As on central, a note UID the record already
// has is refused (400, E1003), and a record the user cannot read is not found (404).
func (a *API) serveNote(w http.ResponseWriter, sess *core.Session, v *tracker.Viewer, collection, uid string, body []byte) {
	var in struct {
		Note  string `json:"note"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		a.H.WebError(w, 400, "Invalid JSON payload: "+err.Error(), "")
		return
	}
	if strings.TrimSpace(in.Value) == "" {
		a.H.WebError(w, 400, "Note value is required.", "")
		return
	}
	kind := map[string]string{"events": store.KindEvent, "enrollments": store.KindEnrollment}[collection]
	o, aerr := a.H.Index.NoteTarget(v, kind, uid)
	if aerr != nil {
		a.H.WebError(w, aerr.Status, aerr.Message, aerr.Code)
		return
	}
	if in.Note == "" {
		in.Note = tracker.NewUID()
	} else if !tracker.ValidUID(in.Note) {
		a.H.WebError(w, 400, "Invalid note UID: "+in.Note, "")
		return
	}
	notes, _ := a.H.Index.Full(o)["notes"].([]any)
	if slices.ContainsFunc(notes, func(n any) bool { m, _ := n.(tracker.M); return tracker.Str(m, "note") == in.Note }) {
		a.H.WebError(w, 400, "Note `"+in.Note+"` already exists.", "E1003")
		return
	}
	u, _ := a.H.Store.UserByID(sess.UserID)
	note := tracker.M{"note": in.Note, "value": in.Value, "storedAt": tracker.DHIS2Time(a.H.CentralNow()),
		"storedBy": u.Username, "createdBy": tracker.M{"uid": u.UserUID, "username": u.Username}}
	if _, err := a.H.PersistNote(sess.UserID, o, collection, note); err != nil {
		a.H.WebError(w, 500, err.Error(), "")
		return
	}
	core.WriteJSON(w, 200, tracker.M{"note": in.Note, "value": in.Value})
}
