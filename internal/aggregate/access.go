package aggregate

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/valuetype"
)

// Access is what one user may enter, from central's dataEntry/metadata: the data sets
// with data write sharing for the user, their elements and category combinations,
// and the category options the user may enter data for. The hub matches against it;
// central decided it.
type Access struct {
	DataSets map[string]*DataSet `json:"dataSets"`
	Elements map[string]*Element `json:"elements"`
	Combos   map[string]*Combo   `json:"combos"`
	Options  map[string]bool     `json:"options"`
	// Bypass: F_EDIT_EXPIRED or ALL, so expiry days do not lock periods for this user.
	Bypass bool `json:"bypass"`
}

type DataSet struct {
	ID                string        `json:"id"`
	Name              string        `json:"name"`
	PeriodType        string        `json:"periodType"`
	CategoryCombo     string        `json:"categoryCombo"`
	ExpiryDays        float64       `json:"expiryDays"`
	OpenFuturePeriods int           `json:"openFuturePeriods"`
	InputPeriods      []InputPeriod `json:"inputPeriods,omitempty"`
	// InputByMoment: central compares data input periods with the moment, not the day
	// (2.43; see InputOpen). Set by the hub from central's version.
	InputByMoment                bool              `json:"inputByMoment,omitempty"`
	ValidCompleteOnly            bool              `json:"validCompleteOnly"`
	CompulsoryFieldsCompleteOnly bool              `json:"compulsoryFieldsCompleteOnly"`
	Compulsory                   []Operand         `json:"compulsory,omitempty"`
	Elements                     map[string]string `json:"elements"` // element -> its category combo in this data set
	OrgUnits                     []string          `json:"orgUnits"`
}

type InputPeriod struct{ Period, Opening, Closing string }

type Operand struct{ DataElement, COC string }

type Element struct{ ID, Name, ValueType, CategoryCombo string }

type Combo struct {
	ID      string `json:"id"`
	Default bool   `json:"default"`
	COCs    []COC  `json:"cocs"`
}

type COC struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Options []string `json:"options"`
}

type ref struct {
	ID string `json:"id"`
}

// Bypass: authorities under which expiry days do not lock periods.
func Bypass(authorities []string) bool {
	return slices.Contains(authorities, "F_EDIT_EXPIRED") || slices.Contains(authorities, "ALL")
}

// ParseMetadata reads central's dataEntry/metadata answer for a user with these
// authorities.
func ParseMetadata(b []byte, authorities []string) (*Access, error) {
	var md struct {
		DataSets []struct {
			ID                           string  `json:"id"`
			DisplayName                  string  `json:"displayName"`
			PeriodType                   string  `json:"periodType"`
			CategoryCombo                ref     `json:"categoryCombo"`
			ExpiryDays                   float64 `json:"expiryDays"`
			OpenFuturePeriods            int     `json:"openFuturePeriods"`
			ValidCompleteOnly            bool    `json:"validCompleteOnly"`
			CompulsoryFieldsCompleteOnly bool    `json:"compulsoryFieldsCompleteOnly"`
			DataInputPeriods             []struct {
				Period      ref    `json:"period"`
				OpeningDate string `json:"openingDate"`
				ClosingDate string `json:"closingDate"`
			} `json:"dataInputPeriods"`
			Compulsory []struct {
				DataElement         ref `json:"dataElement"`
				CategoryOptionCombo ref `json:"categoryOptionCombo"`
			} `json:"compulsoryDataElementOperands"`
			DataSetElements []struct {
				DataElement   ref  `json:"dataElement"`
				CategoryCombo *ref `json:"categoryCombo"`
			} `json:"dataSetElements"`
			OrganisationUnits []any `json:"organisationUnits"` // ids, or objects with an id
		} `json:"dataSets"`
		DataElements []struct {
			ID            string `json:"id"`
			DisplayName   string `json:"displayName"`
			ValueType     string `json:"valueType"`
			CategoryCombo ref    `json:"categoryCombo"`
		} `json:"dataElements"`
		CategoryCombos []struct {
			ID                   string `json:"id"`
			IsDefault            bool   `json:"isDefault"`
			CategoryOptionCombos []struct {
				ID              string   `json:"id"`
				DisplayName     string   `json:"displayName"`
				CategoryOptions []string `json:"categoryOptions"`
			} `json:"categoryOptionCombos"`
		} `json:"categoryCombos"`
		CategoryOptions []ref `json:"categoryOptions"`
	}
	if err := json.Unmarshal(b, &md); err != nil {
		return nil, err
	}
	a := &Access{DataSets: map[string]*DataSet{}, Elements: map[string]*Element{}, Combos: map[string]*Combo{},
		Options: map[string]bool{}, Bypass: Bypass(authorities)}
	for _, e := range md.DataElements {
		a.Elements[e.ID] = &Element{ID: e.ID, Name: e.DisplayName, ValueType: e.ValueType, CategoryCombo: e.CategoryCombo.ID}
	}
	for _, c := range md.CategoryCombos {
		cc := &Combo{ID: c.ID, Default: c.IsDefault}
		for _, coc := range c.CategoryOptionCombos {
			cc.COCs = append(cc.COCs, COC{ID: coc.ID, Name: coc.DisplayName, Options: coc.CategoryOptions})
		}
		a.Combos[c.ID] = cc
	}
	for _, o := range md.CategoryOptions {
		a.Options[o.ID] = true
	}
	for _, d := range md.DataSets {
		ds := &DataSet{ID: d.ID, Name: d.DisplayName, PeriodType: d.PeriodType, CategoryCombo: d.CategoryCombo.ID,
			ExpiryDays: d.ExpiryDays, OpenFuturePeriods: d.OpenFuturePeriods, ValidCompleteOnly: d.ValidCompleteOnly,
			CompulsoryFieldsCompleteOnly: d.CompulsoryFieldsCompleteOnly, Elements: map[string]string{}}
		for _, ip := range d.DataInputPeriods {
			ds.InputPeriods = append(ds.InputPeriods, InputPeriod{Period: ip.Period.ID, Opening: ip.OpeningDate, Closing: ip.ClosingDate})
		}
		for _, op := range d.Compulsory {
			ds.Compulsory = append(ds.Compulsory, Operand{op.DataElement.ID, op.CategoryOptionCombo.ID})
		}
		for _, dse := range d.DataSetElements {
			cc := ""
			if e := a.Elements[dse.DataElement.ID]; e != nil {
				cc = e.CategoryCombo
			}
			if dse.CategoryCombo != nil && dse.CategoryCombo.ID != "" {
				cc = dse.CategoryCombo.ID
			}
			ds.Elements[dse.DataElement.ID] = cc
		}
		for _, ou := range d.OrganisationUnits {
			switch v := ou.(type) {
			case string:
				ds.OrgUnits = append(ds.OrgUnits, v)
			case map[string]any:
				if id, _ := v["id"].(string); id != "" {
					ds.OrgUnits = append(ds.OrgUnits, id)
				}
			}
		}
		a.DataSets[d.ID] = ds
	}
	return a, nil
}

