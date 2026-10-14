package tracker

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/store"
)

// TrackerType is the DHIS2 import report name for a kind.
func TrackerType(kind string) string {
	return map[string]string{store.KindTrackedEntity: "TRACKED_ENTITY", store.KindEnrollment: "ENROLLMENT",
		store.KindEvent: "EVENT", store.KindRelationship: "RELATIONSHIP"}[kind]
}

// ErrorReport mirrors one entry of validationReport.errorReports.
type ErrorReport struct {
	Message     string `json:"message"`
	ErrorCode   string `json:"errorCode"`
	TrackerType string `json:"trackerType"`
	UID         string `json:"uid"`
}

// Stats mirrors the import report stats object.
type Stats struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
	Ignored int `json:"ignored"`
	Total   int `json:"total"`
}

// ObjectResult is the outcome for one object.
type ObjectResult struct {
	Kind    string
	UID     string
	Created bool
	Errors  []ErrorReport
}

// ImportOutcome is the result of an offline import (spec 10.2).
type ImportOutcome struct {
	Results  []*ObjectResult
	Errors   []ErrorReport
	Accepted []*Obj
	Flat     M // accepted objects as a flat payload, for the outbox
	Refs     []store.OutboxRef
	// Parents are UIDs the accepted objects reference (used for depends_on_seq).
	Parents []string
	// Atomic is set when atomicMode=ALL rejected everything because of an error.
	Atomic bool
}

func (o *ImportOutcome) Stats() (total Stats, byType map[string]Stats) {
	byType = map[string]Stats{}
	for _, r := range o.Results {
		s := byType[r.Kind]
		switch {
		case len(r.Errors) > 0 || o.Atomic:
			s.Ignored++
		case r.Created:
			s.Created++
		default:
			s.Updated++
		}
		s.Total++
		byType[r.Kind] = s
	}
	for _, s := range byType {
		total.Created += s.Created
		total.Updated += s.Updated
		total.Ignored += s.Ignored
		total.Total += s.Total
	}
	return
}

// Importer validates a payload against the index for one user.
type Importer struct {
	Ix         *Index
	V          *Viewer
	Username   string
	UserUID    string
	DefaultAOC string // default attribute option combo UID
	Strategy   string // CREATE, UPDATE, CREATE_AND_UPDATE
	// AtomicAll rejects the whole payload if any object fails (atomicMode=ALL, central's
	// default). Accepting part of it left orphan tracked entities (FINDINGS.md).
	AtomicAll   bool
	Now         time.Time
	pending     map[string]*Obj // accepted in this payload
	rejected    map[string]bool
	outcome     *ImportOutcome
	parentsSeen map[string]bool
}

// Import validates every object, accepting what passes (atomicMode=OBJECT behaviour).
// Nothing is written; the caller persists Accepted.
func (im *Importer) Import(payload M) *ImportOutcome {
	im.pending, im.rejected, im.parentsSeen = map[string]*Obj{}, map[string]bool{}, map[string]bool{}
	im.outcome = &ImportOutcome{Flat: M{"trackedEntities": []any{}, "enrollments": []any{}, "events": []any{}, "relationships": []any{}}}
	if im.Strategy == "" {
		im.Strategy = "CREATE_AND_UPDATE"
	}
	AssignUIDs(payload)
	for _, it := range Flatten(payload) {
		res := &ObjectResult{Kind: it.Kind, UID: it.UID}
		im.outcome.Results = append(im.outcome.Results, res)
		obj, created, errs := im.validate(it)
		if len(errs) > 0 {
			res.Errors = errs
			im.outcome.Errors = append(im.outcome.Errors, errs...)
			if it.UID != "" {
				im.rejected[it.UID] = true
			}
			continue
		}
		res.Created = created
		im.pending[obj.UID] = obj
		im.outcome.Accepted = append(im.outcome.Accepted, obj)
		im.outcome.Refs = append(im.outcome.Refs, store.OutboxRef{UID: obj.UID, Kind: obj.Kind})
		key := map[string]string{store.KindTrackedEntity: "trackedEntities", store.KindEnrollment: "enrollments",
			store.KindEvent: "events", store.KindRelationship: "relationships"}[it.Kind]
		im.outcome.Flat[key] = append(im.outcome.Flat[key].([]any), it.Data)
	}
	for p := range im.parentsSeen {
		im.outcome.Parents = append(im.outcome.Parents, p)
	}
	if im.AtomicAll && len(im.outcome.Errors) > 0 && len(im.outcome.Accepted) > 0 {
		im.outcome.Accepted, im.outcome.Refs, im.outcome.Parents = nil, nil, nil
		im.outcome.Flat = M{"trackedEntities": []any{}, "enrollments": []any{}, "events": []any{}, "relationships": []any{}}
		im.outcome.Atomic = true
	}
	return im.outcome
}

