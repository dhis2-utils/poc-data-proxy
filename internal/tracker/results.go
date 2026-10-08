package tracker

import (
	"net/url"
	"strconv"
	"sync"
	"time"
)

// A device paging through a large query (the Android app's download: 50 per page,
// hundreds of pages) made the hub search and sort everything again for every page,
// so a download cost the square of the facility's size. Page 1 is always computed
// fresh and its sorted result kept for a minute; later pages of the same query by the
// same user read it from there. Within one paging run the device sees a consistent
// snapshot; what is written meanwhile comes with its next sync, as on central.

const resultTTL = time.Minute

type cachedResult struct {
	objs []*Obj
	at   time.Time
}

type resultCache struct {
	mu sync.Mutex
	m  map[string]cachedResult
}

func (ix *Index) resultKey(v *Viewer, collection string, q url.Values) string {
	k := url.Values{}
	for name, vals := range q {
		if name != "page" && name != "fields" && name != "totalPages" {
			k[name] = vals
		}
	}
	return strconv.FormatInt(v.UserID, 10) + " " + collection + "?" + k.Encode()
}

func (ix *Index) cachedResult(key string) ([]*Obj, bool) {
	ix.results.mu.Lock()
	defer ix.results.mu.Unlock()
	r, ok := ix.results.m[key]
	if !ok || time.Since(r.at) > resultTTL {
		return nil, false
	}
	return r.objs, true
}

func (ix *Index) cacheResult(key string, objs []*Obj) {
	ix.results.mu.Lock()
	defer ix.results.mu.Unlock()
	if ix.results.m == nil {
		ix.results.m = map[string]cachedResult{}
	}
	now := time.Now()
	for k, r := range ix.results.m {
		if now.Sub(r.at) > resultTTL {
			delete(ix.results.m, k)
		}
	}
	ix.results.m[key] = cachedResult{objs, now}
}

// clearResults forgets every kept result (the index was reloaded or cleared).
func (ix *Index) clearResults() {
	ix.results.mu.Lock()
	ix.results.m = nil
	ix.results.mu.Unlock()
}
