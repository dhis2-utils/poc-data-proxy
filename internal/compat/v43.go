package compat

// v43 is DHIS2 2.43: the 2.42 shapes, except where the live suite against 2.43
// showed otherwise.
type v43 struct{ v42 }

func (v43) Major() int { return 43 }

// ProgramFields: 2.43 gives enrollments a category combination of their own
// (enrollmentCategoryCombo). When it is not the default, enrollments carry an
// attribute option combo, and who may see or write them depends on access to its
// category options, as for events.
func (v v43) ProgramFields(af string) string {
	return v.v42.ProgramFields(af) + ",enrollmentCategoryCombo[id,isDefault]"
}

// TransferOrgUnit: 2.43 takes orgUnit= and answers 400 "Required parameter
// 'orgUnit' is not present" to ou=.
func (v43) TransferOrgUnit() string { return "orgUnit" }
