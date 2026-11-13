package proxy

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

// Replay: at a staff member's online login, the hub loads into their cache, in the
// background and with their own credential, the metadata and settings requests the
// facility's apps were seen making (any user's, in the last replaySeen), so they can
// open those screens offline without having opened each online first. Central answers
// as them, so sharing and language stay theirs; nothing is shared between users
// (FINDINGS.md, "Shared metadata cache"). Once a day per user, at most replayMax
// requests, one at a time with replayPause between them.
const (
	replaySeen  = 14 * 24 * time.Hour
	replayFresh = 24 * time.Hour
	replayMax   = 300
)

var replayPause = 200 * time.Millisecond

type replays struct {
	mu     sync.Mutex
	last   map[int64]time.Time
	wg     sync.WaitGroup
	closed bool
}

// notReplayed are the first path segments (after /api/ and a version) of data and of
// requests with effects: patient and aggregate data, which the hub serves from its
// store or not at all offline, analytics, audits, messages, files, central's checks
// of data, generated IDs (generate reserves values), users (other people), and the
// hub's own answers. /api/me itself is answered offline from the copy kept at login.
var notReplayed = map[string]bool{
	"tracker": true, "trackedEntityInstances": true, "trackedEntities": true, "events": true, "enrollments": true,
	"relationships": true, "dataValues": true, "dataValueSets": true, "dataEntry": true,
	"completeDataSetRegistrations": true, "analytics": true, "audits": true, "validation": true,
	"validationResults": true, "dataAnalysis": true, "messageConversations": true, "interpretations": true,
	"fileResources": true, "documents": true, "users": true, "userLookup": true, "apiToken": true, "ping": true,
}

// NotLoadedAtLogin lists notReplayed, sorted: for the catalog (internal/catalog).
func NotLoadedAtLogin() []string { return slices.Sorted(maps.Keys(notReplayed)) }

var versionRe = regexp.MustCompile(`^[0-9]{2}/`)

// replayable reports whether a cached request may be made again as another user.
// others are the hub's users' identifiers: a request naming one is about that user.
func replayable(uri string, others []string) bool {
	u, err := url.Parse(uri)
	if err == nil && u.Path == "/dhis-web-commons/menu/getModules.action" {
		return true // the header bar's apps menu, per user
	}
	if err != nil || !strings.HasPrefix(u.Path, "/api/") || shared(u.Path) || validationRe.MatchString(u.Path) || meRe.MatchString(u.Path) ||
		strings.Contains(u.Path, "/generate") || strings.HasSuffix(u.Path, "/requestValues") {
		return false
	}
	rest := versionRe.ReplaceAllString(strings.TrimPrefix(u.Path, "/api/"), "")
	first, _, _ := strings.Cut(rest, "/")
	first = strings.TrimSuffix(first, ".json")
	if notReplayed[first] {
		return false
	}
	for k := range u.Query() {
		if k == "user" || k == "username" || k == "userId" {
			return false
		}
	}
	for _, id := range others {
		if id != "" && strings.Contains(uri, id) {
			return false
		}
	}
	return true
}

// ReplayAfterLogin starts the replay for a user who just logged in online, unless
// it ran for them in the last day.
func (p *Proxy) ReplayAfterLogin(sess *core.Session) {
	if sess == nil || sess.UserID <= 0 {
		return
	}
	r := &p.replay
	r.mu.Lock()
	defer r.mu.Unlock()
	// Again within the day when the hub has since learnt of a change.
	if r.closed || (time.Since(r.last[sess.UserID]) < replayFresh && r.last[sess.UserID].After(p.H.MetadataCutoff(sess.UserID))) {
		return
	}
	if r.last == nil {
		r.last = map[int64]time.Time{}
	}
	r.last[sess.UserID] = time.Now()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		start := time.Now()
		n, kb, err := p.replayFor(sess.UserID, p.Upstream(sess))
		slog.Info("replay after login", "user", sess.Username, "loaded", n, "kB", kb, "s", int(time.Since(start).Seconds()), "err", err)
	}()
}

// WaitReplays waits for running replays to end.
func (p *Proxy) WaitReplays() { p.replay.wg.Wait() }

// StopReplays ends running replays (at their next request) and starts no more.
func (p *Proxy) StopReplays() {
	p.replay.mu.Lock()
	p.replay.closed = true
	p.replay.mu.Unlock()
	p.replay.wg.Wait()
}

func (p *Proxy) stopped() bool {
	p.replay.mu.Lock()
	defer p.replay.mu.Unlock()
	return p.replay.closed
}

// replayFor loads the requests for userID with a; it returns how many answers it
// stored and their size in kB.
func (p *Proxy) replayFor(userID int64, a central.Auth) (n, kb int, err error) {
	now := time.Now()
	fresh := now.Add(-replayFresh) // copies fetched since are kept, unless older than a change
	if cut := p.H.MetadataCutoff(userID); cut.After(fresh) {
		fresh = cut
	}
	uris, err := p.H.Store.ReplaySources(userID, now.Add(-replaySeen), fresh)
	if err != nil {
		return 0, 0, err
	}
	users, err := p.H.Store.Users()
	if err != nil {
		return 0, 0, err
	}
	var others []string
	for _, u := range users {
		if u.ID != userID {
			others = append(others, u.UserUID)
		}
	}
	tried, bytes := 0, 0
	defer func() { kb = bytes / 1024 }()
	for _, uri := range uris {
		if tried == replayMax || p.stopped() || !p.H.Online() || !p.H.Store.Unlocked() {
			break
		}
		if !replayable(uri, others) {
			continue
		}
		if tried > 0 {
			time.Sleep(replayPause)
		}
		tried++
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		res, err := p.Forward(ctx, r, nil, a)
		cancel()
		if err != nil {
			if central.IsNetwork(err) {
				return n, 0, err
			}
			continue
		}
		if !cacheable(r, res.Status) || len(res.Body) > maxCacheBody {
			continue
		}
		if err := p.H.Store.PutCache(userID, http.MethodGet, uri,
			&store.CachedResponse{Status: res.Status, Header: res.Header, Body: res.Body, Replayed: true}); err != nil {
			return n, 0, err
		}
		n++
		bytes += len(res.Body)
	}
	return n, 0, nil
}
