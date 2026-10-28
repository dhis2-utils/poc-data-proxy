package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/bits"
	"slices"
	"strings"
	"sync"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/store"
)

// Index answers questions about the hub's tracker objects (spec 9.3). The objects
// live only in the store's data file: queries read its indexed columns and each
// object's slim JSON (slim.go), and rendering reads full payloads. An in-memory copy of
// every object took about 2 KB each, beyond a Pi at a busy facility's size
// (docs/PERFORMANCE.md, "How the hub scales"). What stays in memory is small:
// the visibility bitmaps and the pull scopes.
//
// Objects handed out are immutable: writers store a new version and call Put.
type Index struct {
	mu    sync.RWMutex
	vis   map[int64]bitmap // user -> visible central-origin slots (visibility.go)
	visMu sync.Mutex       // one visibility change at a time, taken before any transaction
	// slotMu guards nextSlot and pending: handing out a slot looks the object up in
	// the store, and readers of the bitmaps (ix.mu) should not wait on that.
	slotMu   sync.Mutex
	nextSlot uint32              // last slot handed out
	pending  map[string]uint32   // slots handed out to objects not yet stored
	scopes   map[string][]string // tracked entity -> pull scopes fetched in full (store.TEScopes)
	store    *store.Store
	payloads payloadCache  // decoded payloads, by version (payloads.go)
	pages    pagePositions // where each user's last page of a query ended (pagepos.go)
}

// NewStoreIndex is the index of a hub's store.
func NewStoreIndex(s *store.Store) *Index {
	return &Index{vis: map[int64]bitmap{}, pending: map[string]uint32{}, scopes: map[string][]string{}, store: s}
}

// HasScope reports whether the tracked entity was fetched in full for this pull scope.
func (ix *Index) HasScope(te, scope string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return slices.Contains(ix.scopes[te], scope)
}

// Scopes returns the pull scopes the tracked entity was fetched in full for.
func (ix *Index) Scopes(te string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return slices.Clone(ix.scopes[te])
}

// AddScopes records that these tracked entities were fetched in full for scope.
func (ix *Index) AddScopes(tes []string, scope string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, te := range tes {
		if !slices.Contains(ix.scopes[te], scope) {
			ix.scopes[te] = append(ix.scopes[te], scope)
		}
	}
}

// Load reads what the index keeps in memory from the store, which must be unlocked.
func (ix *Index) Load(s *store.Store) error {
	chunks, err := s.VisibilityChunks()
	if err != nil {
		return err
	}
	scopes, err := s.TEScopes()
	if err != nil {
		return err
	}
	vis := map[int64]bitmap{}
	for user, c := range chunks {
		vis[user] = bitmapFromChunks(c)
	}
	ix.visMu.Lock()
	ix.slotMu.Lock()
	ix.mu.Lock()
	ix.vis, ix.pending, ix.scopes, ix.store = vis, map[string]uint32{}, scopes, s
	ix.nextSlot = uint32(s.MaxSlot())
	ix.mu.Unlock()
	ix.slotMu.Unlock()
	ix.visMu.Unlock()
	ix.payloads.clear()
	ix.pages.clear()
	return nil
}

// Clear forgets what the index holds in memory (on lock or reset).
func (ix *Index) Clear() {
	ix.visMu.Lock()
	ix.slotMu.Lock()
	ix.mu.Lock()
	ix.vis, ix.pending, ix.scopes = map[int64]bitmap{}, map[string]uint32{}, map[string][]string{}
	ix.nextSlot = 0
	ix.mu.Unlock()
	ix.slotMu.Unlock()
	ix.visMu.Unlock()
	ix.payloads.clear()
	ix.pages.clear()
}

// FromStore converts a stored row: with its payload, a full object; without, the slim
// object its Slim column holds.
func FromStore(o *store.Object) (*Obj, error) {
	n := &Obj{UID: o.UID, Kind: o.Kind, TE: o.TE, Enrollment: o.Enrollment, Program: o.Program, Stage: o.Stage,
		TET: o.TET, OwnerOrgUnit: o.OwnerOrgUnit, UpdatedAt: o.UpdatedAt, Origin: o.Origin, SyncState: o.SyncState,
		Slot: uint32(o.Slot), Deleted: o.Deleted, Stub: o.Stub}
	raw := o.Payload
	if raw == nil {
		raw, n.slim = o.Slim, true
	}
	if len(raw) == 0 {
		n.Data = M{}
		return n, nil
	}
	if err := json.Unmarshal(raw, &n.Data); err != nil {
		return nil, err
	}
	return n, nil
}

