package provision

import (
	"fmt"
	"log"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/dhis2/facility-hub/internal/testkit"
)

// SeededOptions tune the seeded profiles.
type SeededOptions struct {
	Name     string // profile name: sierra-leone, laos
	Prefix   string // 3-letter UID prefix for created objects
	Facility string // force a facility; empty picks the one with the most tracker data
	Programs int    // at most this many tracker programs (default 4)
	// KeepPublic leaves the demo database's public sharing alone. By default public
	// data access is lowered to metadata-only on the chosen programs, stages and
	// types, so the access matrix decides who sees what.
	KeepPublic bool
}

type seedAttr struct {
	ID        string `json:"id"`
	ValueType string `json:"valueType"`
	Unique    bool   `json:"unique"`
	Generated bool   `json:"generated"`
	OptionSet *struct {
		Options []struct {
			Code string `json:"code"`
		} `json:"options"`
	} `json:"optionSet"`
}

type seedProgram struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	AccessLevel       string `json:"accessLevel"`
	TrackedEntityType struct {
		ID                          string `json:"id"`
		TrackedEntityTypeAttributes []struct {
			TrackedEntityAttribute seedAttr `json:"trackedEntityAttribute"`
		} `json:"trackedEntityTypeAttributes"`
	} `json:"trackedEntityType"`
	ProgramStages []struct {
		ID                       string `json:"id"`
		SortOrder                int    `json:"sortOrder"`
		ProgramStageDataElements []struct {
			Compulsory  bool     `json:"compulsory"`
			DataElement seedAttr `json:"dataElement"`
		} `json:"programStageDataElements"`
	} `json:"programStages"`
	ProgramTrackedEntityAttributes []struct {
		Mandatory              bool     `json:"mandatory"`
		Searchable             bool     `json:"searchable"`
		TrackedEntityAttribute seedAttr `json:"trackedEntityAttribute"`
	} `json:"programTrackedEntityAttributes"`
}

