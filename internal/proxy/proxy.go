// Package proxy is the pass-through for every path the hub does not handle itself
// (spec 5.1). Online it forwards to central with the user's credentials and caches GET
// responses per user; offline it serves those cached responses.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
)

type Proxy struct {
	H    *core.Hub
	Auth *auth.Auth

	replay replays // replay.go
}

const maxCacheBody = 64 << 20

// Headers never forwarded in either direction.
var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer",
	"Transfer-Encoding", "Upgrade", "Cookie", "Authorization", "Accept-Encoding", "Content-Length", "Host",
	// The browser's origin is the hub; to central the hub is a same-origin client. A
	// forwarded Origin makes central answer non-allowlisted origins with empty bodies.
	"Origin",
	// Central sees the request itself; the page it came from adds nothing.
	"Referer"}

var droppedResponseHeaders = []string{"Set-Cookie", "Content-Length", "Content-Encoding", "Transfer-Encoding",
	"Connection", "Keep-Alive", "Strict-Transport-Security"}

// Upstream picks the credential for a session: the stored PAT, else the session's
// memory-only credential.
func (p *Proxy) Upstream(sess *core.Session) central.Auth {
	if sess == nil {
		return central.Auth{}
	}
	if sess.UserID > 0 {
		if a, err := p.H.UpstreamAuth(sess.UserID); err == nil {
			return a
		}
	}
	return sess.Upstream
}

// Result of a forwarded request, fully buffered.
type Result struct {
	Status int
	Header http.Header
	Body   []byte
}

// Forward sends the request to central. A network error is returned as such so the
// caller can fall back to offline handling.
func (p *Proxy) Forward(ctx context.Context, r *http.Request, body []byte, a central.Auth) (*Result, error) {
	hdr := forwardHeader(r.Header)
	resp, err := p.H.Central.Do(ctx, r.Method, r.URL.RequestURI(), body, a, hdr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		p.H.Central.Failed(ctx, err)
		return nil, central.ErrNetwork
	}
	if why := notCentral(r, hdr, resp, p.H.Central.BaseURLs()); why != "" {
		err := fmt.Errorf("%w: %s %s answered by something other than central (%s)", central.ErrNetwork, r.Method, r.URL.Path, why)
		p.H.Central.Monitor.ReportFailure(err)
		return nil, err
	}
	h := resp.Header.Clone()
	for _, k := range droppedResponseHeaders {
		h.Del(k)
	}
	return &Result{Status: resp.StatusCode, Header: h, Body: b}, nil
}

// forwardHeader is a client's request header as sent on to central. Without the
// forwarded family too: central builds the base URL it writes into app pages from
// X-Forwarded-*, and the hub serves that page to every user (review R-17). The hub
// sets none of them itself.
func forwardHeader(h http.Header) http.Header {
	hdr := h.Clone()
	for _, k := range hopHeaders {
		hdr.Del(k)
	}
	hdr.Del("Forwarded")
	for k := range hdr {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Forwarded-") {
			delete(hdr, k)
		}
	}
	return hdr
}

// notCentral says why an answer to an /api/ request cannot be central's, or "": a
// captive portal or a hotel login in front of the internet answers every request,
// before the hub's next ping notices (adversarial D7). Kept narrow, as central also
// serves files and pages under /api/: an HTML page where the client asked for JSON
// (the hub asks for JSON when the client names nothing), outside the apps' files; or
// a redirect to a host that is not central's.
func notCentral(r *http.Request, hdr http.Header, resp *http.Response, bases []string) string {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return ""
	}
	if loc := resp.Header.Get("Location"); resp.StatusCode/100 == 3 && loc != "" {
		u, err := url.Parse(loc)
		if err != nil || u.Host == "" {
			return ""
		}
		for _, b := range bases {
			if bu, err := url.Parse(b); err == nil && strings.EqualFold(bu.Host, u.Host) {
				return ""
			}
		}
		return "a redirect to " + u.Host
	}
	accept := hdr.Get("Accept")
	if accept == "" {
		accept = "application/json" // what central.Do sends
	}
	if resp.StatusCode/100 == 2 && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") &&
		strings.Contains(accept, "json") && !strings.Contains(accept, "html") && !strings.Contains(accept, "*/*") &&
		!appsRe.MatchString(r.URL.Path) {
		return "an HTML page for " + accept
	}
	return ""
}

