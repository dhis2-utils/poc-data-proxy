package provision

import (
	"fmt"

	"github.com/dhis2/facility-hub/internal/testkit"
)

// Layout is where a profile's role users and access matrix live: the org units and
// the programs each access dimension applies to. Every profile builds one, so the
// same access patterns land on synthetic and on real metadata.
type Layout struct {
	Prefix        string // UID prefix for created objects (3 letters), e.g. "Syn"
	UserPrefix    string // username prefix, e.g. "syn_"
	Root          string
	District      string // the facility's district
	OtherDistrict string
	Facility      string
	Sibling       string // another facility in the district

	Programs        []ProgramLayout
	Open            *ProgramLayout // the "open" matrix dimension: the primary program
	Protected       *ProgramLayout // the "protected" dimension; nil if the profile has none
	Closed          *ProgramLayout // the "closed" dimension; nil if the profile has none
	RestrictedStage string         // hidden from the restricted role; a stage of Open
	// SharedOption is a category option of an attribute category combo, shared per
	// the "option" dimension; HiddenOption is shared with no test user. Events carry
	// them as their attribute option combo.
	SharedOption, HiddenOption string
	RelationshipTypes          []string
}

type ProgramLayout struct {
	ID     string
	TET    string
	Stages []string
}

func (l *Layout) tets() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range l.Programs {
		if p.TET != "" && !seen[p.TET] {
			seen[p.TET] = true
			out = append(out, p.TET)
		}
	}
	return out
}

// stagesOf returns a program's stages, without the restricted one.
func (l *Layout) stagesOf(p *ProgramLayout) []string {
	var out []string
	for _, s := range p.Stages {
		if s != l.RestrictedStage {
			out = append(out, s)
		}
	}
	return out
}

// Access is what buildAccess produces: metadata to import (role, groups, users) and
// the grants to add to existing objects' sharing.
type Access struct {
	Role    map[string]any
	Groups  []any
	Users   []any
	Sharing map[string]*sharing // object UID -> grants to add
	Roles   map[string]testkit.User
	Matrix  []testkit.MatrixUser
	Setup   string // the setup user group
}

func (l *Layout) uid(kind string, n int) string {
	return fmt.Sprintf("%s%s%0*d", l.Prefix, kind, 11-len(l.Prefix)-len(kind), n)
}

