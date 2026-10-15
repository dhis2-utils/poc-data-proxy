package aggregate

// Retention is how many periods of each data set the hub keeps (the current one
// included), read from central's dataStore (facility-hub-config/aggregate).
type Retention struct {
	Default  int            `json:"retentionPeriods"`
	DataSets map[string]int `json:"dataSets"`
}

// DefaultRetention is used when neither the dataStore nor the configuration says.
const DefaultRetention = 12

func (r Retention) For(ds string) int {
	if n := r.DataSets[ds]; n > 0 {
		return n
	}
	if r.Default > 0 {
		return r.Default
	}
	return DefaultRetention
}
