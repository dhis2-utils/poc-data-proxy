package access

import "testing"

func TestInFacilityFollowsCaptureScopeDown(t *testing.T) {
	path := "/country/district/facility"
	for _, c := range []struct {
		ous  []string
		want bool
	}{
		{[]string{"facility"}, true},
		{[]string{"district"}, true}, // capture at the district covers its facilities
		{[]string{"country"}, true},
		{[]string{"sibling"}, false},
		{[]string{"fac"}, false}, // a prefix of an ID is not the ID
		{nil, false},
	} {
		if got := (&List{OrgUnits: c.ous}).InFacility(path); got != c.want {
			t.Errorf("%v: got %v, want %v", c.ous, got, c.want)
		}
	}
	if (&List{OrgUnits: []string{"facility"}}).InFacility("") {
		t.Error("a hub without a facility has no facility users")
	}
}