// buildAccess creates the role users and the access matrix for a layout.
func buildAccess(l *Layout) *Access {
	a := &Access{Sharing: map[string]*sharing{}, Roles: map[string]testkit.User{}}
	sh := func(id string) *sharing {
		if a.Sharing[id] == nil {
			a.Sharing[id] = &sharing{}
		}
		return a.Sharing[id]
	}
	grant := func(user, group, access string, ids ...string) {
		for _, id := range ids {
			sh(id).grant(user, group, access)
		}
	}
	role := l.uid("Rol", 1)
	grpSetup, grpNurse, grpLab, grpClerk, grpDistrict := l.uid("Grp", 1), l.uid("Grp", 2), l.uid("Grp", 3), l.uid("Grp", 4), l.uid("Grp", 5)
	a.Setup = grpSetup

	// Role groups.
	for i := range l.Programs {
		p := &l.Programs[i]
		grant("", grpNurse, acWrite, append([]string{p.ID}, l.stagesOf(p)...)...)
	}
	grant("", grpNurse, acWrite, l.tets()...)
	if l.SharedOption != "" {
		grant("", grpNurse, acWrite, l.SharedOption)
		grant("", grpClerk, acRead, l.SharedOption)
	}
	grant("", grpNurse, acWrite, l.RelationshipTypes...)
	if l.RestrictedStage != "" {
		grant("", grpLab, acWrite, l.RestrictedStage)
	}
	for _, p := range []*ProgramLayout{l.Open, l.Protected} {
		if p != nil {
			grant("", grpClerk, acRead, append([]string{p.ID}, l.stagesOf(p)...)...)
		}
	}
	grant("", grpClerk, acWrite, l.tets()...)
	grant("", grpDistrict, acRead, append([]string{l.Open.ID}, l.stagesOf(l.Open)...)...)
	grant("", grpDistrict, acRead, l.Open.TET)

	type spec struct {
		uid, username, first, surname string
		capture, search, groups       []string
		dims                          map[string]string
	}
	roleSpecs := []struct {
		role string
		spec
	}{
		{testkit.RoleAdmin, spec{username: "admin", first: "Setup", surname: "Admin", capture: []string{l.Facility},
			search: []string{l.District}, groups: []string{grpSetup, grpDistrict}}},
		{testkit.RoleNurse, spec{username: "nurse", first: "Nurse", surname: "Full", capture: []string{l.Facility},
			search: []string{l.District}, groups: []string{grpNurse, grpLab}}},
		{testkit.RoleRestricted, spec{username: "restricted", first: "Nurse", surname: "Restricted", capture: []string{l.Facility},
			search: []string{l.District}, groups: []string{grpNurse}}},
		{testkit.RoleClerk, spec{username: "clerk", first: "Clerk", surname: "Read", capture: []string{l.Facility},
			search: []string{l.District}, groups: []string{grpClerk}}},
		{testkit.RoleOutsider, spec{username: "outsider", first: "Nurse", surname: "Sibling", capture: []string{l.Sibling},
			search: []string{l.District}, groups: []string{grpNurse, grpLab}}},
		{testkit.RoleDistrict, spec{username: "district", first: "District", surname: "Officer", capture: []string{l.District},
			search: []string{l.District}, groups: []string{grpDistrict}}},
		{testkit.RoleSearchless, spec{username: "searchless", first: "Nurse", surname: "Searchless", capture: []string{l.Facility},
			search: []string{l.OtherDistrict}, groups: []string{grpNurse}}},
	}
	var specs []spec
	for i, r := range roleSpecs {
		s := r.spec
		s.uid, s.username = l.uid("Usr", i+1), l.UserPrefix+s.username
		specs = append(specs, s)
		a.Roles[r.role] = testkit.User{Username: s.username, UID: s.uid}
	}

	scope := map[string][]string{
		"none": nil, "facility": {l.Facility}, "district": {l.District}, "sibling": {l.Sibling},
		"facility+sibling": {l.Facility, l.Sibling}, "other-district": {l.OtherDistrict}, "country": {l.Root},
	}
	sizes := make([]int, len(matrixDims))
	for i, d := range matrixDims {
		sizes[i] = len(d.Values)
	}
	var matrixGroups []any
	rows := []map[string]string{}
	for _, row := range Pairwise(sizes) {
		dims := map[string]string{}
		for i, v := range row {
			dims[matrixDims[i].Name] = matrixDims[i].Values[v]
		}
		rows = append(rows, dims)
	}
	// Twins for the hub's shared listings (access.List.GroupKey): two users whose
	// access lists match except for the shared category option, which the key leaves
	// out. The hub groups them; the oracle checks each against central.
	for _, option := range []string{"write", "none"} {
		rows = append(rows, map[string]string{"capture": "facility", "search": "facility", "open": "write", "via": "user",
			"protected": "write", "closed": "none", "lab": "none", "tet": "write", "option": option})
	}
	for n, dims := range rows {
		s := spec{uid: l.uid("Mxu", n), username: fmt.Sprintf("%smx%02d", l.UserPrefix, n), first: "Matrix",
			surname: fmt.Sprintf("%02d", n), capture: scope[dims["capture"]], search: scope[dims["search"]], dims: dims}
		group := ""
		if dims["via"] == "group" {
			group = l.uid("Mxg", n)
			s.groups = []string{group}
			matrixGroups = append(matrixGroups, map[string]any{"id": group, "name": l.Prefix + " matrix group " + s.surname,
				"sharing": (&sharing{Public: acMeta}).json()})
		}
		for dim, p := range map[string]*ProgramLayout{"open": l.Open, "protected": l.Protected, "closed": l.Closed} {
			if p != nil {
				grant(s.uid, group, accessOf[dims[dim]], append([]string{p.ID}, l.stagesOf(p)...)...)
			}
		}
		if l.RestrictedStage != "" {
			grant(s.uid, group, accessOf[dims["lab"]], l.RestrictedStage)
		}
		grant(s.uid, group, accessOf[dims["tet"]], l.tets()...)
		if l.SharedOption != "" {
			grant(s.uid, group, accessOf[dims["option"]], l.SharedOption)
		}
		grant(s.uid, group, acWrite, l.RelationshipTypes...)
		specs = append(specs, s)
		a.Matrix = append(a.Matrix, testkit.MatrixUser{User: testkit.User{Username: s.username, UID: s.uid}, Dims: dims})
	}

	// A twin of the nurse: same groups and scopes, so the hub lists for both once.
	twin := spec{uid: l.uid("Usr", len(roleSpecs)+1), username: l.UserPrefix + "nurse_twin", first: "Nurse", surname: "Twin",
		capture: []string{l.Facility}, search: []string{l.District}, groups: []string{grpNurse, grpLab}}
	specs = append(specs, twin)
	a.Matrix = append(a.Matrix, testkit.MatrixUser{User: testkit.User{Username: twin.username, UID: twin.uid},
		Dims: map[string]string{"capture": "facility", "search": "district", "via": "group", "tet": "write", "twin": "nurse"}})

	members := map[string][]string{}
	for _, s := range specs {
		for _, g := range s.groups {
			members[g] = append(members[g], s.uid)
		}
	}
	for _, g := range []struct{ id, name string }{{grpSetup, "hub setup"}, {grpNurse, "nurses"}, {grpLab, "lab access"},
		{grpClerk, "clerks"}, {grpDistrict, "district officers"}} {
		a.Groups = append(a.Groups, map[string]any{"id": g.id, "name": l.Prefix + " " + g.name,
			"sharing": (&sharing{Public: acMeta}).json()})
	}
	a.Groups = append(a.Groups, matrixGroups...)
	for _, g := range a.Groups {
		gm := g.(map[string]any)
		gm["users"] = refs(members[gm["id"].(string)]...)
	}
	a.Role = map[string]any{"id": role, "name": l.Prefix + " hub test user",
		"authorities": []string{"M_dhis-web-capture", "M_dhis-web-dashboard", "F_UNCOMPLETE_EVENT",
			"M_dhis-web-aggregate-data-entry", "F_DATAVALUE_ADD", "F_DATAVALUE_DELETE"}}
	for _, s := range specs {
		a.Users = append(a.Users, map[string]any{"id": s.uid, "username": s.username, "password": Password,
			"firstName": s.first, "surname": s.surname, "userRoles": refs(role), "organisationUnits": refs(s.capture...),
			"dataViewOrganisationUnits": refs(s.capture...), "teiSearchOrganisationUnits": refs(s.search...)})
	}
	return a
}

func ref(id string) map[string]any { return map[string]any{"id": id} }

func refs(ids ...string) []any {
	out := []any{}
	for _, id := range ids {
		out = append(out, ref(id))
	}
	return out
}
