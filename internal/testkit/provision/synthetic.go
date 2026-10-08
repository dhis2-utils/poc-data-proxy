// Package provision turns a DHIS2 instance into a test setup and writes its manifest
// (docs/TESTING.md). Profiles: Synthetic builds everything on an empty instance;
// the seeded profiles adapt a demo database.
package provision

import (
	"fmt"
	"strings"

	"github.com/dhis2/facility-hub/internal/testkit"
)

// The synthetic setup. Fixed UIDs make provisioning idempotent: running it twice
// updates instead of duplicating.
const (
	ouRoot  = "SynOuRoot01"
	ouNorth = "SynOuDist01" // the hub's district
	ouSouth = "SynOuDist02"
	ouFac   = "SynOuFac011" // the hub's facility
	ouSib   = "SynOuFac012" // a sibling facility in the same district
	ouSib2  = "SynOuFac013"
	ouFar   = "SynOuFac021" // a facility in the other district

	osSex = "SynOsSex001"
	osRes = "SynOsRes001"

	atFirst = "SynAtFirst1"
	atLast  = "SynAtLast01"
	atNatID = "SynAtNatId1" // unique
	atSex   = "SynAtSex001" // option set
	atDob   = "SynAtDob001" // date
	atPhone = "SynAtPhone1"
	atPatNo = "SynAtPatNo1" // generated, RANDOM
	atArtNo = "SynAtArtNo1" // generated, ORG_UNIT_CODE + SEQUENTIAL
	atTbNo  = "SynAtTbNo01" // unique, not generated

	tetPerson = "SynTetPers1"

	deWeight = "SynDeWeight"
	deHb     = "SynDeHb0001"
	deNote   = "SynDeNote01"
	deHivRes = "SynDeHivRes" // option set
	deDeliv  = "SynDeDeliv1" // date
	deArv    = "SynDeArv001" // boolean
	deLab    = "SynDeLabRes"
	deSmear  = "SynDeSmear1"

	// Programs: one per access level, a public one and an event program.
	prMat = "SynPrgMat01" // OPEN; its lab stage is restricted
	prHiv = "SynPrgHiv01" // PROTECTED
	prTb  = "SynPrgTb001" // CLOSED
	prCom = "SynPrgCom01" // OPEN, data shared publicly
	prEvt = "SynPrgEvt01" // WITHOUT_REGISTRATION

	stAnc = "SynStAnc001" // repeatable
	stDel = "SynStDel001"
	stLab = "SynStLab001" // restricted
	stIni = "SynStIni001"
	stFup = "SynStFup001" // repeatable
	stTbV = "SynStTbV001"
	stCom = "SynStCom001"
	stEvt = "SynStEvt001"

	rtMotherChild = "SynRtMoChi1"

	roleUser = "SynRole0001"

	grpSetup    = "SynGrpSetup"
	grpNurse    = "SynGrpNurse"
	grpLab      = "SynGrpLab01"
	grpClerk    = "SynGrpClerk"
	grpDistrict = "SynGrpDistr"

	Password = "Hub-Test-2026!"
)

const (
	acNone  = ""
	acMeta  = "r-------"
	acRead  = "r-r-----"
	acWrite = "rwrw----"
)

// sharing is the 2.42 metadata sharing object.
type sharing struct {
	Public string
	Users  map[string]string // uid -> access
	Groups map[string]string
}

func (s *sharing) grant(user, group, access string) {
	if access == acNone {
		return
	}
	if group != "" {
		if s.Groups == nil {
			s.Groups = map[string]string{}
		}
		s.Groups[group] = maxAccess(s.Groups[group], access)
		return
	}
	if s.Users == nil {
		s.Users = map[string]string{}
	}
	s.Users[user] = maxAccess(s.Users[user], access)
}

