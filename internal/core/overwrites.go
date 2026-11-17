package core

// The overwrite count's setting (store/overwrites.go): facility-hub-config/overwrites
// on central, {"off": true} to stop the reads on every hub without a release. Absent,
// the count runs.
const overwritesKey = "overwrites"

// OverwriteCountOff reports whether the country turned the overwrite count off.
func (h *Hub) OverwriteCountOff() bool {
	var s struct {
		Off bool `json:"off"`
	}
	h.configState(overwritesKey, &s)
	return s.Off
}