// Seeded adapts a demo database: it finds a facility with tracker data, maps the
// access matrix onto its programs, creates the users and sharing, and describes
// the result in a manifest. The database's own metadata, program rules and data stay.
func Seeded(c *testkit.Central, o SeededOptions) (*testkit.Manifest, error) {
	if o.Programs == 0 {
		o.Programs = 4
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := c.JSON("GET", "/api/system/info?fields=version", nil, &info); err != nil {
		return nil, err
	}

	var res struct {
		Programs []seedProgram `json:"programs"`
	}
	af := "id,valueType,unique,generated,optionSet[options[code]]"
	fields := "id,name,accessLevel,trackedEntityType[id,trackedEntityTypeAttributes[trackedEntityAttribute[" + af + "]]]," +
		"programStages[id,sortOrder,programStageDataElements[compulsory,dataElement[" + af + "]]]," +
		"programTrackedEntityAttributes[mandatory,searchable,trackedEntityAttribute[" + af + "]]"
	if err := c.JSON("GET", "/api/programs?paging=false&filter=programType:eq:WITH_REGISTRATION&fields="+url.QueryEscape(fields), nil, &res); err != nil {
		return nil, err
	}
	byID := map[string]*seedProgram{}
	for i := range res.Programs {
		byID[res.Programs[i].ID] = &res.Programs[i]
	}
	mandatory, err := ruleMandatory(c)
	if err != nil {
		return nil, err
	}

	// Where is the data? Count tracked entities per owning org unit and program from a
	// sample of each program.
	count := map[string]map[string]int{} // org unit -> program -> tracked entities
	for _, p := range res.Programs {
		var page struct {
			TEs []struct {
				ProgramOwners []struct {
					OrgUnit string `json:"orgUnit"`
					Program string `json:"program"`
				} `json:"programOwners"`
			} `json:"trackedEntities"`
		}
		q := url.Values{"program": {p.ID}, "orgUnitMode": {"ALL"}, "pageSize": {"1000"}, "fields": {"programOwners[orgUnit,program]"}}
		if err := c.JSON("GET", "/api/tracker/trackedEntities?"+q.Encode(), nil, &page); err != nil {
			log.Printf("seeded: skipping program %s: %v", p.ID, err)
			continue
		}
		for _, te := range page.TEs {
			for _, ow := range te.ProgramOwners {
				if ow.Program == p.ID {
					if count[ow.OrgUnit] == nil {
						count[ow.OrgUnit] = map[string]int{}
					}
					count[ow.OrgUnit][p.ID]++
				}
			}
		}
	}
	facility := o.Facility
	if facility == "" {
		best := -1
		for ou, ps := range count {
			score := 0
			for _, n := range ps {
				score += min(n, 50) // breadth over depth: several programs with data beat one big one
			}
			if score > best || (score == best && ou < facility) {
				best, facility = score, ou
			}
		}
	}
	if facility == "" {
		return nil, fmt.Errorf("no tracker data found")
	}

	type ouInfo struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Code     string `json:"code"`
		Path     string `json:"path"`
		Children []struct {
			ID string `json:"id"`
		} `json:"children"`
	}
	getOU := func(id string) (*ouInfo, error) {
		var x ouInfo
		err := c.JSON("GET", "/api/organisationUnits/"+id+"?fields=id,name,code,path,children[id]", nil, &x)
		return &x, err
	}
	fac, err := getOU(facility)
	if err != nil {
		return nil, err
	}
	path := strings.Split(strings.Trim(fac.Path, "/"), "/")
	if len(path) < 3 {
		return nil, fmt.Errorf("facility %s is too high in the hierarchy", facility)
	}
	district, err := getOU(path[len(path)-2])
	if err != nil {
		return nil, err
	}
	grand, err := getOU(path[len(path)-3])
	if err != nil {
		return nil, err
	}
	pick := func(children []struct {
		ID string `json:"id"`
	}, not string) string {
		bestID, best := "", -1
		for _, ch := range children {
			n := 0
			for _, v := range count[ch.ID] {
				n += v
			}
			if ch.ID != not && n > best {
				bestID, best = ch.ID, n
			}
		}
		return bestID
	}
	sibID := pick(district.Children, facility)
	other := pick(grand.Children, district.ID)
	if sibID == "" || other == "" {
		return nil, fmt.Errorf("facility %s has no sibling or its district no sibling district", facility)
	}
	sib, err := getOU(sibID)
	if err != nil {
		return nil, err
	}

	// The facility's programs with data, busiest first.
	var progs []*seedProgram
	for pid := range count[facility] {
		if byID[pid] != nil {
			progs = append(progs, byID[pid])
		}
	}
	sort.Slice(progs, func(i, j int) bool {
		a, b := count[facility][progs[i].ID], count[facility][progs[j].ID]
		return a > b || (a == b && progs[i].ID < progs[j].ID)
	})
	if len(progs) > o.Programs {
		progs = progs[:o.Programs]
	}
	log.Printf("seeded: facility %s (%s), district %s, sibling %s, other district %s, %d programs",
		fac.Name, fac.ID, district.ID, sib.ID, other, len(progs))

	l := &Layout{Prefix: o.Prefix, UserPrefix: strings.ToLower(o.Prefix) + "_", Root: path[0], District: district.ID,
		OtherDistrict: other, Facility: facility, Sibling: sib.ID}
	m := &testkit.Manifest{Profile: o.Name, DHIS2Version: info.Version, CentralURL: c.Base, AdminUser: c.User,
		AdminPassword: c.Password, Password: Password,
		Facility: testkit.OrgUnit{ID: fac.ID, Name: fac.Name, Code: fac.Code, Path: fac.Path},
		Sibling:  testkit.OrgUnit{ID: sib.ID, Name: sib.Name, Code: sib.Code, Path: sib.Path},
		District: testkit.OrgUnit{ID: district.ID, Name: district.Name, Code: district.Code, Path: district.Path}}
	for _, p := range progs {
		stages := slices.Clone(p.ProgramStages)
		sort.Slice(stages, func(i, j int) bool { return stages[i].SortOrder < stages[j].SortOrder })
		pl := ProgramLayout{ID: p.ID, TET: p.TrackedEntityType.ID}
		mp := testkit.Program{ID: p.ID, Name: p.Name, TET: p.TrackedEntityType.ID, AccessLevel: p.AccessLevel,
			RequiredAttrs: map[string]string{}, EventValues: map[string]map[string]string{}}
		for _, st := range stages {
			pl.Stages = append(pl.Stages, st.ID)
			mp.Stages = append(mp.Stages, st.ID)
			vals := map[string]string{}
			for _, de := range st.ProgramStageDataElements {
				if de.Compulsory || mandatory[de.DataElement.ID] {
					vals[de.DataElement.ID] = sampleEventValue(de.DataElement, facility)
				}
			}
			mp.EventValues[st.ID] = vals
		}
		for _, a := range p.ProgramTrackedEntityAttributes {
			ta := a.TrackedEntityAttribute
			switch {
			case ta.Generated:
				if mp.GeneratedAttr == "" {
					mp.GeneratedAttr = ta.ID
				}
			case ta.Unique:
				if mp.UniqueAttr == "" || a.Mandatory {
					if mp.UniqueAttr != "" {
						mp.OtherUnique = append(mp.OtherUnique, mp.UniqueAttr)
					}
					mp.UniqueAttr = ta.ID
				} else {
					mp.OtherUnique = append(mp.OtherUnique, ta.ID)
				}
			case a.Searchable && ta.OptionSet == nil && (ta.ValueType == "TEXT" || ta.ValueType == "LONG_TEXT") && mp.SearchAttr == "":
				mp.SearchAttr = ta.ID
			}
			if a.Mandatory && !ta.Generated && !ta.Unique {
				mp.RequiredAttrs[ta.ID] = sampleValue(ta, facility)
			}
			if a.Mandatory && ta.Generated {
				mp.MandatoryGenerated = append(mp.MandatoryGenerated, ta.ID)
			}
		}
		l.Programs = append(l.Programs, pl)
		m.Programs = append(m.Programs, mp)
	}
	l.Open = &l.Programs[0]
	m.Programs[0].DistrictSees = true
	if st := l.Open.Stages; len(st) > 1 {
		l.RestrictedStage = st[len(st)-1]
		m.Programs[0].RestrictedStage = l.RestrictedStage
	}
	// The protected and closed dimensions go to programs with that access level if
	// there are any, else to the next busiest programs.
	rest := l.Programs[1:]
	take := func(level string) *ProgramLayout {
		for i := range rest {
			if byID[rest[i].ID].AccessLevel == level {
				p := &rest[i]
				rest = append(rest[:i:i], rest[i+1:]...)
				return p
			}
		}
		if len(rest) > 0 {
			p := &rest[0]
			rest = rest[1:]
			return p
		}
		return nil
	}
	l.Protected, l.Closed = take("PROTECTED"), take("CLOSED")
	// Event programs at the facility, granted like the tracker programs.
	evps, err := seededEventPrograms(c, facility)
	if err != nil {
		return nil, err
	}
	for _, ep := range evps {
		l.Programs = append(l.Programs, ProgramLayout{ID: ep.ID, Stages: ep.Stages})
		m.EventPrograms = append(m.EventPrograms, ep)
	}

	// Relationship types between two people of the programs' types, granted as the
	// types are: the tests relate people (C7), and a seed may share them with nobody
	// (Laos).
	var rts struct {
		Types []struct {
			ID                           string
			FromConstraint, ToConstraint struct {
				TrackedEntityType *struct{ ID string } `json:"trackedEntityType"`
			}
		} `json:"relationshipTypes"`
	}
	if err := c.JSON("GET", "/api/relationshipTypes?paging=false&fields=id,fromConstraint[trackedEntityType[id]],toConstraint[trackedEntityType[id]]", nil, &rts); err != nil {
		return nil, err
	}
	for _, rt := range rts.Types {
		f, to := rt.FromConstraint.TrackedEntityType, rt.ToConstraint.TrackedEntityType
		if f != nil && to != nil && slices.Contains(l.tets(), f.ID) && slices.Contains(l.tets(), to.ID) {
			l.RelationshipTypes = append(l.RelationshipTypes, rt.ID)
		}
	}
	acc := buildAccess(l)
	m.SetupGroup, m.Users, m.Matrix = acc.Setup, acc.Roles, acc.Matrix
	m.AccountsGroup, m.AccountsRole = acc.AccountsGroup, acc.AccountsRole
	md := map[string]any{"userRoles": []any{acc.Role, acc.AccountsRoleMeta}, "userGroups": acc.Groups, "users": acc.Users}
	log.Printf("seeded: importing %d users", len(acc.Users))
	if err := importMetadata(c, md); err != nil {
		return nil, err
	}
	if err := nameHubGroups(c, m); err != nil {
		return nil, err
	}
	types := map[string]string{}
	for _, p := range l.Programs {
		types[p.ID] = "program"
		types[p.TET] = "trackedEntityType"
		for _, s := range p.Stages {
			types[s] = "programStage"
		}
	}
	for _, rt := range l.RelationshipTypes {
		types[rt] = "relationshipType"
	}
	for id, grants := range acc.Sharing {
		if err := addSharing(c, types[id], id, grants, !o.KeepPublic); err != nil {
			return nil, err
		}
	}
	if err := seededAggregate(c, m); err != nil {
		return nil, err
	}
	return m, verifyLogins(c, m)
}