func (o *Obj) ToStore() *store.Object {
	if o.slim {
		panic("tracker: storing the slim copy of " + o.UID + " would lose its payload; use Index.Full")
	}
	b, _ := json.Marshal(o.Data)
	slim, _ := json.Marshal(slimmed(o).Data)
	so := &store.Object{UID: o.UID, Kind: o.Kind, TE: o.TE, Enrollment: o.Enrollment, Program: o.Program,
		Stage: o.Stage, TET: o.TET, OwnerOrgUnit: o.OwnerOrgUnit, UpdatedAt: o.UpdatedAt, Origin: o.Origin,
		SyncState: o.SyncState, Payload: b, Slot: int64(o.Slot), Slim: slim,
		CreatedAt: Str(o.Data, "createdAt"), Status: Str(o.Data, "status")}
	switch o.Kind {
	case store.KindTrackedEntity:
		for _, a := range list(o.Data, "attributes") {
			so.Attrs = append(so.Attrs, store.Attr{Attr: Str(a, "attribute"), Value: fmt.Sprint(a["value"])})
		}
	case store.KindRelationship:
		so.RelFrom, so.RelTo = RelRef(asM(o.Data["from"])).UID, RelRef(asM(o.Data["to"])).UID
	}
	return so
}

// Put records that o was stored: the slot handed out for it is no longer pending.
func (ix *Index) Put(o *Obj) {
	ix.slotMu.Lock()
	defer ix.slotMu.Unlock()
	delete(ix.pending, o.UID)
}

// objects runs a query over the objects table whose columns are store.ObjectCols("").
func (ix *Index) objects(query string, args ...any) []*Obj {
	if ix.store == nil || !ix.store.Unlocked() {
		return nil
	}
	rows, err := ix.store.QueryObjects(query, args...)
	if err != nil {
		slog.Warn("index query", "err", err)
		return nil
	}
	out := make([]*Obj, 0, len(rows))
	for _, r := range rows {
		if o, err := FromStore(r); err == nil {
			out = append(out, o)
		}
	}
	return out
}

func (ix *Index) Get(uid string) *Obj {
	if uid == "" {
		return nil
	}
	if objs := ix.objects(`SELECT `+store.ObjectCols("")+` FROM objects WHERE uid = ?`, uid); len(objs) == 1 {
		return objs[0]
	}
	return nil
}

// GetMany returns these objects by uid (missing ones are left out).
func (ix *Index) GetMany(uids []string) map[string]*Obj {
	out := map[string]*Obj{}
	if ix.store == nil || !ix.store.Unlocked() || len(uids) == 0 {
		return out
	}
	rows, err := ix.store.ObjectsByUID(uids)
	if err != nil {
		slog.Warn("index query", "err", err)
		return out
	}
	for uid, r := range rows {
		if o, err := FromStore(r); err == nil {
			out[uid] = o
		}
	}
	return out
}

func (ix *Index) EnrollmentsOf(te string) []*Obj {
	return ix.objects(`SELECT `+store.ObjectCols("")+` FROM objects WHERE te_uid = ? AND kind = 'enrollment'`, te)
}

func (ix *Index) EventsOf(enrollment string) []*Obj {
	return ix.objects(`SELECT `+store.ObjectCols("")+` FROM objects WHERE enrollment_uid = ? AND kind = 'event'`, enrollment)
}

func (ix *Index) EventsOfTE(te string) []*Obj {
	return ix.objects(`SELECT `+store.ObjectCols("")+` FROM objects WHERE te_uid = ? AND kind = 'event'`, te)
}

func (ix *Index) RelationshipsOf(uid string) []*Obj {
	// Only relationships have ends: no kind condition, so both end indexes serve.
	return ix.objects(`SELECT `+store.ObjectCols("")+` FROM objects WHERE rel_from = ? OR rel_to = ?`, uid, uid)
}