var appsRe = regexp.MustCompile(`^/api/([0-9]{2}/)?apps/`)

// Localize points central's absolute URLs in a response at the hub, for the host the
// client used. Forward does not do this, so the cache holds central's own form: a
// response cached for one Host must never be replayed to another client with that
// Host baked in (review M1).
func (p *Proxy) Localize(r *http.Request, res *Result) *Result {
	out := *res
	out.Header = res.Header.Clone()
	if loc := out.Header.Get("Location"); loc != "" {
		// Absolute, on the hub's origin: the Android SDK resolves a relative Location
		// as a new server URL ("/api/..." became host "api").
		if rel := p.H.Central.Local(loc); strings.HasPrefix(rel, "/") {
			out.Header.Set("Location", Origin(r)+rel)
		}
	}
	// Absolute central URLs (pager nextPage, job locations, the app shell's
	// <meta name="dhis2-base-url">) point clients past the hub; rewrite them to the hub.
	if rewritable(out.Header.Get("Content-Type")) {
		for _, base := range p.H.Central.BaseURLs() {
			out.Body = bytes.ReplaceAll(out.Body, []byte(base), []byte(Origin(r)))
		}
	}
	return &out
}

// Origin is the scheme and host the client used to reach the hub.
func Origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func Write(w http.ResponseWriter, res *Result) {
	for k, v := range res.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(res.Status)
	w.Write(res.Body)
}

func rewritable(ct string) bool {
	return strings.Contains(ct, "json") || strings.Contains(ct, "text/html") || strings.Contains(ct, "javascript")
}

// cacheable: 200s and 404s. Clients probe optional data store keys, icons and logos
// and treat "not found" as an answer, so a 404 is replayed offline too.
func cacheable(r *http.Request, status int) bool {
	switch status {
	case http.StatusOK, http.StatusNotFound, http.StatusMovedPermanently, http.StatusFound:
		return true // redirects too: /dhis-web-capture/index.html -> /apps/capture
	}
	return false
}

var meRe = regexp.MustCompile(`^/api/([0-9]{2}/)?me(\.json)?$`)

// validationRe: central's validation of data the hub may have changed since
// (validation/dataSet/<ds>, run by the Data Entry app). An online answer replayed
// offline could say "no alerts" for values that break a rule, so it is not cached
// and offline needs a connection (FINDINGS.md, I6c).
var validationRe = regexp.MustCompile(`^/api/([0-9]{2}/)?validation/`)

// selectFields applies a top-level DHIS2 fields selection ("authorities",
// "settings[keyUiLocale]") to a JSON object, as central does for /api/me. Fields the
// object lacks are left out ("userCredentials[userRoles]" gives {} on 2.42). exact is
// false when it cannot do what central would: fields of a list's items
// ("organisationUnits[id,programs[id]]"), which the login copy of /api/me names by id
// only.
func selectFields(body []byte, fields string) (out []byte, exact bool) {
	var obj map[string]any
	if fields == "" || json.Unmarshal(body, &obj) != nil {
		return body, true
	}
	exact = true
	sel := map[string]any{}
	for _, f := range splitTop(fields) {
		name, sub, _ := strings.Cut(f, "[")
		name = strings.TrimSpace(name)
		if name == "*" || name == ":all" {
			return body, true
		}
		v, ok := obj[name]
		if !ok {
			continue
		}
		if _, isList := v.([]any); isList && sub != "" {
			exact = false
		}
		if m, isMap := v.(map[string]any); isMap && sub != "" {
			part := map[string]any{}
			for _, k := range splitTop(strings.TrimSuffix(sub, "]")) {
				if x, ok := m[strings.TrimSpace(k)]; ok {
					part[strings.TrimSpace(k)] = x
				}
			}
			v = part
		}
		sel[name] = v
	}
	out, _ = json.Marshal(sel)
	return out, exact
}

// splitTop splits on commas that are not inside brackets.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '[':
			depth++
		case ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// sharedPrefixes are the API answers that do not depend on who asks: the login page's
