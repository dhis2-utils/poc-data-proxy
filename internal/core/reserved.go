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
// per-attribute setting in the Facility Hub app, default 500). An explicit per-attribute
// 0 or less is returned as is — it turns the attribute's offline pool off — while the
// default keeps its > 0 check: with omitempty an explicit 0 and an absent default are
// indistinguishable, and the app's default field never writes one.
// Sizes from central are kept to at most maxPool, the app's limit (review R-07).
func (h *Hub) PoolTarget(attr string) int {
	r := h.ReservedPool()
	if n, ok := r.Attributes[attr]; ok {
		return min(n, maxPool)
	}
	if r.Default > 0 {
		return min(r.Default, maxPool)
	}
	return h.Cfg.ReservedPoolSize
}

// maxPool is the largest pool size the Facility Hub app lets a country set.
const maxPool = 100000