func (im *Importer) errf(it *Item, code, format string, a ...any) []ErrorReport {
	return []ErrorReport{{Message: fmt.Sprintf(format, a...), ErrorCode: code, TrackerType: TrackerType(it.Kind), UID: it.UID}}
}

// resolve finds a referenced object in this payload or in the index (if readable).
// It returns (nil, true) when the parent was rejected earlier in this payload.
func (im *Importer) resolve(uid string) (o *Obj, parentRejected bool) {
	if im.rejected[uid] {
		return nil, true
	}
	if p := im.pending[uid]; p != nil {
		return p, false
	}
	if o := im.Ix.Get(uid); im.Ix.CanRead(im.V, o) {
		im.parentsSeen[uid] = true
		return o, false
	}
	return nil, false
}

func (im *Importer) existing(it *Item) (*Obj, []ErrorReport) {
	old := im.pending[it.UID]
	if old == nil {
		old = im.Ix.Get(it.UID)
	}
	if old != nil && old.Kind != it.Kind {
		return nil, im.errf(it, "E1048", "Object: `%s`, uid: `%s`, is already used by another object", TrackerType(it.Kind), it.UID)
	}
	switch {
	case old != nil && !im.Ix.CanRead(im.V, old) && im.pending[it.UID] == nil:
		return nil, im.errf(it, "E1000", "needs connection: %s `%s` is not available offline", it.Kind, it.UID)
	case old != nil && im.Strategy == "CREATE":
		return nil, im.errf(it, "E1030", "%s `%s` already exists", it.Kind, it.UID)
	case old == nil && im.Strategy == "UPDATE":
		return nil, im.errf(it, "E1063", "%s `%s` does not exist", it.Kind, it.UID)
	}
	if old != nil {
		im.parentsSeen[it.UID] = true
	}
	return old, nil
}

func (im *Importer) newObj(it *Item, old *Obj) *Obj {
	now := DHIS2Time(im.Now)
	data := it.Data
	if old != nil {
		full := im.Ix.Full(old)
		data = Merge(clone(full), it.Data)
		// Who created an object is not the client's to change (review L3).
		for _, k := range []string{"createdAt", "createdBy"} {
			if v, ok := full[k]; ok {
				data[k] = v
			} else {
				delete(data, k)
			}
		}
	} else {
		data = clone(it.Data)
		data["createdAt"] = now
		data["createdBy"] = M{"uid": im.UserUID, "username": im.Username}
	}
	data["updatedAt"] = now
	data["updatedBy"] = M{"uid": im.UserUID, "username": im.Username}
	im.normalise(it.Kind, data)
	o := &Obj{UID: it.UID, Kind: it.Kind, OwnerOrgUnit: im.V.Facility, UpdatedAt: now, Origin: store.OriginLocal,
		SyncState: store.SyncPending, Data: data}
	if old != nil {
		o.Slot = old.Slot
	}
	return o
}