// settings and logo, and the server's language lists. Offline, a user who never fetched
// one online is served another user's copy. Everything else stays per user, metadata
// included: central answers programs, option sets, data elements, org units and the
// rest per user (metadata sharing, from public, group and individual sharing; display
// names in the user's language), and the hub does not evaluate that (REVIEW-2 F4,
// FINDINGS "Shared metadata cache"). A user who never loaded them online gets "needs
// connection" for them offline.
var sharedPrefixes = []string{"loginConfig", "staticContent", "locales"}

// shared reports whether a path's cached response may be served to any user: the
// answers above, and the apps' static files.
func shared(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/")
	if !ok {
		return isStatic(path)
	}
	if m := regexp.MustCompile(`^[0-9]{2}/`).FindString(rest); m != "" {
		rest = rest[len(m):]
	}
	for _, p := range sharedPrefixes {
		if rest == p || strings.HasPrefix(rest, p+"/") || strings.HasPrefix(rest, p+".") || strings.HasPrefix(rest, p+"?") {
			return true
		}
	}
	return isStatic(path)
}

// cacheUser is the cache key owner: the user, or 0 for anonymous requests.
func cacheUser(sess *core.Session) int64 {
	if sess == nil {
		return 0
	}
	return sess.UserID
}

// anonCacheable reports whether an answer to a request without a session may be
// cached: what the login page needs offline, its files and its shared settings, and
// not with a query string of the client's choosing. Anything more lets one device
// fill the disk with copies, and stores central's redirects to the login page under
// app files (review R-16, R-18).
func anonCacheable(u *url.URL) bool {
	if u.RawQuery != "" && u.RawQuery != "paging=false" {
		return false
	}
	if strings.HasPrefix(u.Path, "/dhis-web-login/") {
		return true
	}
	return strings.HasPrefix(u.Path, "/api/") && !isStatic(u.Path) && shared(u.Path)
}

// isAppShell: /apps/<app>, the page 2.42+ opens an app at.
func isAppShell(p string) bool {
	rest, ok := strings.CutPrefix(p, "/apps/")
	return ok && rest != "" && !strings.Contains(rest, "/")
}

// isStatic reports whether a path is an app's static file, which holds no user data
// and may be served offline from any user's cache entry.
func isStatic(p string) bool {
	if strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/api/apps/") {
		return false
	}
	// The apps menu lists the apps the user may open: central answers it per user
	// (checked on 2.42, review R-16).
	if p == "/api/apps/menu.json" {
		return false
	}
	// 2.42+ opens apps at /apps/<app> in its global shell: an app file without an
	// extension.
	if isAppShell(p) {
		return true
	}
	switch strings.ToLower(path.Ext(p)) {
	case ".js", ".css", ".html", ".png", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".json", ".jpg", ".gif", ".map", ".webmanifest", ".txt":
		return true
	}
	return strings.HasSuffix(p, "/")
}