// The same lookups, kept for the rest of one request: rendering a page asks for the
// same children several times (prefetch, attributes, the children themselves).
func (ix *Index) enrollmentsFor(v *Viewer, te string) []*Obj {
	return v.memo("enr:"+te, func() []*Obj { return ix.EnrollmentsOf(te) })
}

func (ix *Index) eventsFor(v *Viewer, enrollment string) []*Obj {
	return v.memo("ev:"+enrollment, func() []*Obj { return ix.EventsOf(enrollment) })
}

func (ix *Index) relationshipsFor(v *Viewer, uid string) []*Obj {
	return v.memo("rel:"+uid, func() []*Obj { return ix.RelationshipsOf(uid) })
}

// Len is how many objects the hub holds.
func (ix *Index) Len() int {
	if ix.store == nil || !ix.store.Unlocked() {
		return 0
	}
	return ix.store.CountKind("")
}

// AttributeValueTaken reports whether a tracked entity other than te holds this value
// of the attribute (unique attributes, E1064).
func (ix *Index) AttributeValueTaken(attr, value, te string) bool {
	if ix.store == nil || !ix.store.Unlocked() {
		return false
	}
	var n int
	ix.store.DB().QueryRow(`SELECT count(*) FROM te_attrs WHERE attr = ? AND value = ? AND te_uid <> ?`, attr, value, te).Scan(&n)
	return n > 0
}

// EnrolledTEs returns the tracked entities with a synced enrollment in program owned by
// owner (the ownership check).
func (ix *Index) EnrolledTEs(program, owner string) []string {
	return ix.enrolledTEs(sqlEnrolledOwned, program, owner)
}

// EnrolledElsewhere returns the tracked entities with a synced enrollment in program
// owned by another org unit than owner, or by none known: people the hub still holds,
// hidden, after a transfer out (the ownership check, which shows them again if central
// lists them as owner's).
func (ix *Index) EnrolledElsewhere(program, owner string) []string {
	return ix.enrolledTEs(sqlEnrolledElsewhere, program, owner)
}

const (
	sqlEnrolledOwned = `SELECT DISTINCT te_uid FROM objects WHERE kind = 'enrollment' AND program = ?
 AND owner_org_unit = ? AND sync_state = ?`
	sqlEnrolledElsewhere = `SELECT DISTINCT te_uid FROM objects WHERE kind = 'enrollment' AND program = ?
 AND owner_org_unit <> ? AND sync_state = ?`
)

func (ix *Index) enrolledTEs(q, program, owner string) []string {
	if ix.store == nil || !ix.store.Unlocked() {
		return nil
	}
	rows, err := ix.store.DB().Query(q, program, owner, store.SyncSynced)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var te string
		if rows.Scan(&te) == nil {
			out = append(out, te)
		}
	}
	return out
}

// VisibleSlots returns the slots visible to the user, from the bitmap in memory.
func (ix *Index) VisibleSlots(userID int64) []uint32 {
	var out []uint32
	for w, word := range ix.bitmapOf(userID) {
		for word != 0 {
			out = append(out, uint32(w)*64+uint32(bits.TrailingZeros64(word)))
			word &= word - 1
		}
	}
	return out
}

// Viewer is the user a read or write is evaluated for.
type Viewer struct {
	// AccessibleAsFacility serves orgUnitMode ACCESSIBLE/CAPTURE searches from the
	// facility's data offline instead of refusing them (Capture search and uniqueness
	// checks use ACCESSIBLE; FINDINGS.md).
	AccessibleAsFacility bool
	// SearchScope: the facility is in the user's search scope. Central-origin objects
	// carry central's own decision per user; for local objects the hub decides, and
	// requires this as well as the program and stage rules (review M9).
	SearchScope bool
	UserID      int64
	Access      *access.List
	Facility    string
	Catalog     *access.Catalog
	// IncludeDeleted: the request asked for includeDeleted=true (Android's download),
	// so tombstones are read too, marked deleted. Otherwise they are not there.
	IncludeDeleted bool
	// Ctx is the request's: a search stops when its client has gone, so abandoned
	// searches do not pile up on a slow machine (docs/INCREMENTAL-SYNC-FIX.md).
	Ctx context.Context

	payloads map[string]M      // full payloads loaded for this request (prefetch)
	related  map[string][]*Obj // children looked up for this request (enrollmentsFor and the like)
}