func (im *Importer) validate(it *Item) (*Obj, bool, []ErrorReport) {
	if it.UID == "" || !ValidUID(it.UID) {
		return nil, false, im.errf(it, "E1048", "Object: `%s`, uid: `%s`, has an invalid uid format (offline imports require client-generated UIDs)",
			TrackerType(it.Kind), it.UID)
	}
	old, errs := im.existing(it)
	if errs != nil {
		return nil, false, errs
	}
	switch it.Kind {
	case store.KindTrackedEntity:
		return im.validateTE(it, old)
	case store.KindEnrollment:
		return im.validateEnrollment(it, old)
	case store.KindEvent:
		return im.validateEvent(it, old)
	default:
		return im.validateRelationship(it, old)
	}
}

func (im *Importer) orgUnitOK(it *Item, old *Obj) []ErrorReport {
	ou := Str(it.Data, "orgUnit")
	if ou == "" && old != nil {
		return nil
	}
	if ou != im.V.Facility {
		return im.errf(it, "E1000", "needs connection: User: `%s`, has no write access to OrganisationUnit: `%s` (only the hub facility is available offline)",
			im.UserUID, ou)
	}
	return nil
}

func (im *Importer) validateTE(it *Item, old *Obj) (*Obj, bool, []ErrorReport) {
	tet := Str(it.Data, "trackedEntityType")
	if tet == "" && old != nil {
		tet = old.TET
	}
	if tet == "" {
		return nil, false, im.errf(it, "E1121", "Missing required tracked entity property: `trackedEntityType`.")
	}
	if old != nil && tet != old.TET {
		return nil, false, im.errf(it, "E1126", "Not allowed to update Tracked Entity property: trackedEntityType.")
	}
	if errs := im.orgUnitOK(it, old); errs != nil {
		return nil, false, errs
	}
	if !im.V.Access.TET(tet).Write {
		return nil, false, im.errf(it, "E1001", "User: `%s`, has no data write access to tracked entity type: `%s`.", im.UserUID, tet)
	}
	if errs := im.checkUnique(it); errs != nil {
		return nil, false, errs
	}
	o := im.newObj(it, old)
	o.TET = tet
	return o, old == nil, nil
}

// checkUnique rejects values of unique attributes that already exist in the store.
// Central does the authoritative check on push (spec 13.3 scenario 7).
func (im *Importer) checkUnique(it *Item) []ErrorReport {
	if im.V.Catalog == nil {
		return nil
	}
	unique := map[string]bool{}
	for _, as := range im.V.Catalog.TETAttributes {
		for _, a := range as {
			if a.Unique {
				unique[a.ID] = true
			}
		}
	}
	for _, p := range im.V.Catalog.Programs {
		for _, a := range p.Attributes {
			if a.Unique {
				unique[a.ID] = true
			}
		}
	}
	for attr, val := range attrValues(it.Data, "attributes", "attribute") {
		if !unique[attr] || val == "" || val == "<nil>" {
			continue
		}
		if im.Ix.AttributeValueTaken(attr, val, it.UID) {
			return im.errf(it, "E1064", "Non-unique attribute value `%s` for attribute `%s`", val, attr)
		}
	}
	return nil
}

