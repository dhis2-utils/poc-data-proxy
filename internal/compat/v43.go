package compat

// v43 is DHIS2 2.43: the 2.42 shapes, except where the live suite against 2.43
// showed otherwise.
type v43 struct{ v42 }

func (v43) Major() int { return 43 }

// TransferOrgUnit: 2.43 takes orgUnit= and answers 400 "Required parameter
// 'orgUnit' is not present" to ou=.
func (v43) TransferOrgUnit() string { return "orgUnit" }
