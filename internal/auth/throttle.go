package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// throttle slows down password guessing (review M3). DHIS2 has no lockout of its
// own, and offline each guess costs the hub an Argon2id derivation. Failures count
// per user and source address, per source address, and per user across sources:
// a tablet still sending an old password locks only itself out of that account,
// while guessing from many addresses is still capped.
type throttle struct {
	mu    sync.Mutex
	fails map[string]*failures
}

type failures struct {
	n       int // failures since the counter last went quiet
	pending int // attempts being checked now
	last    time.Time
	until   time.Time
}

const throttleWindow = 15 * time.Minute

func throttleKeys(src, user string) map[string]int {
	return map[string]int{"us:" + user + "@" + src: 5, "s:" + src: 20, "u:" + user: 50}
}

// begin reserves an attempt for src at user's password, or returns how long src must
// wait. The attempt counts before the password is checked (review R-04): otherwise
// parallel guesses all passed the check of the counters before any of them failed.
// Attempts in flight count as failures until end says otherwise; past the limit, after
// a lockout ran out, one attempt at a time.
func (t *throttle) begin(src, user string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fails == nil {
		t.fails = map[string]*failures{}
	}
	now := time.Now()
	keys := throttleKeys(src, user)
	var d time.Duration
	for k, limit := range keys {
		f := t.fails[k]
		if f == nil {
			continue
		}
		d = max(d, f.until.Sub(now))
		if f.pending >= max(limit-f.current(now), 1) {
			d = max(d, time.Second) // the attempts in flight may use up the rest
		}
	}
	if d > 0 {
		return d
	}
	t.add(src, user)
	return 0
}

// reserve counts an attempt in flight without checking the counters: the cached
// credential of the device it was verified from passes during a lockout (L2).
func (t *throttle) reserve(src, user string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fails == nil {
		t.fails = map[string]*failures{}
	}
	t.add(src, user)
}

func (t *throttle) add(src, user string) {
	for k := range throttleKeys(src, user) {
		f := t.fails[k]
		if f == nil {
			f = &failures{}
			t.fails[k] = f
		}
		f.pending++
	}
}

// current is the failure count still in force: a counter is forgotten once it has
// been quiet for throttleWindow since its last failure, not its first, so steady
// guessing below the limit still adds up (review R-04).
func (f *failures) current(now time.Time) int {
	if now.Sub(f.last) > throttleWindow && now.After(f.until) {
		return 0
	}
	return f.n
}

// end closes an attempt begin reserved: failed counts it, succeeded clears the
// counter of this user from this device, neither (no verdict on the password, e.g.
// central unreachable) just releases it.
func (t *throttle) end(src, user string, failed, succeeded bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fails == nil {
		t.fails = map[string]*failures{}
	}
	now := time.Now()
	for k, limit := range throttleKeys(src, user) {
		f := t.fails[k]
		if f == nil {
			f = &failures{}
			t.fails[k] = f
		}
		f.pending = max(f.pending-1, 0)
		if !failed {
			continue
		}
		f.n = f.current(now) + 1
		f.last = now
		if f.n >= limit {
			// 30 s at the limit, doubling with each further failure, at most 15 min.
			d := min(30*time.Second<<min(f.n-limit, 5), throttleWindow)
			f.until = now.Add(d)
		}
	}
	if succeeded {
		// The per-device and per-account counters keep counting: clearing them on any
		// success would let someone with one working account reset them and keep
		// guessing others; on a shared device mistyped logins by several staff can
		// therefore delay everyone's for a while (security review S12).
		if f := t.fails["us:"+user+"@"+src]; f != nil && f.pending == 0 {
			delete(t.fails, "us:"+user+"@"+src)
		} else if f != nil {
			f.n = 0
		}
	}
	if len(t.fails) > 10000 { // bound memory under a flood of distinct names
		for k, f := range t.fails {
			if f.pending == 0 && f.current(now) == 0 {
				delete(t.fails, k)
			}
		}
	}
}

// Source is the client address a login attempt is counted against. The hub is the
// facility's edge, so RemoteAddr is the device.
func Source(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