// Assigned reports whether the data set is assigned to the org unit.
func (d *DataSet) Assigned(ou string) bool { return slices.Contains(d.OrgUnits, ou) }

// AOC is the attribute option combination the app means by cc and cp (the
// attribute combination and its options): the data set's only combination when it
// has the default one and the app sent none.
func (a *Access) AOC(ds *DataSet, cc string, cp []string) (string, bool) {
	combo := a.Combos[ds.CategoryCombo]
	if combo == nil || (cc != "" && cc != ds.CategoryCombo) {
		return "", false
	}
	if cc == "" {
		if !combo.Default || len(combo.COCs) != 1 {
			return "", false
		}
		return combo.COCs[0].ID, true
	}
	want := slices.Sorted(slices.Values(cp))
	for _, coc := range combo.COCs {
		if slices.Equal(slices.Sorted(slices.Values(coc.Options)), want) {
			return coc.ID, true
		}
	}
	return "", false
}

// Attribute is the combination and options of an attribute option combination of the
// data set, as dataEntry/dataValues writes it ("attribute": {"combo", "options"}).
func (a *Access) Attribute(ds *DataSet, aoc string) (combo string, options []string, ok bool) {
	c := a.Combos[ds.CategoryCombo]
	if c == nil {
		return "", nil, false
	}
	for _, coc := range c.COCs {
		if coc.ID == aoc {
			return c.ID, coc.Options, true
		}
	}
	return "", nil, false
}

// MayUse reports whether the user may enter data for this attribute option
// combination of the data set: it is the data set's, and every option of it is one
// central lists for the user.
func (a *Access) MayUse(ds *DataSet, aoc string) bool {
	_, opts, ok := a.Attribute(ds, aoc)
	if !ok {
		return false
	}
	for _, o := range opts {
		if !a.Options[o] {
			return false
		}
	}
	return true
}

// COCName is a category option combination's display name, or its ID.
func (a *Access) COCName(id string) string {
	for _, c := range a.Combos {
		for _, coc := range c.COCs {
			if coc.ID == id {
				return coc.Name
			}
		}
	}
	return id
}

// Key is the same for users whose metadata grants the same data sets and options:
// the hub pulls their forms once (as with tracker listings, access.List.GroupKey).
func (a *Access) Key() string {
	if a == nil || len(a.DataSets) == 0 {
		return ""
	}
	ds := make([]string, 0, len(a.DataSets))
	for id := range a.DataSets {
		ds = append(ds, id)
	}
	opts := make([]string, 0, len(a.Options))
	for id := range a.Options {
		opts = append(opts, id)
	}
	sort.Strings(ds)
	sort.Strings(opts)
	return strings.Join(ds, ",") + "|" + strings.Join(opts, ",")
}

// ValidValue reports whether central accepts v for a data element of this value type
// (E2030 otherwise). Types the hub does not check are accepted: central decides on
// push.
func ValidValue(valueType, v string) bool {
	if valuetype.Numeric(valueType) {
		return valuetype.Valid(valueType, v) // as central (I8c)
	}
	switch valueType {
	case "BOOLEAN":
		return v == "true" || v == "false"
	case "TRUE_ONLY":
		return v == "true"
	case "DATE":
		_, err := time.Parse(time.DateOnly, v)
		return err == nil
	}
	return true
}
