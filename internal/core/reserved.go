package core

const reservedPoolKey = "reserved"

// ReservedPool is the pool-size setting read from central
// (facility-hub-config/reserved) at the last catalog refresh, managed in the Facility
// Hub app: the default pool size, and one per generated attribute.
type ReservedPool struct {
	Default    int            `json:"default,omitempty"`
	Attributes map[string]int `json:"attributes,omitempty"`
}

// ReservedPool returns the setting as last read; the zero value when there is none.
func (h *Hub) ReservedPool() ReservedPool {
	var r ReservedPool
	h.configState(reservedPoolKey, &r)
	return r
}

// PoolTarget is how many reserved values attr's pool aims for: the app's per-attribute
// setting, else its default, else reserved_pool_size (T2/T4, decided 2026-10-30: a
// per-attribute setting in the Facility Hub app, default 500).
func (h *Hub) PoolTarget(attr string) int {
	r := h.ReservedPool()
	if n := r.Attributes[attr]; n > 0 {
		return n
	}
	if r.Default > 0 {
		return r.Default
	}
	return h.Cfg.ReservedPoolSize
}