// gone is the answer to a search whose client has gone (nginx's code: nobody reads it).
var gone = &APIError{Status: 499, Message: "client closed request"}

func (v *Viewer) ctx() context.Context {
	if v == nil || v.Ctx == nil {
		return context.Background()
	}
	return v.Ctx
}

func (v *Viewer) memo(key string, load func() []*Obj) []*Obj {
	if v == nil {
		return load()
	}
	if objs, ok := v.related[key]; ok {
		return objs
	}
	if v.related == nil {
		v.related = map[string][]*Obj{}
	}
	objs := load()
	v.related[key] = objs
	return objs
}

// accessAllows checks a central-origin object against the user's current access list:
// central decided visibility at the last pull, but access lost since (refreshed at
// each sync and login) hides the object at once, before the next full pull removes it.
func accessAllows(v *Viewer, o *Obj) bool {
	switch o.Kind {
	case store.KindTrackedEntity:
		return o.TET == "" || v.Access.TET(o.TET).Read
	case store.KindEnrollment:
		return v.Access.Program(o.Program).Read
	case store.KindEvent:
		return v.Access.Program(o.Program).Read && v.Access.Stage(o.Stage).Read
	}
	return true
}

// CanRead applies spec 8.2.
func (ix *Index) CanRead(v *Viewer, o *Obj) bool {
	if o == nil || v.Access == nil || o.OwnerOrgUnit != v.Facility || (o.Deleted && !v.IncludeDeleted) || o.Stub {
		return false
	}
	if o.Origin == store.OriginCentral {
		return accessAllows(v, o) && ix.VisibleTo(o, v.UserID)
	}
	if !v.SearchScope || !ix.optionsReadable(v, o) {
		return false
	}
	switch o.Kind {
	case store.KindTrackedEntity:
		return v.Access.TET(o.TET).Read
	case store.KindEnrollment:
		return v.Access.Program(o.Program).Read
	case store.KindEvent:
		return v.Access.Program(o.Program).Read && v.Access.Stage(o.Stage).Read
	case store.KindRelationship:
		from, to := RelRef(asM(o.Data["from"])), RelRef(asM(o.Data["to"]))
		return ix.CanRead(v, ix.Get(from.UID)) && ix.CanRead(v, ix.Get(to.UID))
	}
	return false
}

// optionsReadable: an event or enrollment the hub holds under a non-default attribute
// option combination (written through the hub) is readable only with read access to
// each of its category options, as central decides. Central's own objects come with
// their visibility from the pull (listed per user for such programs).
func (ix *Index) optionsReadable(v *Viewer, o *Obj) bool {
	if (o.Kind != store.KindEvent && o.Kind != store.KindEnrollment) || v.Catalog == nil {
		return true
	}
	pm := v.Catalog.Programs[o.Program]
	if pm == nil || (o.Kind == store.KindEvent && !pm.AttributeCombo) ||
		(o.Kind == store.KindEnrollment && !pm.EnrollmentAttributeCombo) {
		return true
	}
	// Search rows carry their data encoded, or none when the query does not need it:
	// then read the object (only local objects of such programs come here).
	aoc := Str(withData(o).Data, "attributeOptionCombo")
	if aoc == "" && o.Data == nil && len(o.raw) == 0 {
		aoc = Str(ix.Full(o), "attributeOptionCombo")
	}
	if aoc == "" {
		return true
	}
	opts, ok := v.Catalog.AOCs[aoc]
	if !ok {
		return true // the default combination, or one of a program without a combination
	}
	return v.Access.MayReadOptions(opts)
}

// likePattern is a LIKE pattern for "contains s", with SQL's wildcards escaped.
func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(strings.ToLower(s)) + "%"
}

// Forget drops the caches after objects were removed from the store (pruning).
func (ix *Index) Forget() {
	ix.payloads.clear()
	ix.pages.clear()
}
