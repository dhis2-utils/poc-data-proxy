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
	n     int
	first time.Time
	until time.Time
}

const throttleWindow = 15 * time.Minute

func throttleKeys(src, user string) map[string]int {
	return map[string]int{"us:" + user + "@" + src: 5, "s:" + src: 20, "u:" + user: 50}
}

// wait returns how long src must wait before trying user's password again.
func (t *throttle) wait(src, user string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	var d time.Duration
	for k := range throttleKeys(src, user) {
		if f := t.fails[k]; f != nil {
			d = max(d, time.Until(f.until))
		}
	}
	return d
}

func (t *throttle) fail(src, user string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fails == nil {
		t.fails = map[string]*failures{}
	}
	now := time.Now()
	for k, limit := range throttleKeys(src, user) {
		f := t.fails[k]
		if f == nil || (now.Sub(f.first) > throttleWindow && now.After(f.until)) {
			f = &failures{first: now}
			t.fails[k] = f
		}
		f.n++
		if f.n >= limit {
			// 30 s at the limit, doubling with each further failure, at most 15 min.
			d := min(30*time.Second<<min(f.n-limit, 5), throttleWindow)
			f.until = now.Add(d)
		}
	}
	if len(t.fails) > 10000 { // bound memory under a flood of distinct names
		for k, f := range t.fails {
			if now.Sub(f.first) > throttleWindow && now.After(f.until) {
				delete(t.fails, k)
			}
		}
	}
}

// ok clears the counter of this user from this device after a successful login. The
// per-device and per-account counters keep counting: clearing them on any success would
// let someone with one working account reset them and keep guessing others; on a shared
// device mistyped logins by several staff can therefore delay everyone's for a while
// (security review S12).
func (t *throttle) ok(src, user string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, "us:"+user+"@"+src)
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