func (im *Importer) validateEnrollment(it *Item, old *Obj) (*Obj, bool, []ErrorReport) {
	program := Str(it.Data, "program")
	if program == "" && old != nil {
		program = old.Program
	}
	te := it.TE
	if te == "" && old != nil {
		te = old.TE
	}
	pm := im.V.Catalog.Programs[program]
	if pm == nil {
		return nil, false, im.errf(it, "E1069", "needs connection: program `%s` is not cached on this hub", program)
	}
	if aoc := Str(it.Data, "attributeOptionCombo"); (aoc != "" && aoc != im.DefaultAOC) || pm.EnrollmentAttributeCombo {
		// Central (2.43) checks access to the combo's category options (E1099), which
		// the hub cannot offline: say so now instead of on push.
		return nil, false, im.errf(it, "E1055", "needs connection: enrollments with an attribute option combo are not supported offline")
	}
	parent, rej := im.resolve(te)
	if rej {
		return nil, false, im.errf(it, "E5000", "%s `%s` cannot be persisted because %s `%s` referenced by it cannot be persisted.",
			TrackerType(it.Kind), it.UID, "TRACKED_ENTITY", te)
	}
	if parent == nil || parent.Kind != store.KindTrackedEntity {
		return nil, false, im.errf(it, "E1068", "needs connection: Could not find TrackedEntity: `%s`, linked to Enrollment.", te)
	}
	if parent.TET != pm.TET {
		return nil, false, im.errf(it, "E1022", "TrackedEntity: `%s`, must have same TrackedEntityType as Program `%s`.", te, program)
	}
	if errs := im.orgUnitOK(it, old); errs != nil {
		return nil, false, errs
	}
	if !im.V.Access.Program(program).Write {
		return nil, false, im.errf(it, "E1091", "User: `%s`, has no data write access to Program: `%s`.", im.UserUID, program)
	}
	if !im.V.Access.TET(parent.TET).Read {
		return nil, false, im.errf(it, "E1104", "User: `%s`, has no data read access to program: `%s`, TrackedEntityType: `%s`.", im.UserUID, program, parent.TET)
	}
	o := im.newObj(it, old)
	o.TE, o.Program = te, program
	if Str(o.Data, "status") == "" {
		o.Data["status"] = "ACTIVE"
	}
	o.Data["trackedEntity"], o.Data["program"] = te, program
	return o, old == nil, nil
}

func (im *Importer) validateEvent(it *Item, old *Obj) (*Obj, bool, []ErrorReport) {
	stage := Str(it.Data, "programStage")
	if stage == "" && old != nil {
		stage = old.Stage
	}
	program := im.V.Catalog.StageProgram[stage]
	if program == "" {
		return nil, false, im.errf(it, "E1013", "needs connection: program stage `%s` is not cached on this hub", stage)
	}
	if p := Str(it.Data, "program"); p != "" && p != program {
		return nil, false, im.errf(it, "E1079", "Event: `%s`, program: `%s` is different from program defined in program stage: `%s`.", it.UID, p, stage)
	}
	if aoc := Str(it.Data, "attributeOptionCombo"); aoc != "" && aoc != im.DefaultAOC {
		return nil, false, im.errf(it, "E1055", "needs connection: events with a non-default attribute option combo are not supported offline")
	}
	if p := im.V.Catalog.Programs[program]; p != nil && p.AttributeCombo {
		// Central requires a non-default attribute option combo here (E1055), and
		// those are not supported offline: say so now instead of on push.
		return nil, false, im.errf(it, "E1055", "needs connection: events in a program with an attribute category combination are not supported offline")
	}
	enr := it.Enrollment
	if enr == "" && old != nil {
		enr = old.Enrollment
	}
	parent, rej := im.resolve(enr)
	if rej {
		return nil, false, im.errf(it, "E5000", "%s `%s` cannot be persisted because %s `%s` referenced by it cannot be persisted.",
			TrackerType(it.Kind), it.UID, "ENROLLMENT", enr)
	}
	if parent == nil || parent.Kind != store.KindEnrollment {
		return nil, false, im.errf(it, "E1033", "needs connection: Event: `%s`, Enrollment: `%s` not found.", it.UID, enr)
	}
	if parent.Program != program {
		return nil, false, im.errf(it, "E1079", "Event: `%s`, program: `%s` is different from enrollment program: `%s`.", it.UID, program, parent.Program)
	}
	if errs := im.orgUnitOK(it, old); errs != nil {
		return nil, false, errs
	}
	// An event in a tracker program needs write access to its stage (E1095, see
	// testdata/2.42/tracker_post_access_denied.json), not to the program: central
	// accepts it from a user who may only read the program's data (the write oracle,
	// synthetic profile). Reading the enrollment above needs program read access.
	if !im.V.Access.Stage(stage).Write {
		return nil, false, im.errf(it, "E1095", "User: `%s` has no data write access to ProgramStage: `%s`.", im.UserUID, stage)
	}
	if old == nil && im.stageTaken(it.UID, enr, program, stage) {
		return nil, false, im.errf(it, "E1039", "ProgramStage: `%s`, is not repeatable and an event already exists.", stage)
	}
	o := im.newObj(it, old)
	o.TE, o.Enrollment, o.Program, o.Stage = parent.TE, enr, program, stage
	o.Data["program"], o.Data["programStage"], o.Data["enrollment"], o.Data["trackedEntity"] = program, stage, enr, parent.TE
	if Str(o.Data, "status") == "" {
		o.Data["status"] = "ACTIVE"
	}
	return o, old == nil, nil
}

