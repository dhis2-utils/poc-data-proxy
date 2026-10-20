package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/dhis2/facility-hub/internal/logfile"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
)

const SessionCookie = "HUBSESSION"

// Session is a logged-in user. Sessions live in memory only; a hub restart means
// logging in again (which is also what unlocks the store). A session ends after
// SessionIdle without requests or SessionMax after login (review M4).
type Session struct {
	Token    string
	UserID   int64 // 0 when the user could not be registered (store locked, new user)
	Username string
	Created  time.Time
	lastSeen time.Time
	// verified is when central last accepted a cached Basic credential.
	verified time.Time
	// Upstream is a memory-only credential (central session cookie or Basic) used for
	// pass-through when the user has no stored PAT, e.g. while the store is locked.
	Upstream central.Auth
}

type Sessions struct {
	Idle time.Duration // end a session after this long without requests
	Max  time.Duration // end a session this long after login

	mu    sync.Mutex
	byTok map[string]*Session
	// basic caches verified Basic credentials (sha256 of salt+user+password) so the
	// Argon2 check does not run on every Android request.
	basic map[string]*Session
	salt  []byte
}

func NewSessions() *Sessions {
	salt := make([]byte, 16)
	rand.Read(salt)
	return &Sessions{Idle: time.Hour, Max: 12 * time.Hour, byTok: map[string]*Session{}, basic: map[string]*Session{}, salt: salt}
}

// BasicRecheck is how often a cached Basic credential is checked with central again
// while online, so a disabled account or a changed password stops working.
var BasicRecheck = 5 * time.Minute

// live reports whether sess is within its idle and absolute lifetime, and marks it
// used. Callers hold s.mu.
func (s *Sessions) live(sess *Session) bool {
	now := time.Now()
	if now.Sub(sess.Created) > s.Max || now.Sub(sess.lastSeen) > s.Idle {
		return false
	}
	sess.lastSeen = now
	return true
}

func (s *Sessions) New(userID int64, username string, upstream central.Auth) *Session {
	b := make([]byte, 24)
	rand.Read(b)
	now := time.Now()
	sess := &Session{Token: hex.EncodeToString(b), UserID: userID, Username: username, Created: now, lastSeen: now,
		verified: now, Upstream: upstream}
	s.mu.Lock()
	s.byTok[sess.Token] = sess
	s.mu.Unlock()
	return sess
}

func (s *Sessions) Get(token string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.byTok[token]
	if sess != nil && !s.live(sess) {
		delete(s.byTok, token)
		return nil
	}
	return sess
}

func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	delete(s.byTok, token)
	s.mu.Unlock()
}

func (s *Sessions) Clear() {
	s.mu.Lock()
	s.byTok = map[string]*Session{}
	s.basic = map[string]*Session{}
	s.mu.Unlock()
}

func (s *Sessions) basicKey(user, pw string) string {
	h := sha256.New()
	h.Write(s.salt)
	fmt.Fprintf(h, "%s\x00%s", user, pw)
	return hex.EncodeToString(h.Sum(nil))
}

// Basic returns the cached session for a verified Basic credential, and whether
// central accepted it within BasicRecheck.
func (s *Sessions) Basic(user, pw string) (sess *Session, fresh bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.basicKey(user, pw)
	sess = s.basic[k]
	if sess == nil {
		return nil, false
	}
	if !s.live(sess) {
		delete(s.basic, k)
		return nil, false
	}
	return sess, time.Since(sess.verified) < BasicRecheck
}

// Verified records that central accepted the session's credential just now.
func (s *Sessions) Verified(sess *Session) {
	s.mu.Lock()
	sess.verified = time.Now()
	s.mu.Unlock()
}

// DropBasic forgets a user's cached Basic credentials, so the next Basic request
// is checked again (on logout).
func (s *Sessions) DropBasic(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.basic {
		if v.Username == username {
			delete(s.basic, k)
		}
	}
}

// DropUser ends every session of a user: logout, a credential central refused, or a
// user the hub knows must log in online again.
func (s *Sessions) DropUser(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.byTok {
		if v.Username == username {
			delete(s.byTok, k)
		}
	}
	for k, v := range s.basic {
		if v.Username == username {
			delete(s.basic, k)
		}
	}
}

