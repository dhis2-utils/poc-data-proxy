package testkit

import (
	"net/http/httptest"
	"testing"

	fc "github.com/dhis2/facility-hub/internal/fakecentral"
)

// Fake starts the in-process fake central and returns its manifest. The fake's users
// map onto the roles; it has no district or searchless user unless added.
func Fake(t testing.TB) (*Manifest, *fc.Central) {
	t.Helper()
	c := fc.New()
	c.Seed("SeedTe00001", "SeedEn00001", "Seeded", "NAT-0001", map[string]string{"SeedEv00001": fc.Stage1, "SeedEv00003": fc.Stage3})
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	m := &Manifest{
		Profile: "fake", DHIS2Version: fc.Version, CentralURL: srv.URL,
		AdminUser: "hub_admin", AdminPassword: fc.Password,
		Password: fc.Password, SetupGroup: fc.SetupGroup,
		Facility: OrgUnit{ID: fc.Facility, Name: "Facility", Path: "/root/" + fc.Facility},
		Sibling:  OrgUnit{ID: fc.OtherFacility, Name: "Other facility", Path: "/root/" + fc.OtherFacility},
		Programs: []Program{{ID: fc.Program, Name: "TB program", TET: fc.TET, AccessLevel: "OPEN",
			Stages: []string{fc.Stage1, fc.Stage2, fc.Stage3}, RestrictedStage: fc.Stage3,
			SearchAttr: fc.AttrFirstName, UniqueAttr: fc.AttrNationalI, GeneratedAttr: fc.AttrTBID, DistrictSees: true}},
		Users: map[string]User{
			RoleAdmin:      {Username: "hub_admin", UID: "HubUsrAdmin"},
			RoleNurse:      {Username: "nurse_a", UID: "HubUsrNursA"},
			RoleRestricted: {Username: "nurse_b", UID: "HubUsrNursB"},
			RoleClerk:      {Username: "clerk_c", UID: "HubUsrClrkC"},
			RoleOutsider:   {Username: "outsider_d", UID: "HubUsrOutsD"},
		},
		EventPrograms: []Program{{ID: fc.EventProgram, Name: "Fake event program", Event: true, Stages: []string{fc.EventStage}},
			{ID: fc.EventDonorProgram, Name: "Fake donor events", Event: true, Stages: []string{fc.EventDonorStage},
				AOCs: []string{fc.AggDonorAOCA, fc.AggDonorAOCB}}},
		Aggregate: &Aggregate{DataSet: fc.AggDataSet, ValidDataSet: fc.AggValidDS, DonorDataSet: fc.AggDonorDS,
			Number: fc.AggDeNum, Integer: fc.AggDeInt, Required: fc.AggDeReq, COC: fc.AggCOC, DefaultAOC: fc.AggCOC,
			DonorCC: fc.AggDonorCC, DonorOptionNurse: fc.AggDonorOptA, DonorAOCNurse: fc.AggDonorAOCA},
	}
	return m, c
}