// stageTaken reports whether a new event would be a second one in a non-repeatable
// stage of the enrollment, counting events the hub holds and earlier ones in this
// payload, as central does (E1039).
func (im *Importer) stageTaken(uid, enr, program, stage string) bool {
	p := im.V.Catalog.Programs[program]
	if p == nil || !slices.Contains(p.NonRepeatable, stage) {
		return false
	}
	for _, ev := range im.Ix.EventsOf(enr) {
		if ev.UID != uid && ev.Stage == stage && ev.Data["deleted"] != true {
			return true
		}
	}
	for _, ev := range im.pending {
		if ev.UID != uid && ev.Kind == store.KindEvent && ev.Enrollment == enr && ev.Stage == stage {
			return true
		}
	}
	return false
}

func (im *Importer) validateRelationship(it *Item, old *Obj) (*Obj, bool, []ErrorReport) {
	if Str(it.Data, "relationshipType") == "" {
		return nil, false, im.errf(it, "E1124", "Missing required relationship property: `relationshipType`.")
	}
	for _, ref := range []Ref{it.From, it.To} {
		p, rej := im.resolve(ref.UID)
		if rej {
			return nil, false, im.errf(it, "E5000", "%s `%s` cannot be persisted because %s `%s` referenced by it cannot be persisted.",
				"RELATIONSHIP", it.UID, strings.ToUpper(ref.Kind), ref.UID)
		}
		if p == nil {
			return nil, false, im.errf(it, "E4012", "needs connection: Could not find `%s`: `%s`, linked to Relationship.", ref.Kind, ref.UID)
		}
	}
	o := im.newObj(it, old)
	return o, old == nil, nil
}

// normalise gives locally written objects the fields central always returns; Capture
// crashes on an event without dataValues or notes.
func (im *Importer) normalise(kind string, d M) {
	def := func(k string, v any) {
		if _, ok := d[k]; !ok || d[k] == nil {
			d[k] = v
		}
	}
	def("deleted", false)
	for _, k := range []string{"occurredAt", "scheduledAt", "enrolledAt", "completedAt", "createdAtClient", "updatedAtClient"} {
		if v, ok := d[k].(string); ok && v != "" {
			d[k] = NormaliseDate(v)
		}
	}
	switch kind {
	case store.KindTrackedEntity:
		def("attributes", []any{})
		def("inactive", false)
		def("potentialDuplicate", false)
	case store.KindEnrollment:
		def("attributes", []any{})
		def("notes", []any{})
		def("followUp", false)
	case store.KindEvent:
		def("dataValues", []any{})
		def("notes", []any{})
		def("followUp", false)
		if Str(d, "attributeOptionCombo") == "" && im.DefaultAOC != "" {
			d["attributeOptionCombo"] = im.DefaultAOC
		}
		if s, ok := d["attributeCategoryOptions"].(string); ok && s == "" {
			delete(d, "attributeCategoryOptions")
		}
	}
}

// NormaliseDate turns the date forms clients send ("2026-10-07", "2026-10-07T08:30:00Z")
// into central's "2026-10-07T00:00:00.000". The Android SDK cannot parse a bare date.
func NormaliseDate(v string) string {
	t, ok := parseTime(v)
	if !ok {
		return v
	}
	return t.Format("2006-01-02T15:04:05.000")
}
