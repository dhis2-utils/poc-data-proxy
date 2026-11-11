// Package provision turns a DHIS2 instance into a test setup and writes its manifest
// (docs/TESTING.md). Profiles: Synthetic builds everything on an empty instance;
// the seeded profiles adapt a demo database.
package provision

import (
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
	prEvD = "SynPrgEvD01" // WITHOUT_REGISTRATION, donor attribute combination

	stAnc = "SynStAnc001" // repeatable
	stDel = "SynStDel001"
	stLab = "SynStLab001" // restricted
	stIni = "SynStIni001"
	stFup = "SynStFup001" // repeatable
	stTbV = "SynStTbV001"
	stCom = "SynStCom001"
	stEvt = "SynStEvt001"
	stEvD = "SynStEvD001"

	rtMotherChild = "SynRtMoChi1"

	// A second tracked entity type, and a program whose events carry an attribute
	// option combo: who sees an event depends on access to its category option.
	tetHousehold = "SynTetHhld1"
	atHhID       = "SynAtHhId01" // unique
	atVillage    = "SynAtVilla1"
	prHhs        = "SynPrgHhs01"
	stHhs        = "SynStHhs001"
	coDonorA     = "SynCoDonA01" // shared per the matrix "option" dimension
	coDonorB     = "SynCoDonB01" // shared with no test user
	caDonor      = "SynCaDonor1"
	ccDonor      = "SynCcDonor1"

	// Aggregate (docs/superpowers/specs/2026-10-15-aggregate-data-entry-design.md).
	deAggNum   = "SynDeAggNm1"
	deAggInt   = "SynDeAggIn1"
	deAggReq   = "SynDeAggRq1"
	dsMonth    = "SynDsMonth1" // compulsory field
	dsValid    = "SynDsValid1" // validCompleteOnly
	dsDonor    = "SynDsDonor1" // attribute combination ccDonor (category options shared per user)
	defaultCOC = "HllvX50cXC0" // DHIS2's default category option combination, the same everywhere
	// A category combination override: deAggSex has the default combination, and
	// dsMonth disaggregates it by sex.
	deAggSex = "SynDeAggSx1"
	coMale   = "SynCoMale01"
	coFemale = "SynCoFeml01"
	caSex    = "SynCaSex001"
	ccSex    = "SynCcSex001"

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
	{"option", []string{"none", "read", "write"}}, // the shared category option (donor A)
}

var accessOf = map[string]string{"none": acNone, "metadata": acMeta, "read": acRead, "write": acWrite}

func withCombo(p map[string]any, tet, combo string) map[string]any {
	p["trackedEntityType"] = ref(tet)
	p["categoryCombo"] = ref(combo)
	return p
}

