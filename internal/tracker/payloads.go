package tracker

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// Decoding payloads was most of what rendering a page cost, and at a first download
// every Android device asks for the same pages (docs/PERFORMANCE.md). Decoded payloads
// are kept for later requests, keyed by uid and the payload's nonce: each write seals
// with a new random nonce, so a changed object always misses. Entries are never
// changed (rendering copies before it edits, see shallow) and are dropped when the
// store locks.

const payloadCacheSize = 4096

type payloadCache struct {
	mu   sync.Mutex
	m    map[string]cachedPayload
	ring []string // keys in insertion order, for eviction
	next int
}

type cachedPayload struct {
	version string
	data    M
}

func (c *payloadCache) get(uid, version string) (M, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[uid]
	if !ok || e.version != version {
		return nil, false
	}
	return e.data, true
}

func (c *payloadCache) put(uid, version string, d M) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m, c.ring = map[string]cachedPayload{}, make([]string, payloadCacheSize)
	}
	if _, ok := c.m[uid]; !ok {
		if old := c.ring[c.next]; old != "" {
			delete(c.m, old)
		}
		c.ring[c.next] = uid
		c.next = (c.next + 1) % payloadCacheSize
	}
	c.m[uid] = cachedPayload{version, d}
}

func (c *payloadCache) clear() {
	c.mu.Lock()
	c.m, c.ring, c.next = nil, nil, 0
	c.mu.Unlock()
}

// loadPayloads returns the decoded full payloads of these objects, from the cache
// where their version is there.
func (ix *Index) loadPayloads(uids []string) map[string]M {
	sealed, err := ix.store.SealedPayloads(uids)
	if err != nil {
		slog.Warn("reading payloads", "err", err)
		return nil
	}
	out := make(map[string]M, len(sealed))
	for uid, ct := range sealed {
		version := string(ct[:min(len(ct), 12)])
		if d, ok := ix.payloads.get(uid, version); ok {
			out[uid] = d
			continue
		}
		b, err := ix.store.OpenPayload(uid, ct)
		if err != nil {
			slog.Warn("opening payload", "uid", uid, "err", err)
			continue
		}
		var d M
		if json.Unmarshal(b, &d) == nil {
			out[uid] = d
			ix.payloads.put(uid, version, d)
		}
	}
	return out
}
