package tracker

import (
	"encoding/json"
	"slices"
	"sync"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/store"
)

// Index is the in-memory view of all objects, built on unlock (spec 9.3). Objects are
// immutable once in the index: writers replace them, so readers can hold pointers.
type Index struct {
	mu    sync.RWMutex
	objs  map[string]*Obj
	byKey map[string]map[string]bool // "enr:<te>", "ev:<enr>", "evte:<te>", "rel:<uid>" -> uids

	vis      map[int64]bitmap    // user -> visible central-origin slots (visibility.go)
	visMu    sync.Mutex          // one visibility change at a time, taken before any transaction
	nextSlot uint32              // last slot handed out
	pending  map[string]uint32   // slots handed out to objects not yet in the index
	scopes   map[string][]string // tracked entity -> pull scopes fetched in full (store.TEScopes)
	store    *store.Store        // set by Load: objects are then held slim (slim.go)
	results  resultCache         // sorted results kept for later pages (results.go)
}

// NewStoreIndex is the index of a hub's store: objects are held slim (slim.go) from
// the first one, whether the store was unlocked by setup or by a login.
func NewStoreIndex(s *store.Store) *Index {
	ix := NewIndex()
	ix.store = s
	return ix
}

func NewIndex() *Index {
	return &Index{objs: map[string]*Obj{}, byKey: map[string]map[string]bool{}, vis: map[int64]bitmap{},
		pending: map[string]uint32{}, scopes: map[string][]string{}}
}

// HasScope reports whether the tracked entity was fetched in full for this pull scope.
func (ix *Index) HasScope(te, scope string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return slices.Contains(ix.scopes[te], scope)
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

// Load rebuilds the index from the store. The store must be unlocked.
func (ix *Index) Load(s *store.Store) error {
	chunks, err := s.VisibilityChunks()
	if err != nil {
		return err
	}
	fresh := NewIndex()
	fresh.store = s
	if fresh.scopes, err = s.TEScopes(); err != nil {
		return err
	}
	for user, c := range chunks {
		fresh.vis[user] = bitmapFromChunks(c)
	}
	err = s.AllObjects(func(o *store.Object) error {
		obj, err := FromStore(o)
		if err != nil {
			return err
		}
		fresh.put(obj)
		return nil
	})
	if err != nil {
		return err
	}
	ix.visMu.Lock()
	ix.mu.Lock()
	ix.objs, ix.byKey, ix.vis, ix.pending, ix.scopes = fresh.objs, fresh.byKey, fresh.vis, fresh.pending, fresh.scopes
	ix.store = s
	ix.nextSlot = uint32(s.MaxSlot())
	ix.mu.Unlock()
	ix.visMu.Unlock()
	ix.clearResults()
	return nil
}

// Clear empties the index (on lock or reset).
func (ix *Index) Clear() {
	ix.visMu.Lock()
	ix.mu.Lock()
	ix.objs, ix.byKey, ix.vis, ix.pending = map[string]*Obj{}, map[string]map[string]bool{}, map[int64]bitmap{}, map[string]uint32{}
	ix.scopes = map[string][]string{}
	ix.nextSlot = 0
	ix.mu.Unlock()
	ix.visMu.Unlock()
	ix.clearResults()
}

func FromStore(o *store.Object) (*Obj, error) {
	var data M
	if err := json.Unmarshal(o.Payload, &data); err != nil {
		return nil, err
	}
	return &Obj{UID: o.UID, Kind: o.Kind, TE: o.TE, Enrollment: o.Enrollment, Program: o.Program, Stage: o.Stage,
		TET: o.TET, OwnerOrgUnit: o.OwnerOrgUnit, UpdatedAt: o.UpdatedAt, Origin: o.Origin, SyncState: o.SyncState,
		Data: data, Slot: uint32(o.Slot)}, nil
}

func (o *Obj) ToStore() *store.Object {
	if o.slim {
		panic("tracker: storing the slim copy of " + o.UID + " would lose its payload; use Index.Full")
	}
	b, _ := json.Marshal(o.Data)
	return &store.Object{UID: o.UID, Kind: o.Kind, TE: o.TE, Enrollment: o.Enrollment, Program: o.Program,
		Stage: o.Stage, TET: o.TET, OwnerOrgUnit: o.OwnerOrgUnit, UpdatedAt: o.UpdatedAt, Origin: o.Origin,
		SyncState: o.SyncState, Payload: b, Slot: int64(o.Slot)}
}

// keys are the lists an object is on: its parents', and for queries its type's or
// program's ("tet:", "penr:", "pev:"), so a search starts from candidates instead of
// every object.
func (ix *Index) keys(o *Obj) []string {
	switch o.Kind {
	case store.KindTrackedEntity:
		return []string{"tet:" + o.TET}
	case store.KindEnrollment:
		return []string{"enr:" + o.TE, "penr:" + o.Program}
	case store.KindEvent:
		return []string{"ev:" + o.Enrollment, "evte:" + o.TE, "pev:" + o.Program}
	case store.KindRelationship:
		from, to := RelRef(asM(o.Data["from"])), RelRef(asM(o.Data["to"]))
		return []string{"rel:" + from.UID, "rel:" + to.UID}
	}
	return nil
}

func (ix *Index) put(o *Obj) {
	if ix.store != nil && !o.slim {
		o = slimmed(o)
	}
	if old := ix.objs[o.UID]; old != nil {
		for _, k := range ix.keys(old) {
			delete(ix.byKey[k], old.UID)
		}
	}
	ix.objs[o.UID] = o
	for _, k := range ix.keys(o) {
		if ix.byKey[k] == nil {
			ix.byKey[k] = map[string]bool{}
		}
		ix.byKey[k][o.UID] = true
	}
}

// Put adds or replaces an object. It keeps the slot the object has.
func (ix *Index) Put(o *Obj) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if old := ix.objs[o.UID]; old != nil && old.Slot != 0 {
		o.Slot = old.Slot
	} else if o.Slot == 0 {
		o.Slot = ix.slotFor(o.UID)
	}
	delete(ix.pending, o.UID)
	ix.put(o)
}