func (s *Sessions) SetBasic(user, pw string, sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.basic { // one cached credential per user
		if v.Username == user {
			delete(s.basic, k)
		}
	}
	s.basic[s.basicKey(user, pw)] = sess
}

// FromCookie returns the session for the request's hub cookie, if any.
func (s *Sessions) FromCookie(r *http.Request) *Session {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil
	}
	return s.Get(c.Value)
}

func SetSessionCookie(w http.ResponseWriter, sess *Session, secure bool, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: sess.Token, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(maxAge.Seconds())})
}

func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure})
}

// --- request log (spec 13.1: which endpoints and parameters clients call) ---------

// apiWords are 11-character API path words that look like UIDs.
var apiWords = map[string]bool{"loginConfig": true, "programType": true, "periodTypes": true, "lockExceptions": true}

var uidSegment = regexp.MustCompile(`/[a-zA-Z][a-zA-Z0-9]{10}(/|$)`)

type RequestLog struct {
	mu      sync.Mutex
	counts  map[string]int
	unsupp  map[string]int
	file    *logfile.File
	enabled bool
}

func NewRequestLog() *RequestLog {
	return &RequestLog{counts: map[string]int{}, unsupp: map[string]int{}}
}

// Open starts appending one line per request to dir/requests.log.
func (l *RequestLog) Open(dir string) {
	// Capped like hub.log (internal/logfile): log_requests may stay on for months.
	f, err := logfile.Open(filepath.Join(dir, "requests.log"), 10<<20)
	if err != nil {
		slog.Warn("request log", "err", err)
		return
	}
	l.mu.Lock()
	l.file, l.enabled = f, true
	l.mu.Unlock()
}

// Template turns "/api/tracker/events/AbCdEfGhIjK" into "/api/tracker/events/{uid}".
func Template(path string) string {
	for {
		n := uidSegment.ReplaceAllStringFunc(path, func(seg string) string {
			if apiWords[strings.Trim(seg, "/")] { // "loginConfig" is a word, not a UID
				return seg
			}
			if strings.HasSuffix(seg, "/") {
				return "/{uid}/"
			}
			return "/{uid}"
		})
		if n == path {
			return n
		}
		path = n
	}
}

// Record logs method, path template, parameter names, client and how it was served.
func (l *RequestLog) Record(r *http.Request, client, servedBy string, status int) {
	var params []string
	for k := range r.URL.Query() {
		params = append(params, k)
	}
	slices.Sort(params)
	key := fmt.Sprintf("%s %s ?%s [%s]", r.Method, Template(r.URL.Path), strings.Join(params, ","), client)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.counts[key]++
	if l.enabled {
		// Parameter names only: values can hold search terms (patient names).
		fmt.Fprintf(l.file, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), client, r.Method,
			Template(r.URL.Path), status, servedBy, strings.Join(params, ","))
	}
}

// Unsupported records an offline parameter the hub could not serve (spec 10.1).
func (l *RequestLog) Unsupported(path, param string) {
	slog.Warn("not supported offline", "path", path, "param", param)
	l.mu.Lock()
	l.unsupp[Template(path)+" "+param]++
	l.mu.Unlock()
}

func (l *RequestLog) Snapshot() (counts, unsupported map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	counts, unsupported = map[string]int{}, map[string]int{}
	for k, v := range l.counts {
		counts[k] = v
	}
	for k, v := range l.unsupp {
		unsupported[k] = v
	}
	return
}

// SafeQuery renders a query with the values of free-text parameters (filter, query)
// replaced, for logs.
func SafeQuery(q map[string][]string) string {
	var parts []string
	for k, vs := range q {
		for _, v := range vs {
			if k == "filter" || k == "query" {
				v = "***"
			}
			parts = append(parts, k+"="+v)
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, "&")
}

// ClientKind guesses web vs Android from the User-Agent.
func ClientKind(r *http.Request) string {
	ua := r.Header.Get("User-Agent")
	switch {
	case strings.Contains(ua, "okhttp") || strings.Contains(ua, "Dhis2") || strings.Contains(ua, "Android"):
		return "android"
	case strings.Contains(ua, "Mozilla"):
		return "web"
	}
	return "other"
}

// Close closes the request log file.
func (l *RequestLog) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

// Opened reports whether the request log file is open.
func (l *RequestLog) Opened() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file != nil
}