// ServeHTTP handles all paths not routed elsewhere.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sess, f := p.Auth.Identify(r)
	if f != nil {
		auth.WriteFailure(w, f)
		return
	}
	served := "central"
	status := 0
	defer func() { p.H.ReqLog.Record(r, core.ClientKind(r), served, status) }()

	limit := int64(core.MaxBody)
	if sess == nil {
		limit = core.MaxAnonymousBody
	}
	body, err := core.ReadBody(r, limit)
	if err != nil {
		status = http.StatusRequestEntityTooLarge
		p.H.WebError(w, status, "request body too large or unreadable", "")
		return
	}
	if p.H.Online() {
		// A 304 cannot be cached for offline use: a browser holding another user's
		// copy (one browser, two staff) asks conditionally, and this user would have
		// nothing offline. Without a copy the hub could answer offline with, ask
		// central for the full answer.
		if r.Method == http.MethodGet && (r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "") &&
			p.H.Store.Unlocked() && !p.H.Store.HasCache(cacheUser(sess), sess != nil && shared(r.URL.Path), r.Method, r.URL.RequestURI()) {
			r = r.Clone(r.Context())
			r.Header.Del("If-None-Match")
			r.Header.Del("If-Modified-Since")
		}
		if copy := p.localCopy(r, sess); copy != nil {
			served, status = "hub-copy", copy.Status
			Write(w, copy)
			return
		}
		fwd, copy := p.revalidate(r)
		res, err := p.Forward(r.Context(), fwd, body, p.Upstream(sess))
		if err == nil && copy != nil && res.Status == http.StatusNotModified {
			served, status = "hub-copy", copy.Status
			res = p.Localize(r, &Result{Status: copy.Status, Header: copy.Header, Body: copy.Body})
			res.Header.Set("X-Hub-Cache", "revalidated; fetched="+copy.FetchedAt)
			Write(w, res)
			return
		}
		if err == nil {
			if r.Method == http.MethodGet && cacheable(r, res.Status) && len(res.Body) <= maxCacheBody && !validationRe.MatchString(r.URL.Path) &&
				p.H.Store.Unlocked() && (cacheUser(sess) > 0 || sess == nil && anonCacheable(r.URL)) {
				if err := p.H.Store.PutCache(cacheUser(sess), r.Method, r.URL.RequestURI(),
					&store.CachedResponse{Status: res.Status, Header: res.Header, Body: res.Body}); err != nil {
					slog.Warn("cache write", "err", err)
				}
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && res.Status/100 == 2 && p.H.Store.Unlocked() &&
				replayable(r.URL.Path, nil) {
				// A setting or a data store entry changed through the hub: copies of it
				// may now be out of date, for this user and others (not for messages,
				// files and other data, which are not copies' business).
				p.H.MetadataChanged(r.Method + " " + r.URL.Path)
			}
			status = res.Status
			Write(w, p.Localize(r, res))
			return
		}
		if !central.IsNetwork(err) {
			status = 502
			p.H.WebError(w, http.StatusBadGateway, err.Error(), "")
			return
		}
	}
	served = "cache"
	core.MarkOffline(w)
	status = p.serveOffline(w, r, sess)
}

// localCopy answers a metadata or settings request from the user's own copy while
// online, when programs are listed in localReads and the copy was fetched after the
// last change the hub knows of (core/metadata.go). What counts as metadata is what the
// login replay loads (replayable): not data, effects, validation or other users.
func (p *Proxy) localCopy(r *http.Request, sess *core.Session) *Result {
	if r.Method != http.MethodGet || sess == nil || sess.UserID <= 0 || !p.H.Store.Unlocked() ||
		len(p.H.LocalReads().Programs) == 0 {
		return nil
	}
	uri := r.URL.RequestURI()
	me := meRe.MatchString(r.URL.Path)
	// Answers that do not depend on who asks (the login page's settings and logo,
	// languages) come from any user's copy, as offline.
	common := strings.HasPrefix(r.URL.Path, "/api/") && shared(r.URL.Path)
	if !me && !common && !replayable(uri, nil) {
		return nil
	}
	key := uri
	if me {
		key = auth.MeCacheKey // kept at login, the fields selected here as offline
	}
	c, err := p.H.Store.GetCache(sess.UserID, http.MethodGet, key)
	if err == nil && c == nil && common {
		c, err = p.H.Store.GetCacheAnyUser(http.MethodGet, uri)
	}
	if err != nil || c == nil || !store.ParseTime(c.FetchedAt).After(p.H.MetadataCutoff(sess.UserID)) {
		return nil
	}
	res := &Result{Status: c.Status, Header: c.Header, Body: c.Body}
	if me {
		body, exact := selectFields(c.Body, r.URL.Query().Get("fields"))
		if !exact {
			return nil // central answers what the login copy lacks
		}
		res = &Result{Status: 200, Header: http.Header{"Content-Type": {"application/json;charset=UTF-8"}}, Body: body}
	}
	res = p.Localize(r, res)
	res.Header.Set("X-Hub-Cache", "local; fetched="+c.FetchedAt)
	return res
}