func (ix *Index) Get(uid string) *Obj {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.objs[uid]
}

// SetSyncState replaces an object's sync state.
func (ix *Index) SetSyncState(uid, state string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if o := ix.objs[uid]; o != nil {
		n := *o
		n.SyncState = state
		ix.objs[uid] = &n
	}
}

// Related returns objects linked under key ("enr:<te>", "ev:<enr>", "evte:<te>", "rel:<uid>").
func (ix *Index) related(key string) []*Obj {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []*Obj
	for uid := range ix.byKey[key] {
		if o := ix.objs[uid]; o != nil {
			out = append(out, o)
		}
	}
	return out
}

func (ix *Index) EnrollmentsOf(te string) []*Obj    { return ix.related("enr:" + te) }
func (ix *Index) EventsOf(enrollment string) []*Obj { return ix.related("ev:" + enrollment) }
func (ix *Index) EventsOfTE(te string) []*Obj       { return ix.related("evte:" + te) }
func (ix *Index) RelationshipsOf(uid string) []*Obj { return ix.related("rel:" + uid) }

// byUIDs returns the objects of kind among uids.
func (ix *Index) byUIDs(kind string, uids []string) []*Obj {
	var out []*Obj
	for _, uid := range uids {
		if o := ix.Get(uid); o != nil && o.Kind == kind {
			out = append(out, o)
		}
	}
	return out
}

// All returns every object of kind.
func (ix *Index) All(kind string) []*Obj {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []*Obj
	for _, o := range ix.objs {
		if o.Kind == kind {
			out = append(out, o)
		}
	}
	return out
}

func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.objs)
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

	payloads map[string]M // full payloads loaded for this request (prefetch)
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
	if o == nil || v.Access == nil || o.OwnerOrgUnit != v.Facility {
		return false
	}
	if o.Origin == store.OriginCentral {
		return accessAllows(v, o) && ix.VisibleTo(o, v.UserID)
	}
	if !v.SearchScope {
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
