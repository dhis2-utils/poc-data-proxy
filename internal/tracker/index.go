package tracker

import (
	"encoding/json"
	"maps"
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
}

func NewIndex() *Index {
	return &Index{objs: map[string]*Obj{}, byKey: map[string]map[string]bool{}}
}

// Load rebuilds the index from the store. The store must be unlocked.
func (ix *Index) Load(s *store.Store) error {
	vis, err := s.Visibility()
	if err != nil {
		return err
	}
	fresh := NewIndex()
	err = s.AllObjects(func(o *store.Object) error {
		obj, err := FromStore(o)
		if err != nil {
			return err
		}
		obj.Visible = vis[o.UID]
		fresh.put(obj)
		return nil
	})
	if err != nil {
		return err
	}
	ix.mu.Lock()
	ix.objs, ix.byKey = fresh.objs, fresh.byKey
	ix.mu.Unlock()
	return nil
}

// Clear empties the index (on lock or reset).
func (ix *Index) Clear() {
	ix.mu.Lock()
	ix.objs, ix.byKey = map[string]*Obj{}, map[string]map[string]bool{}
	ix.mu.Unlock()
}

func FromStore(o *store.Object) (*Obj, error) {
	var data M
	if err := json.Unmarshal(o.Payload, &data); err != nil {
		return nil, err
	}
	return &Obj{UID: o.UID, Kind: o.Kind, TE: o.TE, Enrollment: o.Enrollment, Program: o.Program, Stage: o.Stage,
		TET: o.TET, OwnerOrgUnit: o.OwnerOrgUnit, UpdatedAt: o.UpdatedAt, Origin: o.Origin, SyncState: o.SyncState,
		Data: data}, nil
}

func (o *Obj) ToStore() *store.Object {
	b, _ := json.Marshal(o.Data)
	return &store.Object{UID: o.UID, Kind: o.Kind, TE: o.TE, Enrollment: o.Enrollment, Program: o.Program,
		Stage: o.Stage, TET: o.TET, OwnerOrgUnit: o.OwnerOrgUnit, UpdatedAt: o.UpdatedAt, Origin: o.Origin,
		SyncState: o.SyncState, Payload: b}
}

func (ix *Index) keys(o *Obj) []string {
	switch o.Kind {
	case store.KindEnrollment:
		return []string{"enr:" + o.TE}
	case store.KindEvent:
		return []string{"ev:" + o.Enrollment, "evte:" + o.TE}
	case store.KindRelationship:
		from, to := RelRef(asM(o.Data["from"])), RelRef(asM(o.Data["to"]))
		return []string{"rel:" + from.UID, "rel:" + to.UID}
	}
	return nil
}

func (ix *Index) put(o *Obj) {
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

// Put adds or replaces an object, keeping its visibility set when o.Visible is nil.
func (ix *Index) Put(o *Obj) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if o.Visible == nil {
		if old := ix.objs[o.UID]; old != nil {
			o.Visible = old.Visible
		}
	}
	ix.put(o)
}

func (ix *Index) Get(uid string) *Obj {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.objs[uid]
}

// SetVisible adds or removes a user from an object's visibility set.
func (ix *Index) SetVisible(uid string, userID int64, visible bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	o := ix.objs[uid]
	if o == nil {
		return
	}
	n := *o
	n.Visible = maps.Clone(o.Visible)
	if n.Visible == nil {
		n.Visible = map[int64]bool{}
	}
	if visible {
		n.Visible[userID] = true
	} else {
		delete(n.Visible, userID)
	}
	ix.objs[uid] = &n
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
	UserID               int64
	Access               *access.List
	Facility             string
	Catalog              *access.Catalog
}

// CanRead applies spec 8.2.
func (ix *Index) CanRead(v *Viewer, o *Obj) bool {
	if o == nil || v.Access == nil || o.OwnerOrgUnit != v.Facility {
		return false
	}
	if o.Origin == store.OriginCentral {
		return o.Visible[v.UserID]
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