// metadata builds the whole synthetic setup as one /api/metadata payload, and the
// manifest that describes it.
func syntheticMetadata() (map[string]any, *testkit.Manifest) {
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
		tea(atHhID, "household ID", "TEXT", map[string]any{"unique": true}),
		tea(atVillage, "village", "TEXT", nil),
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

	// Role users and the access matrix (access.go), on this setup's programs.
	l := &Layout{Prefix: "Syn", UserPrefix: "syn_", Root: ouRoot, District: ouNorth, OtherDistrict: ouSouth,
		Facility: ouFac, Sibling: ouSib, RestrictedStage: stLab, RelationshipTypes: []string{rtMotherChild},
		Programs: []ProgramLayout{{prMat, tetPerson, []string{stAnc, stDel, stLab}}, {prHiv, tetPerson, []string{stIni, stFup}},
			{prTb, tetPerson, []string{stTbV}}, {prCom, tetPerson, []string{stCom}}, {prEvt, "", []string{stEvt}}, {prEvD, "", []string{stEvD}},
			{prHhs, tetHousehold, []string{stHhs}}},
		SharedOption: coDonorA, HiddenOption: coDonorB}
	l.Open, l.Protected, l.Closed = &l.Programs[0], &l.Programs[1], &l.Programs[2]
	acc := buildAccess(l)
	sh := func(id string) *sharing {
		if acc.Sharing[id] == nil {
			acc.Sharing[id] = &sharing{}
		}
		return acc.Sharing[id]
	}
	sh(prCom).Public, sh(stCom).Public = acWrite, acWrite // public data access
	sh(rtMotherChild).Public = acMeta
	sh(coDonorA).Public, sh(coDonorB).Public = acMeta, acMeta // metadata only: data access comes from grants

	ptea := func(id string, mandatory bool) map[string]any {
		return map[string]any{"trackedEntityAttribute": ref(id), "mandatory": mandatory, "searchable": true,
			"displayInList": true}
	}
	program := func(id, name, level string, attrs []any, stages []string) map[string]any {
		return map[string]any{"id": id, "name": "Synthetic " + name, "shortName": "Syn " + name,
			"programType": "WITH_REGISTRATION", "trackedEntityType": ref(tetPerson), "accessLevel": level,
			"onlyEnrollOnce": false, "displayIncidentDate": true, "displayFrontPageList": true, "programTrackedEntityAttributes": attrs,
			"programStages": refs(stages...), "organisationUnits": refs(facilities...), "sharing": sh(id).json()}
	}
	programs := []any{
		program(prMat, "maternal care", "OPEN", []any{ptea(atFirst, true), ptea(atLast, true), ptea(atNatID, false),
			ptea(atSex, false), ptea(atDob, false), ptea(atPhone, false), ptea(atPatNo, false)}, []string{stAnc, stDel, stLab}),
		program(prHiv, "HIV care", "PROTECTED", []any{ptea(atFirst, true), ptea(atLast, true), ptea(atArtNo, false),
			ptea(atNatID, false)}, []string{stIni, stFup}),
		program(prTb, "TB treatment", "CLOSED", []any{ptea(atFirst, true), ptea(atLast, true), ptea(atTbNo, false)},
			[]string{stTbV}),
		program(prCom, "community outreach", "OPEN", []any{ptea(atFirst, true), ptea(atPhone, false)}, []string{stCom}),
		withCombo(program(prHhs, "household survey", "OPEN", []any{ptea(atHhID, false), ptea(atVillage, true)},
			[]string{stHhs}), tetHousehold, ccDonor),
		map[string]any{"id": prEvt, "name": "Synthetic facility events", "shortName": "Syn facility events",
			"programType": "WITHOUT_REGISTRATION", "programStages": refs(stEvt), "organisationUnits": refs(facilities...),
			"sharing": sh(prEvt).json()},
		map[string]any{"id": prEvD, "name": "Synthetic donor events", "shortName": "Syn donor events",
			"programType": "WITHOUT_REGISTRATION", "programStages": refs(stEvD), "organisationUnits": refs(facilities...),
			"categoryCombo": ref(ccDonor), "sharing": sh(prEvD).json()},
	}
	psde := func(id string, compulsory bool) map[string]any {
		return map[string]any{"dataElement": ref(id), "compulsory": compulsory}
	}
	stage := func(id, name, program string, order int, repeatable bool, des []any) map[string]any {
		return map[string]any{"id": id, "name": "Synthetic " + name, "program": ref(program), "sortOrder": order,
			"repeatable": repeatable, "programStageDataElements": des, "sharing": sh(id).json()}
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
		stage(stEvD, "donor event", prEvD, 1, true, []any{psde(deNote, false)}),
		stage(stHhs, "household visit", prHhs, 1, true, []any{psde(deNote, false)}),
	}

	everyone := (&sharing{Public: acWrite}).json() // disaggregation options: anyone enters data for them
	aggDE := func(id, name, vt string) map[string]any {
		return map[string]any{"id": id, "name": "Syn agg " + name, "shortName": "Syn agg " + name, "valueType": vt,
			"domainType": "AGGREGATE", "aggregationType": "SUM", "sharing": meta}
	}
	dataElements = append(dataElements, aggDE(deAggNum, "number", "NUMBER"), aggDE(deAggInt, "integer", "INTEGER"),
		aggDE(deAggReq, "required", "NUMBER"), aggDE(deAggSex, "by sex", "NUMBER"))
	aggDS := func(id, name string, extra map[string]any) map[string]any {
		sh(id).Public = acWrite
		d := map[string]any{"id": id, "name": name, "shortName": name, "periodType": "Monthly", "openFuturePeriods": 1,
			"dataSetElements": []any{
				map[string]any{"dataElement": ref(deAggNum), "dataSet": ref(id)},
				map[string]any{"dataElement": ref(deAggInt), "dataSet": ref(id)},
				map[string]any{"dataElement": ref(deAggReq), "dataSet": ref(id)}},
			"organisationUnits": refs(ouFac, ouSib), "sharing": sh(id).json()}
		for k, v := range extra {
			d[k] = v
		}
		return d
	}
	dataSets := []any{
		aggDS(dsMonth, "Syn monthly", map[string]any{"compulsoryFieldsCompleteOnly": true,
			"dataSetElements": []any{
				map[string]any{"dataElement": ref(deAggNum), "dataSet": ref(dsMonth)},
				map[string]any{"dataElement": ref(deAggInt), "dataSet": ref(dsMonth)},
				map[string]any{"dataElement": ref(deAggReq), "dataSet": ref(dsMonth)},
				map[string]any{"dataElement": ref(deAggSex), "dataSet": ref(dsMonth), "categoryCombo": ref(ccSex)}},
			"compulsoryDataElementOperands": []any{map[string]any{"dataElement": ref(deAggReq),
				"categoryOptionCombo": ref(defaultCOC)}}}),
		aggDS(dsValid, "Syn validated", map[string]any{"validCompleteOnly": true}),
		aggDS(dsDonor, "Syn donor", map[string]any{"categoryCombo": ref(ccDonor)}),
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
			"sharing": sh(tetPerson).json()},
			map[string]any{"id": tetHousehold, "name": "Synthetic household", "shortName": "Syn household",
				"trackedEntityTypeAttributes": []any{
					map[string]any{"trackedEntityAttribute": ref(atHhID), "searchable": true, "displayInList": true},
					map[string]any{"trackedEntityAttribute": ref(atVillage), "searchable": true, "displayInList": true}},
				"sharing": sh(tetHousehold).json()}},
		"categoryOptions": []any{
			map[string]any{"id": coDonorA, "name": "Synthetic donor A", "shortName": "Syn donor A", "sharing": sh(coDonorA).json()},
			map[string]any{"id": coDonorB, "name": "Synthetic donor B", "shortName": "Syn donor B", "sharing": sh(coDonorB).json()},
			map[string]any{"id": coMale, "name": "Synthetic male", "shortName": "Syn male", "sharing": everyone},
			map[string]any{"id": coFemale, "name": "Synthetic female", "shortName": "Syn female", "sharing": everyone}},
		"categories": []any{map[string]any{"id": caDonor, "name": "Synthetic donor", "shortName": "Syn donor",
			"dataDimensionType": "ATTRIBUTE", "categoryOptions": refs(coDonorA, coDonorB), "sharing": meta},
			map[string]any{"id": caSex, "name": "Synthetic sex", "shortName": "Syn sex",
				"dataDimensionType": "DISAGGREGATION", "categoryOptions": refs(coMale, coFemale), "sharing": meta}},
		"categoryCombos": []any{map[string]any{"id": ccDonor, "name": "Synthetic donor", "dataDimensionType": "ATTRIBUTE",
			"categories": refs(caDonor), "sharing": meta},
			map[string]any{"id": ccSex, "name": "Synthetic sex", "dataDimensionType": "DISAGGREGATION",
				"categories": refs(caSex), "sharing": meta}},
		"dataElements":  dataElements,
		"dataSets":      dataSets,
		"programs":      programs,
		"programStages": stages,
		"relationshipTypes": []any{map[string]any{"id": rtMotherChild, "name": "Synthetic mother of",
			"fromToName": "mother of", "toFromName": "child of", "bidirectional": false,
			"fromConstraint": map[string]any{"relationshipEntity": "TRACKED_ENTITY_INSTANCE", "trackedEntityType": ref(tetPerson)},
			"toConstraint":   map[string]any{"relationshipEntity": "TRACKED_ENTITY_INSTANCE", "trackedEntityType": ref(tetPerson)},
			"sharing":        sh(rtMotherChild).json()}},
		"userRoles":  []any{acc.Role},
		"userGroups": acc.Groups,
		"users":      acc.Users,
	}

	m := &testkit.Manifest{
		Profile: "synthetic", Password: Password, SetupGroup: acc.Setup,
		Facility: testkit.OrgUnit{ID: ouFac, Name: "North clinic", Code: "SYN_N1", Path: "/" + ouRoot + "/" + ouNorth + "/" + ouFac},
		Sibling:  testkit.OrgUnit{ID: ouSib, Name: "North health post", Code: "SYN_N2", Path: "/" + ouRoot + "/" + ouNorth + "/" + ouSib},
		District: testkit.OrgUnit{ID: ouNorth, Name: "North district", Code: "SYN_N", Path: "/" + ouRoot + "/" + ouNorth},
		Programs: []testkit.Program{
			{ID: prMat, Name: "Synthetic maternal care", TET: tetPerson, AccessLevel: "OPEN", Stages: []string{stAnc, stDel, stLab},
				RestrictedStage: stLab, SearchAttr: atFirst, UniqueAttr: atNatID, GeneratedAttr: atPatNo, DistrictSees: true,
				OnlyAttrs:     []string{atSex, atDob, atPatNo},
				RequiredAttrs: map[string]string{atFirst: "Test", atLast: "Person"},
				EventValues:   map[string]map[string]string{stAnc: {deWeight: "61"}, stDel: {}, stLab: {deLab: "ok"}}},
			{ID: prHiv, Name: "Synthetic HIV care", TET: tetPerson, AccessLevel: "PROTECTED", Stages: []string{stIni, stFup},
				SearchAttr: atFirst, GeneratedAttr: atArtNo, OnlyAttrs: []string{atArtNo},
				RequiredAttrs: map[string]string{atFirst: "Test", atLast: "Person"},
				EventValues:   map[string]map[string]string{stIni: {deHivRes: "POS"}, stFup: {}}},
			{ID: prTb, Name: "Synthetic TB treatment", TET: tetPerson, AccessLevel: "CLOSED", Stages: []string{stTbV},
				SearchAttr: atFirst, UniqueAttr: atTbNo, OnlyAttrs: []string{atTbNo},
				RequiredAttrs: map[string]string{atFirst: "Test", atLast: "Person"},
				EventValues:   map[string]map[string]string{stTbV: {}}},
			{ID: prCom, Name: "Synthetic community outreach", TET: tetPerson, AccessLevel: "OPEN", Stages: []string{stCom},
				SearchAttr: atFirst, RequiredAttrs: map[string]string{atFirst: "Test"},
				EventValues: map[string]map[string]string{stCom: {}}},
			{ID: prHhs, Name: "Synthetic household survey", TET: tetHousehold, AccessLevel: "OPEN", Stages: []string{stHhs},
				SearchAttr: atVillage, UniqueAttr: atHhID, EventValues: map[string]map[string]string{stHhs: {}}},
		},
		Users: map[string]testkit.User{},
	}
	m.Users, m.Matrix = acc.Roles, acc.Matrix
	return metadata, m
}
