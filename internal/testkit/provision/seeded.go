package provision

import (
	"fmt"
	"log"
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
				if de.Compulsory {
					vals[de.DataElement.ID] = sampleValue(de.DataElement, facility)
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
					mp.UniqueAttr = ta.ID
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

	acc := buildAccess(l)
	m.SetupGroup, m.Users, m.Matrix = acc.Setup, acc.Roles, acc.Matrix
	md := map[string]any{"userRoles": []any{acc.Role}, "userGroups": acc.Groups, "users": acc.Users}
	log.Printf("seeded: importing %d users", len(acc.Users))
	if err := importMetadata(c, md); err != nil {
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

// seededAggregate prepares Child Health for the aggregate tests when the facility has
// it: data write for the role users, and a compulsory field (OPV2 <1y outreach)
// needed for completion.
func seededAggregate(c *testkit.Central, m *testkit.Manifest) error {
	var ds struct {
		DataSets []struct {
			ID string `json:"id"`
		} `json:"dataSets"`
	}
	if err := c.JSON("GET", "/api/dataSets?fields=id&filter=id:eq:"+childHealth+"&filter=organisationUnits.id:eq:"+
		m.Facility.ID, nil, &ds); err != nil || len(ds.DataSets) == 0 {
		return err
	}
	g := &sharing{}
	for _, u := range m.Users {
		g.grant(u.UID, "", acWrite)
	}
	if err := addSharing(c, "dataSet", childHealth, g, false); err != nil {
		return err
	}
	const coc = "V6L425pT3A0" // Outreach, <1y
	if err := c.JSON("PATCH", "/api/dataSets/"+childHealth, []map[string]any{
		{"op": "replace", "path": "/compulsoryFieldsCompleteOnly", "value": true},
		{"op": "add", "path": "/compulsoryDataElementOperands", "value": []any{map[string]any{
			"dataElement": map[string]any{"id": "O05mAByOgAv"}, "categoryOptionCombo": map[string]any{"id": coc}}}},
	}, nil); err != nil {
		return fmt.Errorf("child health: %w", err)
	}
	m.Aggregate = &testkit.Aggregate{DataSet: childHealth, Number: "fClA2Erf6IO", Integer: "s46m5MS0hxu",
		Required: "O05mAByOgAv", COC: coc, DefaultAOC: "HllvX50cXC0",
		NumberLabel: "Penta1 doses given", IntegerLabel: "BCG doses given", Column: 3}
	return nil
}
