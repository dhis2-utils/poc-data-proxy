// Package proxy is the pass-through for every path the hub does not handle itself
// (spec 5.1). Online it forwards to central with the user's credentials and caches GET
// responses per user; offline it serves those cached responses.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
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
	hdr := r.Header.Clone()
	for _, k := range hopHeaders {
		hdr.Del(k)
	}
	hdr.Del("X-Forwarded-For")
	resp, err := p.H.Central.Do(ctx, r.Method, r.URL.RequestURI(), body, a, hdr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		p.H.Monitor.ReportFailure(err)
		return nil, central.ErrNetwork
	}
	h := resp.Header.Clone()
	for _, k := range droppedResponseHeaders {
		h.Del(k)
	}
	return &Result{Status: resp.StatusCode, Header: h, Body: b}, nil
}

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

// selectFields applies a top-level DHIS2 fields selection ("authorities",
// "settings[keyUiLocale]") to a JSON object, as central does for /api/me. Fields the
// object lacks are left out ("userCredentials[userRoles]" gives {} on 2.42).
func selectFields(body []byte, fields string) []byte {
	var obj map[string]any
	if fields == "" || json.Unmarshal(body, &obj) != nil {
		return body
	}
	out := map[string]any{}
	for _, f := range splitTop(fields) {
		name, sub, _ := strings.Cut(f, "[")
		name = strings.TrimSpace(name)
		if name == "*" || name == ":all" {
			return body
		}
		v, ok := obj[name]
		if !ok {
			continue
		}
		if m, isMap := v.(map[string]any); isMap && sub != "" {
			sel := map[string]any{}
			for _, k := range splitTop(strings.TrimSuffix(sub, "]")) {
				if x, ok := m[strings.TrimSpace(k)]; ok {
					sel[strings.TrimSpace(k)] = x
				}
			}
			v = sel
		}
		out[name] = v
	}
	b, _ := json.Marshal(out)
	return b
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

// isStatic reports whether a path is an app's static file, which holds no user data
// and may be served offline from any user's cache entry.
func isStatic(p string) bool {
	if strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/api/apps/") {
		return false
	}
	// 2.42+ opens apps at /apps/<app> in its global shell: an app file without an
	// extension.
	if rest, ok := strings.CutPrefix(p, "/apps/"); ok && rest != "" && !strings.Contains(rest, "/") {
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
		res, err := p.Forward(r.Context(), r, body, p.Upstream(sess))
		if err == nil {
			if r.Method == http.MethodGet && cacheable(r, res.Status) && len(res.Body) <= maxCacheBody &&
				p.H.Store.Unlocked() && (cacheUser(sess) > 0 || sess == nil) {
				if err := p.H.Store.PutCache(cacheUser(sess), r.Method, r.URL.RequestURI(),
					&store.CachedResponse{Status: res.Status, Header: res.Header, Body: res.Body}); err != nil {
					slog.Warn("cache write", "err", err)
				}
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
			w.Write(selectFields(me.Body, r.URL.Query().Get("fields")))
			return 200
		}
	}
	if err == nil && cached == nil && r.URL.Path == "/" {
		http.Redirect(w, r, "/dhis-web-capture/index.html", http.StatusFound)
		return 302
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
