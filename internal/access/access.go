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
	// RelationshipTypes: which relationships central shows the user.
	RelationshipTypes map[string]Perm `json:"relationshipTypes,omitempty"`
	// Ungroupable: something central uses for this user could not be read, so their
	// pulls are never shared with others (GroupKey).
	Ungroupable bool `json:"ungroupable,omitempty"`
}

// GroupKey is the same for users central answers alike when the hub lists what they
// see at the facility: everything in the list except who they are. Users with the same
// key share one listing per sync (docs/PERFORMANCE.md, "Load on central"). Two things
// central uses are not in the list: category option sharing, which decides events in
// programs with an attribute category combination (those are listed per user), and
// sharing granted to one user directly rather than through a group on anything other
// than programs, stages, types and relationship types. Category options also decide
// enrollments in programs with an enrollment category combination (2.43): those
// programs are listed per user too.
func (l *List) GroupKey() string {
	if l == nil {
		return ""
	}
	// Relationship-type access never read (lists stored before it was, or a refresh
	// that failed) is unknown, not "none": such a user is not grouped.
	if l.Ungroupable || l.RelationshipTypes == nil {
		return "user:" + l.UserUID
	}
	k := *l
	k.UserUID, k.Username = "", ""
	sorted := func(s []string) []string { s = slices.Clone(s); slices.Sort(s); return s }
	k.OrgUnits, k.SearchOrgUnits = sorted(k.OrgUnits), sorted(k.SearchOrgUnits)
	k.Authorities, k.UserGroups = sorted(k.Authorities), sorted(k.UserGroups)
	b, _ := json.Marshal(k) // maps marshal with sorted keys
	return string(b)
}

func Parse(b []byte) (*List, error) {
	if len(b) == 0 {
		return nil, nil
	}
	l := &List{}
	return l, json.Unmarshal(b, l)
}

// InFacility reports whether the facility, with this path ("/root/.../facility"), is
// in the user's capture scope: one of their capture org units or below one, as on
// central. Users without it may not use hub-local tracker calls (spec 8.1).
func (l *List) InFacility(facilityPath string) bool {
	if l == nil || facilityPath == "" {
		return false
	}
	for _, id := range l.OrgUnits {
		if strings.Contains(facilityPath+"/", "/"+id+"/") {
			return true
		}
	}
	return false
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
	// NonRepeatable are the stages that take one event per enrollment. Catalogs from
	// before this field have none, so the hub then leaves the check to central.
	NonRepeatable []string `json:"nonRepeatableStages,omitempty"`
	// AttributeCombo: the program has a non-default category combination, so its
	// events need an attribute option combo.
	AttributeCombo bool `json:"attributeCombo,omitempty"`
	// EnrollmentAttributeCombo: the same for enrollments (2.43's
	// enrollmentCategoryCombo).
	EnrollmentAttributeCombo bool `json:"enrollmentAttributeCombo,omitempty"`
}

// Catalog is the facility's program set plus tracked entity type attributes.
type Catalog struct {
	Programs      map[string]*ProgramMeta `json:"programs"`
	TETAttributes map[string][]AttrMeta   `json:"tetAttributes"` // TET uid -> attributes
	// TypeAttributes are the attributes of every tracked entity type on central.
	// DHIS2 2.42 shows them on any tracked entity, whatever its type
	// (TrackedEntityAggregate.getAllowedAttributeUids).
	TypeAttributes map[string]bool   `json:"typeAttributes,omitempty"`
	StageProgram   map[string]string `json:"-"`
}

// TETs lists the tracked entity types of the catalog's programs, sorted.
func (c *Catalog) TETs() []string {
	var out []string
	for _, p := range c.Programs {
		if p.TET != "" && !slices.Contains(out, p.TET) {
			out = append(out, p.TET)
		}
	}
	slices.Sort(out)
	return out
}

// Merge adds o's programs and tracked entity type attributes that c lacks.
func (c *Catalog) Merge(o *Catalog) {
	if o == nil {
		return
	}
	for id, p := range o.Programs {
		if c.Programs[id] == nil {
			c.Programs[id] = p
		}
	}
	for tet, as := range o.TETAttributes {
		if _, ok := c.TETAttributes[tet]; !ok {
			c.TETAttributes[tet] = as
		}
	}
	for id := range o.TypeAttributes {
		if c.TypeAttributes == nil {
			c.TypeAttributes = map[string]bool{}
		}
		c.TypeAttributes[id] = true
	}
	c.Index()
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