// addSharing adds grants to an object's existing sharing, optionally lowering public
// access to metadata-only.
func addSharing(c *testkit.Central, typ, id string, g *sharing, restrictPublic bool) error {
	if typ == "" {
		return nil
	}
	var cur struct {
		Object struct {
			PublicAccess      string                        `json:"publicAccess"`
			UserAccesses      []struct{ ID, Access string } `json:"userAccesses"`
			UserGroupAccesses []struct{ ID, Access string } `json:"userGroupAccesses"`
		} `json:"object"`
	}
	if err := c.JSON("GET", "/api/sharing?type="+typ+"&id="+id, nil, &cur); err != nil {
		return err
	}
	users, groups := map[string]string{}, map[string]string{}
	for _, a := range cur.Object.UserAccesses {
		users[a.ID] = a.Access
	}
	for _, a := range cur.Object.UserGroupAccesses {
		groups[a.ID] = a.Access
	}
	for k, v := range g.Users {
		users[k] = v
	}
	for k, v := range g.Groups {
		groups[k] = v
	}
	pub := cur.Object.PublicAccess
	if restrictPublic {
		pub = acMeta
	}
	ua, ga := []any{}, []any{} // null makes 2.42 answer 500
	for k, v := range users {
		ua = append(ua, map[string]any{"id": k, "access": v})
	}
	for k, v := range groups {
		ga = append(ga, map[string]any{"id": k, "access": v})
	}
	return c.JSON("POST", "/api/sharing?type="+typ+"&id="+id, map[string]any{"object": map[string]any{
		"publicAccess": pub, "externalAccess": false, "userAccesses": ua, "userGroupAccesses": ga}}, nil)
}

