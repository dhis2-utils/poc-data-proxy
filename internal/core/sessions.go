package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
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
