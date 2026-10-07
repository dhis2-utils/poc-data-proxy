package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
// logging in again (which is also what unlocks the store).
type Session struct {
	Token    string
	UserID   int64 // 0 when the user could not be registered (store locked, new user)
	Username string
	Created  time.Time
	// Upstream is a memory-only credential (central session cookie or Basic) used for
	// pass-through when the user has no stored PAT, e.g. while the store is locked.
	Upstream central.Auth
}

type Sessions struct {
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
	return &Sessions{byTok: map[string]*Session{}, basic: map[string]*Session{}, salt: salt}
}

func (s *Sessions) New(userID int64, username string, upstream central.Auth) *Session {
	b := make([]byte, 24)
	rand.Read(b)
	sess := &Session{Token: hex.EncodeToString(b), UserID: userID, Username: username, Created: time.Now(), Upstream: upstream}
	s.mu.Lock()
	s.byTok[sess.Token] = sess
	s.mu.Unlock()
	return sess
}

func (s *Sessions) Get(token string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byTok[token]
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

func (s *Sessions) Basic(user, pw string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.basic[s.basicKey(user, pw)]
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

func SetSessionCookie(w http.ResponseWriter, sess *Session, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: sess.Token, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode})
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
	file    *os.File
	enabled bool
}

func NewRequestLog() *RequestLog {
	return &RequestLog{counts: map[string]int{}, unsupp: map[string]int{}}
}

// Open starts appending one line per request to dir/requests.log.
func (l *RequestLog) Open(dir string) {
	f, err := os.OpenFile(filepath.Join(dir, "requests.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
