// Package access holds the per-user access list fetched from central (spec 8.1) and
// the facility's program catalog. The hub does not evaluate DHIS2 sharing; it only
// matches against the read/write flags that central returned for each user.
package access

import (
	"encoding/json"
	"slices"
	"strings"
)

type Perm struct {
	Read  bool `json:"read"`
	Write bool `json:"write"`
}

// List is what the hub stores per user.
type List struct {
	UserUID        string          `json:"userUid"`
	Username       string          `json:"username"`
	OrgUnits       []string        `json:"orgUnits"`
	SearchOrgUnits []string        `json:"searchOrgUnits"`
	Authorities    []string        `json:"authorities"`
	UserGroups     []string        `json:"userGroups"`
	Programs       map[string]Perm `json:"programs"`
	Stages         map[string]Perm `json:"stages"`
	TETs           map[string]Perm `json:"trackedEntityTypes"`
}

func Parse(b []byte) (*List, error) {
	if len(b) == 0 {
		return nil, nil
	}
	l := &List{}
	return l, json.Unmarshal(b, l)
}

// InFacility reports whether the user has the facility in their capture org units.
// Users without it may not use hub-local tracker calls (spec 8.1).
func (l *List) InFacility(facility string) bool {
	return l != nil && slices.Contains(l.OrgUnits, facility)
}

// SearchesIn reports whether the org unit with this path ("/a/b/c") is in the user's
// effective search scope: their search org units, or their capture org units when
// they have none, and everything below them.
func (l *List) SearchesIn(path string) bool {
	if l == nil {
		return false
	}
	scope := l.SearchOrgUnits
	if len(scope) == 0 {
		scope = l.OrgUnits
	}
	for _, id := range scope {
		if strings.Contains(path+"/", "/"+id+"/") {
			return true
		}
	}
	return false
}

func (l *List) InGroup(group string) bool {
	return l != nil && group != "" && slices.Contains(l.UserGroups, group)
}

func (l *List) Program(id string) Perm {
	if l == nil {
		return Perm{}
	}
	return l.Programs[id]
}

func (l *List) Stage(id string) Perm {
	if l == nil {
		return Perm{}
	}
	return l.Stages[id]
}

func (l *List) TET(id string) Perm {
	if l == nil {
		return Perm{}
	}
	return l.TETs[id]
}

// AttrMeta describes a tracked entity attribute the hub needs to know about.
type AttrMeta struct {
	ID        string `json:"id"`
	Generated bool   `json:"generated,omitempty"`
	Unique    bool   `json:"unique,omitempty"`
	Pattern   string `json:"pattern,omitempty"`
}

// ProgramMeta is the hub's view of one cached tracker program.
type ProgramMeta struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	TET        string     `json:"trackedEntityType"`
	Stages     []string   `json:"programStages"`
	Attributes []AttrMeta `json:"attributes"` // program attributes
}

// Catalog is the facility's program set plus tracked entity type attributes.
type Catalog struct {
	Programs      map[string]*ProgramMeta `json:"programs"`
	TETAttributes map[string][]AttrMeta   `json:"tetAttributes"` // TET uid -> attributes
	StageProgram  map[string]string       `json:"-"`
}

func (c *Catalog) Index() {
	c.StageProgram = map[string]string{}
	for _, p := range c.Programs {
		for _, s := range p.Stages {
			c.StageProgram[s] = p.ID
		}
	}
}

func (c *Catalog) ProgramIDs() []string {
	var ids []string
	for id := range c.Programs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// GeneratedAttributes returns every generated attribute used by cached programs.
func (c *Catalog) GeneratedAttributes() []AttrMeta {
	seen := map[string]bool{}
	var out []AttrMeta
	add := func(as []AttrMeta) {
		for _, a := range as {
			if a.Generated && !seen[a.ID] {
				seen[a.ID] = true
				out = append(out, a)
			}
		}
	}
	for _, id := range c.ProgramIDs() {
		add(c.Programs[id].Attributes)
	}
	for _, as := range c.TETAttributes {
		add(as)
	}
	return out
}

// ReadableAttribute reports whether the user may see attribute attr on a tracked entity
// of type tet: it is a TET attribute and the user can read the TET, or it is an
// attribute of a program the user can read.
func (c *Catalog) ReadableAttribute(l *List, tet, attr string) bool {
	if l.TET(tet).Read {
		for _, a := range c.TETAttributes[tet] {
			if a.ID == attr {
				return true
			}
		}
	}
	for _, p := range c.Programs {
		if !l.Program(p.ID).Read {
			continue
		}
		for _, a := range p.Attributes {
			if a.ID == attr {
				return true
			}
		}
	}
	return false
}