// sampleValue is a valid value for a mandatory attribute or compulsory data element.
// ruleMandatory lists the data elements a program rule makes mandatory
// (SETMANDATORYFIELD). Central enforces these on completed events when the rule's
// condition holds, and the tests' sample values can make it hold (Laos: E1301), so
// the tests' events fill them as they fill compulsory ones.
func ruleMandatory(c *testkit.Central) (map[string]bool, error) {
	var res struct {
		Actions []struct {
			DataElement *struct {
				ID string `json:"id"`
			} `json:"dataElement"`
		} `json:"programRuleActions"`
	}
	if err := c.JSON("GET", "/api/programRuleActions?paging=false&filter=programRuleActionType:eq:SETMANDATORYFIELD&fields=dataElement[id]", nil, &res); err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, a := range res.Actions {
		if a.DataElement != nil {
			m[a.DataElement.ID] = true
		}
	}
	return m, nil
}

// sampleEventValue is sampleValue for an event's data element: a date is the event's
// own (testkit.EventDate), as rules can compare it to the event date.
func sampleEventValue(a seedAttr, facility string) string {
	if a.OptionSet == nil && a.ValueType == "DATE" {
		return testkit.EventDate
	}
	return sampleValue(a, facility)
}

func sampleValue(a seedAttr, facility string) string {
	if a.OptionSet != nil && len(a.OptionSet.Options) > 0 {
		return a.OptionSet.Options[0].Code
	}
	switch a.ValueType {
	case "NUMBER", "INTEGER", "INTEGER_POSITIVE", "INTEGER_ZERO_OR_POSITIVE", "PERCENTAGE", "UNIT_INTERVAL":
		return "1"
	case "INTEGER_NEGATIVE":
		return "-1"
	case "DATE", "AGE":
		return "1990-01-01"
	case "DATETIME":
		return "1990-01-01T00:00:00.000"
	case "TIME":
		return "10:00"
	case "BOOLEAN", "TRUE_ONLY":
		return "true"
	case "PHONE_NUMBER":
		return "+23270000000"
	case "EMAIL":
		return "test@example.org"
	case "ORGANISATION_UNIT":
		return facility
	case "COORDINATE":
		return "[0,0]"
	case "URL":
		return "https://example.org"
	}
	return "Test"
}

