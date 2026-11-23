// Package testkit runs the hub against a central DHIS2, fake or real, described by a
// manifest of roles (docs/TESTING.md). Tests ask for "the nurse" or "the primary
// program", never for a UID, so the same test runs on every DHIS2 setup.
package testkit

import (
	"encoding/json"
	"fmt"
	"os"
)

// Roles a manifest may define (docs/TESTING.md).
const (
	RoleAdmin      = "admin"      // in the setup user group
	RoleNurse      = "nurse"      // captures at the facility, full program access
	RoleRestricted = "restricted" // like nurse, but one stage hidden
	RoleClerk      = "clerk"      // reads, cannot write events
	RoleOutsider   = "outsider"   // captures at another facility
	RoleDistrict   = "district"   // district officer: district scope, not every program
	RoleSearchless = "searchless" // captures at the facility without search scope there
	RoleNurse2     = "nurse2"     // a second nurse, for hand-over between devices
)

type Manifest struct {
	Profile      string `json:"profile"`      // synthetic, sierra-leone, laos, fake
	DHIS2Version string `json:"dhis2Version"` // e.g. 2.42.3
	CentralURL   string `json:"centralUrl"`
	// Superuser for provisioning and for reading central's state in tests.
	AdminUser     string `json:"adminUser"`
	AdminPassword string `json:"adminPassword"`

	Password   string `json:"password"`   // every role user's
	SetupGroup string `json:"setupGroup"` // also the hub owners group
	// The hub accounts group and role. With SetupGroup as the hub owners, they are
	// named in facility-hub-admin/accounts before any hub is set up, as a superuser
	// does in the Facility Hub app (docs/DEPLOYMENT.md).
	AccountsGroup string          `json:"accountsGroup"`
	AccountsRole  string          `json:"accountsRole"`
	Facility      OrgUnit         `json:"facility"`
	Sibling       OrgUnit         `json:"sibling"` // another facility in the same district
	District      OrgUnit         `json:"district"`
	Programs      []Program       `json:"programs"`
	Users         map[string]User `json:"users"` // role -> user
	// Matrix users cover combinations of capture and search scope and of program,
	// stage and tracked entity type sharing; the differential oracle checks each.
	Matrix []MatrixUser `json:"matrix,omitempty"`
	// Aggregate: data sets at the facility for the aggregate tests; nil when this
	// setup has none (the tests skip).
	Aggregate *Aggregate `json:"aggregate,omitempty"`
	// EventPrograms: event programs (without registration) at the facility.
	EventPrograms []Program `json:"eventPrograms,omitempty"`
}

// Aggregate names what the aggregate tests use. Number and Integer are data elements
// of DataSet (value types NUMBER and INTEGER), Required its compulsory one, COC their
// category option combination and DefaultAOC the default attribute option combination.
// ValidDataSet has validCompleteOnly. DonorDataSet has an attribute combination
// (DonorCC) with an option only the nurse may use (DonorOptionNurse, its AOC
// DonorAOCNurse); the clerk may not.
type Aggregate struct {
	DataSet          string `json:"dataSet"`
	ValidDataSet     string `json:"validDataSet"`
	DonorDataSet     string `json:"donorDataSet,omitempty"`
	Number           string `json:"number"`
	Integer          string `json:"integer"`
	Required         string `json:"required"`
	COC              string `json:"coc"`
	DefaultAOC       string `json:"defaultAoc"`
	DonorCC          string `json:"donorCc,omitempty"`
	DonorOptionNurse string `json:"donorOptionNurse,omitempty"`
	DonorAOCNurse    string `json:"donorAocNurse,omitempty"`
	// For the real app (TestAggregateDataEntryApp): Number's and Integer's row labels
	// in the form, and the 1-based data cell of COC in their rows.
	NumberLabel  string `json:"numberLabel,omitempty"`
	IntegerLabel string `json:"integerLabel,omitempty"`
	Column       int    `json:"column,omitempty"`
}

type MatrixUser struct {
	User
	Dims map[string]string `json:"dims"` // e.g. capture=district, search=none, open=read
}

type OrgUnit struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
	Path string `json:"path"`
}

type Program struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	TET             string   `json:"tet"`
	AccessLevel     string   `json:"accessLevel"` // OPEN, AUDITED, PROTECTED, CLOSED
	Stages          []string `json:"stages"`
	RestrictedStage string   `json:"restrictedStage,omitempty"` // hidden from the restricted role
	// Attributes tests can use; empty when the program has none of that kind.
	SearchAttr string `json:"searchAttr,omitempty"` // non-unique text, e.g. first name
	// Generated attributes an enrollment must carry: writers reserve a value through
	// the hub (online from central, offline from its pool) as the apps do.
	MandatoryGenerated []string `json:"mandatoryGenerated,omitempty"`
	UniqueAttr         string   `json:"uniqueAttr,omitempty"`
	// OtherUnique are the program's further unique attributes (not generated): each
	// person gets a value of its own in them too, as a program rule may require one
	// (Laos: the TB registration number of a notified case).
	OtherUnique   []string `json:"otherUnique,omitempty"`
	GeneratedAttr string   `json:"generatedAttr,omitempty"`
	// OnlyAttrs: attributes of this program alone, neither its tracked entity type's
	// nor another manifest program's. The oracle filters and orders other programs'
	// lists on them, which a user who cannot read this program must not learn from
	// (review R-03).
	OnlyAttrs []string `json:"onlyAttrs,omitempty"`
	// Valid values for what an enrollment or event must carry: mandatory attributes
	// (attribute -> value) and, per stage, data elements (data element -> value).
	// Writers fill these in so every profile's validation passes.
	RequiredAttrs map[string]string            `json:"requiredAttrs,omitempty"`
	EventValues   map[string]map[string]string `json:"eventValues,omitempty"`
	// Shared with the district role (the setup user may not see every program).
	DistrictSees bool `json:"districtSees"`
	// EnrollmentCombo: enrollments carry an attribute option combo, so who sees them
	// depends on category option sharing (2.43's enrollmentCategoryCombo).
	EnrollmentCombo bool `json:"enrollmentCombo,omitempty"`
	// Event: an event program (no tracked entity type, no enrollments).
	Event bool `json:"event,omitempty"`
	// AOCs: option combinations of the program's attribute category combination (its
	// events); EnrollmentAOCs: of its enrollment category combination (2.43). At most
	// two each: the write oracle tries every user with each.
	AOCs           []string `json:"aocs,omitempty"`
	EnrollmentAOCs []string `json:"enrollmentAocs,omitempty"`
}

type User struct {
	Username string `json:"username"`
	UID      string `json:"uid"`
}

// Primary is the program most tests use: the first one.
func (m *Manifest) Primary() Program { return m.Programs[0] }

// User returns the role's user, or ok=false if this setup has no such role.
func (m *Manifest) User(role string) (User, bool) {
	u, ok := m.Users[role]
	return u, ok
}

func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := &Manifest{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(m.Programs) == 0 || m.Facility.ID == "" || m.Users[RoleAdmin].Username == "" {
		return nil, fmt.Errorf("%s: needs a facility, a program and an admin user", path)
	}
	return m, nil
}

func (m *Manifest) Save(path string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
