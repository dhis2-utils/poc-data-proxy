package tracker

import "github.com/dhis2/facility-hub/internal/store"

// NoteTarget is the stored event or enrollment uid that v adds a note to offline, as
// Capture web does with POST /api/tracker/{events|enrollments}/{uid}/note (PENDING.md,
// item 24). Central answers 404 with E1005 for a record it does not have; the hub does
// the same for one the user cannot read, as for a read. Adding a note needs the write
// access an update of the record needs.
func (ix *Index) NoteTarget(v *Viewer, kind, uid string) (*Obj, *APIError) {
	o := ix.Get(uid)
	if o == nil || o.Kind != kind || !ix.CanRead(v, o) {
		name := map[string]string{store.KindEvent: "Event", store.KindEnrollment: "Enrollment"}[kind]
		return nil, &APIError{Status: 404, Code: "E1005", Message: name + " with id " + uid + " could not be found."}
	}
	if !v.Access.Program(o.Program).Write {
		return nil, &APIError{Status: 403, Code: "E1091",
			Message: "User has no data write access to Program: `" + o.Program + "`."}
	}
	if kind == store.KindEvent && o.Stage != "" && !v.Access.Stage(o.Stage).Write {
		return nil, &APIError{Status: 403, Code: "E1095",
			Message: "User has no data write access to ProgramStage: `" + o.Stage + "`."}
	}
	return o, nil
}