// childHealth is Sierra Leone's Child Health data set: monthly, default attribute
// combination, Penta1 and BCG with the option combination "Outreach, <1y".
const childHealth = "BfMAe6Itzgt"

type seedCombo struct {
	ID   string `json:"id"`
	COCs []struct {
		ID string `json:"id"`
	} `json:"categoryOptionCombos"`
}

type seedDataSet struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	PeriodType    string `json:"periodType"`
	FormType      string `json:"formType"`
	CategoryCombo struct {
		ID        string `json:"id"`
		IsDefault bool   `json:"isDefault"`
	} `json:"categoryCombo"`
	Elements []struct {
		DataElement struct {
			ID        string    `json:"id"`
			FormName  string    `json:"displayFormName"`
			ValueType string    `json:"valueType"`
			CatCombo  seedCombo `json:"categoryCombo"`
		} `json:"dataElement"`
		CategoryCombo *seedCombo `json:"categoryCombo"`
	} `json:"dataSetElements"`
}

// seededAggregate prepares a data set at the facility for the aggregate tests: Child
// Health where the facility has it (Sierra Leone), otherwise the first monthly data
// set with the default attribute combination and a section or default form that has
// three numeric elements under one category combination. The role users get data
// write on it, and one element becomes compulsory for completion.
func seededAggregate(c *testkit.Central, m *testkit.Manifest) error {
	var res struct {
		DataSets []seedDataSet `json:"dataSets"`
	}
	fields := "id,name,periodType,formType,categoryCombo[id,isDefault],dataSetElements[categoryCombo[id,categoryOptionCombos[id]]," +
		"dataElement[id,displayFormName,valueType,categoryCombo[id,categoryOptionCombos[id]]]]"
	if err := c.JSON("GET", "/api/dataSets?paging=false&filter=organisationUnits.id:eq:"+
		m.Facility.ID+"&fields="+url.QueryEscape(fields), nil, &res); err != nil {
		return err
	}
	slices.SortFunc(res.DataSets, func(a, b seedDataSet) int {
		if (a.ID == childHealth) != (b.ID == childHealth) { // Child Health first where it is
			if a.ID == childHealth {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	numeric := func(vt string) bool { return vt == "NUMBER" || strings.HasPrefix(vt, "INTEGER") }
	for _, ds := range res.DataSets {
		if ds.PeriodType != "Monthly" || !ds.CategoryCombo.IsDefault || ds.FormType == "CUSTOM" {
			continue
		}
		type el struct{ id, label, vt string }
		groups := map[string][]el{} // effective category combination -> numeric elements
		cocOf := map[string]string{}
		for _, e := range ds.Elements {
			cc := e.DataElement.CatCombo
			if e.CategoryCombo != nil && e.CategoryCombo.ID != "" {
				cc = *e.CategoryCombo
			}
			if !numeric(e.DataElement.ValueType) || len(cc.COCs) == 0 {
				continue
			}
			groups[cc.ID] = append(groups[cc.ID], el{e.DataElement.ID, e.DataElement.FormName, e.DataElement.ValueType})
			cocOf[cc.ID] = cc.COCs[0].ID
		}
		for _, cc := range slices.Sorted(maps.Keys(groups)) {
			g := groups[cc]
			if len(g) < 3 {
				continue
			}
			slices.SortFunc(g, func(a, b el) int { return strings.Compare(a.id, b.id) })
			if ds.ID == childHealth { // the elements earlier runs and the spike used
				g = []el{{"fClA2Erf6IO", "Penta1 doses given", "NUMBER"}, {"s46m5MS0hxu", "BCG doses given", "INTEGER"},
					{"O05mAByOgAv", "OPV2 doses given", "NUMBER"}}
				cocOf[cc] = "V6L425pT3A0" // Outreach, <1y
			}
			return useAggregate(c, m, ds.ID, cocOf[cc], g[0].id, g[1].id, g[2].id, g[0].label, g[1].label,
				ds.ID == childHealth)
		}
	}
	log.Printf("seeded: no monthly data set at the facility suits the aggregate tests; they will skip")
	return nil
}

// useAggregate grants the role users data write on the data set, makes required
// compulsory for completion, and names it all in the manifest. labels: the rows of
// number and integer in the form; the real-app test needs their column (known for
// Child Health, 3; 1 for the default category combination).
func useAggregate(c *testkit.Central, m *testkit.Manifest, ds, coc, number, integer, required, numberLabel,
	integerLabel string, childHealthForm bool) error {
	g := &sharing{}
	for _, u := range m.Users {
		g.grant(u.UID, "", acWrite)
	}
	if err := addSharing(c, "dataSet", ds, g, false); err != nil {
		return err
	}
	if err := c.JSON("PATCH", "/api/dataSets/"+ds, []map[string]any{
		{"op": "replace", "path": "/compulsoryFieldsCompleteOnly", "value": true},
		{"op": "add", "path": "/compulsoryDataElementOperands", "value": []any{map[string]any{
			"dataElement": map[string]any{"id": required}, "categoryOptionCombo": map[string]any{"id": coc}}}},
	}, nil); err != nil {
		return fmt.Errorf("aggregate data set %s: %w", ds, err)
	}
	m.Aggregate = &testkit.Aggregate{DataSet: ds, Number: number, Integer: integer, Required: required, COC: coc,
		DefaultAOC: "HllvX50cXC0"}
	switch {
	case childHealthForm:
		m.Aggregate.NumberLabel, m.Aggregate.IntegerLabel, m.Aggregate.Column = numberLabel, integerLabel, 3
	case coc == "HllvX50cXC0":
		m.Aggregate.NumberLabel, m.Aggregate.IntegerLabel, m.Aggregate.Column = numberLabel, integerLabel, 1
	}
	log.Printf("seeded: aggregate tests use data set %s (%s, %s, %s; option combination %s)", ds, number, integer,
		required, coc)
	return nil
}

// seededEventPrograms picks up to three event programs with events at the facility,
// those with an attribute category combination first, then the busiest; with the
// first two option combinations of their combination and a valid value for each
// compulsory data element.
func seededEventPrograms(c *testkit.Central, facility string) ([]testkit.Program, error) {
	var res struct {
		Programs []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			CategoryCombo struct {
				ID        string `json:"id"`
				IsDefault bool   `json:"isDefault"`
			} `json:"categoryCombo"`
			ProgramStages []struct {
				ID                       string `json:"id"`
				ProgramStageDataElements []struct {
					Compulsory  bool     `json:"compulsory"`
					DataElement seedAttr `json:"dataElement"`
				} `json:"programStageDataElements"`
			} `json:"programStages"`
		} `json:"programs"`
	}
	af := "id,valueType,unique,generated,optionSet[options[code]]"
	fields := "id,name,categoryCombo[id,isDefault],programStages[id,programStageDataElements[compulsory,dataElement[" + af + "]]]"
	if err := c.JSON("GET", "/api/programs?paging=false&filter=programType:eq:WITHOUT_REGISTRATION&filter=organisationUnits.id:eq:"+
		facility+"&fields="+url.QueryEscape(fields), nil, &res); err != nil {
		return nil, err
	}
	mandatory, err := ruleMandatory(c)
	if err != nil {
		return nil, err
	}
	type cand struct {
		p     testkit.Program
		n     int
		combo bool
	}
	var cands []cand
	for _, p := range res.Programs {
		var page struct {
			Pager struct {
				Total int `json:"total"`
			} `json:"pager"`
		}
		q := url.Values{"program": {p.ID}, "orgUnit": {facility}, "totalPages": {"true"}, "pageSize": {"1"}, "fields": {"event"}}
		if err := c.JSON("GET", "/api/tracker/events?"+q.Encode(), nil, &page); err != nil || page.Pager.Total == 0 {
			continue
		}
		tp := testkit.Program{ID: p.ID, Name: p.Name, Event: true, EventValues: map[string]map[string]string{}}
		for _, st := range p.ProgramStages {
			tp.Stages = append(tp.Stages, st.ID)
			vals := map[string]string{}
			for _, de := range st.ProgramStageDataElements {
				if de.Compulsory || mandatory[de.DataElement.ID] {
					vals[de.DataElement.ID] = sampleEventValue(de.DataElement, facility)
				}
			}
			tp.EventValues[st.ID] = vals
		}
		if !p.CategoryCombo.IsDefault {
			var cocs struct {
				Combos []struct {
					ID string `json:"id"`
				} `json:"categoryOptionCombos"`
			}
			if err := c.JSON("GET", "/api/categoryOptionCombos?paging=false&fields=id&filter=categoryCombo.id:eq:"+p.CategoryCombo.ID, nil, &cocs); err != nil {
				return nil, err
			}
			for _, x := range cocs.Combos {
				tp.AOCs = append(tp.AOCs, x.ID)
			}
			slices.Sort(tp.AOCs)
			tp.AOCs = tp.AOCs[:min(2, len(tp.AOCs))]
		}
		cands = append(cands, cand{tp, page.Pager.Total, !p.CategoryCombo.IsDefault})
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].combo != cands[j].combo {
			return cands[i].combo
		}
		return cands[i].n > cands[j].n || (cands[i].n == cands[j].n && cands[i].p.ID < cands[j].p.ID)
	})
	var out []testkit.Program
	for _, x := range cands[:min(3, len(cands))] {
		out = append(out, x.p)
	}
	log.Printf("seeded: %d event programs", len(out))
	return out, nil
}