// revalidate: a browser without an app's file (a new tablet, a cleared cache, an
// updated app) downloads it through the uplink, and Capture's bundle is 6.9 MB: 62 s at
// 31 KB/s, compressed. When the hub holds a copy, it asks central whether the copy is
// current instead (a 304 of a few hundred bytes) and serves the copy over the facility
// network: 77 s to 1.1 s for Capture's start (docs/PERFORMANCE.md, "A slow uplink").
// Central still decides, as the user, so nothing stale or unauthorised is served.
// Returns the request to forward, and the copy to serve if central answers 304.
func (p *Proxy) revalidate(r *http.Request) (*http.Request, *store.CachedResponse) {
	if r.Method != http.MethodGet || !isStatic(r.URL.Path) || !p.H.Store.Unlocked() ||
		r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" || r.Header.Get("Range") != "" {
		return r, nil
	}
	c, err := p.H.Store.GetCacheAnyUser(r.Method, r.URL.RequestURI())
	if err != nil || c == nil || c.Status != http.StatusOK {
		return r, nil
	}
	etag, modified := c.Header.Get("ETag"), c.Header.Get("Last-Modified")
	if etag == "" && modified == "" {
		return r, nil
	}
	r = r.Clone(r.Context())
	if etag != "" {
		r.Header.Set("If-None-Match", etag)
	}
	if modified != "" {
		r.Header.Set("If-Modified-Since", modified)
	}
	return r, c
}

func (p *Proxy) serveOffline(w http.ResponseWriter, r *http.Request, sess *core.Session) int {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		p.H.NeedsConnection(w, r.Method+" "+r.URL.Path+" is not available offline")
		return 503
	}
	if !p.H.Store.Unlocked() {
		if isStatic(r.URL.Path) {
			p.H.NeedsConnection(w, "the hub is locked until a known user logs in")
			return 503
		}
		return p.unauthenticated(w, r)
	}
	if r.URL.Path == "/api/ping" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("pong")) // the hub is the client's server; it is reachable
		return 200
	}
	if validationRe.MatchString(r.URL.Path) {
		p.H.NeedsConnection(w, "validation runs on central")
		return 503
	}
	uri := r.URL.RequestURI()
	cached, err := p.H.Store.GetCache(cacheUser(sess), r.Method, uri)
	if err == nil && cached == nil && sess != nil && shared(r.URL.Path) {
		cached, err = p.H.Store.GetCacheAnyUser(r.Method, uri)
	}
	if err == nil && cached == nil && sess == nil && isStatic(r.URL.Path) && !strings.HasPrefix(r.URL.Path, "/dhis-web-login") &&
		strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/dhis-web-login/", http.StatusFound)
		return 302
	}
	if err == nil && cached == nil && sess == nil && isStatic(r.URL.Path) {
		cached, err = p.H.Store.GetCacheAnyUser(r.Method, uri)
	}
	if err == nil && cached == nil && sess == nil && strings.HasPrefix(r.URL.Path, "/api/") {
		return p.unauthenticated(w, r)
	}
	if err == nil && cached == nil && sess != nil && meRe.MatchString(r.URL.Path) {
		if me, _ := p.H.Store.GetCache(sess.UserID, "GET", auth.MeCacheKey); me != nil {
			w.Header().Set("Content-Type", "application/json;charset=UTF-8")
			w.Header().Set("X-Hub-Cache", "offline; me")
			body, _ := selectFields(me.Body, r.URL.Query().Get("fields")) // offline: the best it has
			w.Write(body)
			return 200
		}
	}
	if err == nil && cached == nil && r.URL.Path == "/" {
		http.Redirect(w, r, "/dhis-web-capture/index.html", http.StatusFound)
		return 302
	}
	if err == nil && cached == nil && sess != nil && r.Method == "GET" && isAppShell(r.URL.Path) {
		cached, err = p.H.Store.GetAppShell()
	}
	if err != nil || cached == nil {
		p.H.NeedsConnection(w, r.URL.Path+" was not cached while online")
		return 503
	}
	res := p.Localize(r, &Result{Status: cached.Status, Header: cached.Header, Body: cached.Body})
	res.Header.Set("X-Hub-Cache", "offline; fetched="+cached.FetchedAt)
	if ts := store.ParseTime(cached.FetchedAt); !ts.IsZero() {
		res.Header.Set("Age", strconv.Itoa(max(0, int(time.Since(ts).Seconds()))))
	}
	Write(w, res)
	return cached.Status
}

// unauthenticated mirrors central: 401 JSON for XHR, else 302 to the login app.
func (p *Proxy) unauthenticated(w http.ResponseWriter, r *http.Request) int {
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		p.H.WebError(w, 401, "Unauthorized", "")
		return 401
	}
	http.Redirect(w, r, "/dhis-web-login/", http.StatusFound)
	return 302
}
