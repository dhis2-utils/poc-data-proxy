package tracker

import (
	"sync"
	"time"
)

// pagePositions remembers, per user and query, the last row of the last page served,
// so the next page of the same query reads on from that row's creation key instead of
// from the start. A device downloading a facility page by page then costs a page's
// worth of reading per page instead of all pages before it (docs/PERFORMANCE.md, "A
// device's full download"). Only the last row's identifier is kept, never results: a
// page that does not follow a remembered one reads from the start as before.
//
// Invalidation: a position is used once, for the page right after it, and only for
// ten minutes. Reading on from a key, a record written between two pages cannot shift
// the pages after it, as offset paging can.
type pagePositions struct {
	mu sync.Mutex
	m  map[string]pagePos
}

type pagePos struct {
	page int    // the page served
	last string // the row that carries its last object's creation key (plan.keyRow)
	at   time.Time
}

const (
	pagePosTTL = 10 * time.Minute
	pagePosMax = 1024
)

// get returns the row to read page on from, if page-1 of key was served lately.
func (pp *pagePositions) get(key string, page int) (string, bool) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	p, ok := pp.m[key]
	if !ok || p.page != page-1 || time.Since(p.at) > pagePosTTL {
		return "", false
	}
	return p.last, true
}

func (pp *pagePositions) set(key string, page int, last string) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	if pp.m == nil {
		pp.m = map[string]pagePos{}
	}
	if len(pp.m) >= pagePosMax {
		for k, p := range pp.m {
			if time.Since(p.at) > pagePosTTL || len(pp.m) >= pagePosMax {
				delete(pp.m, k)
			}
		}
	}
	pp.m[key] = pagePos{page: page, last: last, at: time.Now()}
}

// clear forgets every position (the store locked or was replaced).
func (pp *pagePositions) clear() {
	pp.mu.Lock()
	pp.m = nil
	pp.mu.Unlock()
}