func maxAccess(a, b string) string {
	rank := map[string]int{acNone: 0, acMeta: 1, acRead: 2, acWrite: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func (s *sharing) json() map[string]any {
	pub := s.Public
	if pub == "" {
		pub = "--------"
	}
	users, groups := map[string]any{}, map[string]any{}
	for id, a := range s.Users {
		users[id] = map[string]any{"id": id, "access": a}
	}
	for id, a := range s.Groups {
		groups[id] = map[string]any{"id": id, "access": a}
	}
	return map[string]any{"public": pub, "external": false, "users": users, "userGroups": groups}
}

// The access matrix (docs/TESTING.md): users covering every pair of these settings.
var matrixDims = []struct {
	Name   string
	Values []string
}{
	{"capture", []string{"facility", "district", "sibling", "facility+sibling", "country"}},
	{"search", []string{"none", "facility", "district", "other-district", "country"}},
	{"open", []string{"none", "metadata", "read", "write"}}, // the OPEN program
	{"via", []string{"user", "group"}},                      // how the grants reach the user
	{"protected", []string{"none", "read", "write"}},
	{"closed", []string{"none", "write"}},
	{"lab", []string{"none", "read", "write"}}, // the restricted stage
	{"tet", []string{"none", "read", "write"}},
}

var accessOf = map[string]string{"none": acNone, "metadata": acMeta, "read": acRead, "write": acWrite}

var scopeOUs = map[string][]string{
	"none": nil, "facility": {ouFac}, "district": {ouNorth}, "sibling": {ouSib},
	"facility+sibling": {ouFac, ouSib}, "other-district": {ouSouth}, "country": {ouRoot},
}

type userSpec struct {
	uid, username, first, surname string
	capture, search               []string
	groups                        []string
	dims                          map[string]string
}

// metadata builds the whole synthetic setup as one /api/metadata payload, and the
// manifest that describes it.
func syntheticMetadata() (map[string]any, *testkit.Manifest) {
	ref := func(id string) map[string]any { return map[string]any{"id": id} }
	refs := func(ids ...string) []any {
		out := []any{}
		for _, id := range ids {
			out = append(out, ref(id))
		}
		return out
	}
	ou := func(id, name, code, parent string) map[string]any {
		o := map[string]any{"id": id, "name": name, "shortName": name, "code": code, "openingDate": "2020-01-01"}
		if parent != "" {
			o["parent"] = ref(parent)
		}
		return o
	}
	ous := []any{
		ou(ouRoot, "Hubland", "SYN_ROOT", ""),
		ou(ouNorth, "North district", "SYN_N", ouRoot),
		ou(ouSouth, "South district", "SYN_S", ouRoot),
		ou(ouFac, "North clinic", "SYN_N1", ouNorth),
		ou(ouSib, "North health post", "SYN_N2", ouNorth),
		ou(ouSib2, "North dispensary", "SYN_N3", ouNorth),
		ou(ouFar, "South clinic", "SYN_S1", ouSouth),
	}
	facilities := []string{ouFac, ouSib, ouSib2, ouFar}

	meta := (&sharing{Public: acMeta}).json() // metadata everyone may read
	optionSets := []any{
		map[string]any{"id": osSex, "name": "Synthetic sex", "valueType": "TEXT", "sharing": meta,
			"options": []any{map[string]any{"id": "SynOpSexM01", "code": "M", "name": "Male"},
				map[string]any{"id": "SynOpSexF01", "code": "F", "name": "Female"}}},
		map[string]any{"id": osRes, "name": "Synthetic test result", "valueType": "TEXT", "sharing": meta,
			"options": []any{map[string]any{"id": "SynOpResP01", "code": "POS", "name": "Positive"},
				map[string]any{"id": "SynOpResN01", "code": "NEG", "name": "Negative"}}},
	}
	var options []any
	for _, o := range optionSets {
		om := o.(map[string]any)
		for i, op := range om["options"].([]any) {
			opm := op.(map[string]any)
			opm["optionSet"] = ref(om["id"].(string))
			opm["sortOrder"] = i + 1
			options = append(options, opm)
		}
		var ids []string
		for _, op := range om["options"].([]any) {
			ids = append(ids, op.(map[string]any)["id"].(string))
		}
		om["options"] = refs(ids...)
	}

	tea := func(id, name, vt string, extra map[string]any) map[string]any {
		a := map[string]any{"id": id, "name": "Synthetic " + name, "shortName": "Syn " + name, "valueType": vt,
			"aggregationType": "NONE", "sharing": meta}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	attributes := []any{
		tea(atFirst, "first name", "TEXT", nil),
		tea(atLast, "last name", "TEXT", nil),
		tea(atNatID, "national ID", "TEXT", map[string]any{"unique": true}),
		tea(atSex, "sex", "TEXT", map[string]any{"optionSet": ref(osSex)}),
		tea(atDob, "date of birth", "DATE", nil),
		tea(atPhone, "phone", "PHONE_NUMBER", nil),
		tea(atPatNo, "patient number", "TEXT", map[string]any{"unique": true, "generated": true,
			"pattern": `"PN" + RANDOM(#######)`}),
		tea(atArtNo, "ART number", "TEXT", map[string]any{"unique": true, "generated": true,
			"pattern": `ORG_UNIT_CODE(...) + "-" + SEQUENTIAL(####)`}),
		tea(atTbNo, "TB number", "TEXT", map[string]any{"unique": true}),
	}
	de := func(id, name, vt string, extra map[string]any) map[string]any {
		d := map[string]any{"id": id, "name": "Synthetic " + name, "shortName": "Syn " + name, "valueType": vt,
			"domainType": "TRACKER", "aggregationType": "NONE", "sharing": meta}
		for k, v := range extra {
			d[k] = v
		}
		return d
	}
	dataElements := []any{
		de(deWeight, "weight", "NUMBER", nil),
		de(deHb, "haemoglobin", "NUMBER", nil),
		de(deNote, "note", "LONG_TEXT", nil),
		de(deHivRes, "HIV test result", "TEXT", map[string]any{"optionSet": ref(osRes)}),
		de(deDeliv, "delivery date", "DATE", nil),
		de(deArv, "on ARV", "BOOLEAN", nil),
		de(deLab, "lab result", "TEXT", nil),
		de(deSmear, "smear result", "TEXT", map[string]any{"optionSet": ref(osRes)}),
	}

	// Sharing per object, filled from the groups (roles) and the matrix users below.
	sh := map[string]*sharing{}
	for _, id := range []string{tetPerson, prMat, prHiv, prTb, prCom, prEvt, stAnc, stDel, stLab, stIni, stFup, stTbV,
		stCom, stEvt, rtMotherChild} {
		sh[id] = &sharing{}
	}
	sh[prCom].Public, sh[stCom].Public = acWrite, acWrite // public data access
	for _, id := range []string{prMat, prHiv, prTb} {
		_ = id // no public access: what a user sees comes from their grants
	}
	grantGroup := func(group, access string, ids ...string) {
		for _, id := range ids {
			sh[id].grant("", group, access)
		}
	}
	grantGroup(grpNurse, acWrite, tetPerson, prMat, stAnc, stDel, prHiv, stIni, stFup, prTb, stTbV, prEvt, stEvt, rtMotherChild)
	grantGroup(grpLab, acWrite, stLab)
	grantGroup(grpClerk, acRead, prMat, stAnc, stDel, prHiv, stIni, stFup)
	grantGroup(grpClerk, acWrite, tetPerson)
	grantGroup(grpDistrict, acRead, tetPerson, prMat, stAnc, stDel)
	sh[rtMotherChild].Public = acMeta

	// Role users (the scenario tests).
	roles := []userSpec{
		{uid: "SynUsrAdmn1", username: "syn_admin", first: "Setup", surname: "Admin", capture: []string{ouFac},
			search: []string{ouNorth}, groups: []string{grpSetup, grpDistrict}},
		{uid: "SynUsrNurs1", username: "syn_nurse", first: "Nurse", surname: "Full", capture: []string{ouFac},
			search: []string{ouNorth}, groups: []string{grpNurse, grpLab}},
		{uid: "SynUsrRstr1", username: "syn_restricted", first: "Nurse", surname: "Restricted", capture: []string{ouFac},
			search: []string{ouNorth}, groups: []string{grpNurse}},
		{uid: "SynUsrClrk1", username: "syn_clerk", first: "Clerk", surname: "Read", capture: []string{ouFac},
			search: []string{ouNorth}, groups: []string{grpClerk}},
		{uid: "SynUsrOuts1", username: "syn_outsider", first: "Nurse", surname: "Sibling", capture: []string{ouSib},
			search: []string{ouNorth}, groups: []string{grpNurse, grpLab}},
		{uid: "SynUsrDist1", username: "syn_district", first: "District", surname: "Officer", capture: []string{ouNorth},
			search: []string{ouNorth}, groups: []string{grpDistrict}},
		{uid: "SynUsrNoSr1", username: "syn_searchless", first: "Nurse", surname: "Searchless", capture: []string{ouFac},
			search: []string{ouFar}, groups: []string{grpNurse}},
	}
	roleNames := []string{testkit.RoleAdmin, testkit.RoleNurse, testkit.RoleRestricted, testkit.RoleClerk,
		testkit.RoleOutsider, testkit.RoleDistrict, testkit.RoleSearchless}

	// Matrix users: each gets its grants directly or through a group of its own.
	sizes := make([]int, len(matrixDims))
	for i, d := range matrixDims {
		sizes[i] = len(d.Values)
	}
	var matrix []userSpec
	var matrixGroups []any
	for n, row := range Pairwise(sizes) {
		dims := map[string]string{}
		for i, v := range row {
			dims[matrixDims[i].Name] = matrixDims[i].Values[v]
		}
		u := userSpec{uid: fmt.Sprintf("SynUsrMx%03d", n), username: fmt.Sprintf("syn_mx%02d", n), first: "Matrix",
			surname: fmt.Sprintf("%02d", n), capture: scopeOUs[dims["capture"]], search: scopeOUs[dims["search"]], dims: dims}
		group := ""
		if dims["via"] == "group" {
			group = fmt.Sprintf("SynGrpMx%03d", n)
			u.groups = []string{group}
			matrixGroups = append(matrixGroups, map[string]any{"id": group, "name": "Synthetic matrix group " + u.surname,
				"sharing": (&sharing{Public: acMeta}).json()})
		}
		grant := func(access string, ids ...string) {
			for _, id := range ids {
				sh[id].grant(u.uid, group, access)
			}
		}
		grant(accessOf[dims["open"]], prMat, stAnc, stDel)
		grant(accessOf[dims["lab"]], stLab)
		grant(accessOf[dims["protected"]], prHiv, stIni, stFup)
		grant(accessOf[dims["closed"]], prTb, stTbV)
		grant(accessOf[dims["tet"]], tetPerson)
		grant(acWrite, rtMotherChild)
		matrix = append(matrix, u)
	}

	// Group membership is set on the group side.
	members := map[string][]string{}
	for _, u := range append(append([]userSpec{}, roles...), matrix...) {
		for _, g := range u.groups {
			members[g] = append(members[g], u.uid)
		}
	}
	groups := []any{}
	for _, g := range []struct{ id, name string }{{grpSetup, "Synthetic hub setup"}, {grpNurse, "Synthetic nurses"},
		{grpLab, "Synthetic lab access"}, {grpClerk, "Synthetic clerks"}, {grpDistrict, "Synthetic district officers"}} {
		groups = append(groups, map[string]any{"id": g.id, "name": g.name, "sharing": (&sharing{Public: acMeta}).json()})
	}
	groups = append(groups, matrixGroups...)
	for _, g := range groups {
		gm := g.(map[string]any)
		gm["users"] = refs(members[gm["id"].(string)]...)
	}

	userJSON := func(u userSpec) map[string]any {
		return map[string]any{"id": u.uid, "username": u.username, "password": Password, "firstName": u.first,
			"surname": u.surname, "userRoles": refs(roleUser), "organisationUnits": refs(u.capture...),
			"dataViewOrganisationUnits": refs(u.capture...), "teiSearchOrganisationUnits": refs(u.search...)}
	}
	var users []any
	for _, u := range roles {
		users = append(users, userJSON(u))
	}
	for _, u := range matrix {
		users = append(users, userJSON(u))
	}

	ptea := func(id string, mandatory bool) map[string]any {
		return map[string]any{"trackedEntityAttribute": ref(id), "mandatory": mandatory, "searchable": true,
			"displayInList": true}
	}
	program := func(id, name, level string, attrs []any, stages []string) map[string]any {
		return map[string]any{"id": id, "name": "Synthetic " + name, "shortName": "Syn " + name,
			"programType": "WITH_REGISTRATION", "trackedEntityType": ref(tetPerson), "accessLevel": level,
			"onlyEnrollOnce": false, "displayIncidentDate": true, "programTrackedEntityAttributes": attrs,
			"programStages": refs(stages...), "organisationUnits": refs(facilities...), "sharing": sh[id].json()}
	}
	programs := []any{
		program(prMat, "maternal care", "OPEN", []any{ptea(atFirst, true), ptea(atLast, true), ptea(atNatID, false),
			ptea(atSex, false), ptea(atDob, false), ptea(atPhone, false), ptea(atPatNo, false)}, []string{stAnc, stDel, stLab}),
		program(prHiv, "HIV care", "PROTECTED", []any{ptea(atFirst, true), ptea(atLast, true), ptea(atArtNo, false),
			ptea(atNatID, false)}, []string{stIni, stFup}),
		program(prTb, "TB treatment", "CLOSED", []any{ptea(atFirst, true), ptea(atLast, true), ptea(atTbNo, false)},
			[]string{stTbV}),
		program(prCom, "community outreach", "OPEN", []any{ptea(atFirst, true), ptea(atPhone, false)}, []string{stCom}),
		map[string]any{"id": prEvt, "name": "Synthetic facility events", "shortName": "Syn facility events",
			"programType": "WITHOUT_REGISTRATION", "programStages": refs(stEvt), "organisationUnits": refs(facilities...),
			"sharing": sh[prEvt].json()},
	}
	psde := func(id string, compulsory bool) map[string]any {
		return map[string]any{"dataElement": ref(id), "compulsory": compulsory}
	}
	stage := func(id, name, program string, order int, repeatable bool, des []any) map[string]any {
		return map[string]any{"id": id, "name": "Synthetic " + name, "program": ref(program), "sortOrder": order,
			"repeatable": repeatable, "programStageDataElements": des, "sharing": sh[id].json()}
	}
	stages := []any{
		stage(stAnc, "ANC visit", prMat, 1, true, []any{psde(deWeight, true), psde(deHb, false), psde(deNote, false)}),
		stage(stDel, "delivery", prMat, 2, false, []any{psde(deDeliv, false), psde(deNote, false)}),
		stage(stLab, "lab results", prMat, 3, true, []any{psde(deLab, false), psde(deHivRes, false)}),
		stage(stIni, "ART initiation", prHiv, 1, false, []any{psde(deHivRes, true), psde(deArv, false)}),
		stage(stFup, "ART follow-up", prHiv, 2, true, []any{psde(deWeight, false), psde(deArv, false)}),
		stage(stTbV, "TB visit", prTb, 1, true, []any{psde(deSmear, false), psde(deWeight, false)}),
		stage(stCom, "outreach visit", prCom, 1, true, []any{psde(deNote, false)}),
		stage(stEvt, "facility event", prEvt, 1, true, []any{psde(deNote, false), psde(deWeight, false)}),
	}

	metadata := map[string]any{
		"organisationUnits": ous,
		"optionSets":        optionSets, "options": options,
		"trackedEntityAttributes": attributes,
		"trackedEntityTypes": []any{map[string]any{"id": tetPerson, "name": "Synthetic person", "shortName": "Syn person",
			"trackedEntityTypeAttributes": []any{
				map[string]any{"trackedEntityAttribute": ref(atFirst), "searchable": true, "displayInList": true},
				map[string]any{"trackedEntityAttribute": ref(atLast), "searchable": true, "displayInList": true},
				map[string]any{"trackedEntityAttribute": ref(atNatID), "searchable": true, "displayInList": true}},
			"sharing": sh[tetPerson].json()}},
		"dataElements":  dataElements,
		"programs":      programs,
		"programStages": stages,
		"relationshipTypes": []any{map[string]any{"id": rtMotherChild, "name": "Synthetic mother of",
			"fromToName": "mother of", "toFromName": "child of", "bidirectional": false,
			"fromConstraint": map[string]any{"relationshipEntity": "TRACKED_ENTITY_INSTANCE", "trackedEntityType": ref(tetPerson)},
			"toConstraint":   map[string]any{"relationshipEntity": "TRACKED_ENTITY_INSTANCE", "trackedEntityType": ref(tetPerson)},
			"sharing":        sh[rtMotherChild].json()}},
		"userRoles": []any{map[string]any{"id": roleUser, "name": "Synthetic tracker user",
			"authorities": []string{"M_dhis-web-capture", "M_dhis-web-dashboard", "F_UNCOMPLETE_EVENT"}}},
		"userGroups": groups,
		"users":      users,
	}

	m := &testkit.Manifest{
		Profile: "synthetic", Password: Password, SetupGroup: grpSetup,
		Facility: testkit.OrgUnit{ID: ouFac, Name: "North clinic", Code: "SYN_N1", Path: "/" + ouRoot + "/" + ouNorth + "/" + ouFac},
		Sibling:  testkit.OrgUnit{ID: ouSib, Name: "North health post", Code: "SYN_N2", Path: "/" + ouRoot + "/" + ouNorth + "/" + ouSib},
		District: testkit.OrgUnit{ID: ouNorth, Name: "North district", Code: "SYN_N", Path: "/" + ouRoot + "/" + ouNorth},
		Programs: []testkit.Program{
			{ID: prMat, Name: "Synthetic maternal care", TET: tetPerson, AccessLevel: "OPEN", Stages: []string{stAnc, stDel, stLab},
				RestrictedStage: stLab, SearchAttr: atFirst, UniqueAttr: atNatID, GeneratedAttr: atPatNo, DistrictSees: true,
				RequiredAttrs: map[string]string{atFirst: "Test", atLast: "Person"},
				EventValues:   map[string]map[string]string{stAnc: {deWeight: "61"}, stDel: {}, stLab: {deLab: "ok"}}},
			{ID: prHiv, Name: "Synthetic HIV care", TET: tetPerson, AccessLevel: "PROTECTED", Stages: []string{stIni, stFup},
				SearchAttr: atFirst, GeneratedAttr: atArtNo,
				RequiredAttrs: map[string]string{atFirst: "Test", atLast: "Person"},
				EventValues:   map[string]map[string]string{stIni: {deHivRes: "POS"}, stFup: {}}},
			{ID: prTb, Name: "Synthetic TB treatment", TET: tetPerson, AccessLevel: "CLOSED", Stages: []string{stTbV},
				SearchAttr: atFirst, UniqueAttr: atTbNo,
				RequiredAttrs: map[string]string{atFirst: "Test", atLast: "Person"},
				EventValues:   map[string]map[string]string{stTbV: {}}},
			{ID: prCom, Name: "Synthetic community outreach", TET: tetPerson, AccessLevel: "OPEN", Stages: []string{stCom},
				SearchAttr: atFirst, RequiredAttrs: map[string]string{atFirst: "Test"},
				EventValues: map[string]map[string]string{stCom: {}}},
		},
		Users: map[string]testkit.User{},
	}
	for i, u := range roles {
		m.Users[roleNames[i]] = testkit.User{Username: u.username, UID: u.uid}
	}
	for _, u := range matrix {
		m.Matrix = append(m.Matrix, testkit.MatrixUser{User: testkit.User{Username: u.username, UID: u.uid}, Dims: u.dims})
	}
	return metadata, m
}

// describe is a short summary of a matrix user's settings, for test names.
func describe(dims map[string]string) string {
	var parts []string
	for _, d := range matrixDims {
		parts = append(parts, d.Name+"="+dims[d.Name])
	}
	return strings.Join(parts, " ")
}
