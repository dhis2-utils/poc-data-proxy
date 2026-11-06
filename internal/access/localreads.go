package access

import "slices"

// LocalReads lists the programs whose data the hub answers from its own copy even
// while central is reachable (facility-hub-config/localReads). Staff's screens then
// do not wait on the uplink; saves still go to central. Empty: everything passes
// through while online, as before (docs/CONNECTIVITY.md, docs/PERFORMANCE.md "A slow
// uplink").
type LocalReads struct {
	Programs []string `json:"programs"`
}

func (l LocalReads) Has(program string) bool {
	return program != "" && slices.Contains(l.Programs, program)
}

// FacilityOnly reports whether the user's search and capture scopes are this facility
// alone: central's orgUnitMode=ACCESSIBLE and CAPTURE then cover exactly the data the
// hub holds.
func (l *List) FacilityOnly(facility string) bool {
	if l == nil || facility == "" || len(l.OrgUnits) == 0 {
		return false
	}
	for _, scope := range [][]string{l.OrgUnits, l.SearchOrgUnits} {
		for _, id := range scope {
			if id != facility {
				return false
			}
		}
	}
	return true
}
