// Package auth implements hub login (spec 7): online login is forwarded to central and
// registers the user on the hub (key slot, PAT, access list); offline login opens the
// user's key slot, which both checks the password and unlocks the store.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/compat"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
)

type Auth struct {
	H   *core.Hub
	thr throttle
}

// Failure is a login answer to send back verbatim.
type Failure struct {
	Status int
	Body   any
	Raw    []byte // central's body, when forwarding
	Header http.Header
}

func (a *Auth) api() compat.API {
	if api := a.H.API(); api != nil {
		return api
	}
	return compat.For("2.42")
}

const needsOnlineAgain = "needs connection: central refused this account; log in again online"

// LoginRequest is the body of POST /api/auth/login.
type LoginRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	TwoFactor string `json:"twoFactorCode,omitempty"`
}

// LoginWeb handles the login app's JSON login from src (the client address). It
// returns the session and central's (or the hub's) answer body for the client.
func (a *Auth) LoginWeb(ctx context.Context, src string, req LoginRequest, rawBody []byte) (*core.Session, any, *Failure) {
	if f := a.throttled(src, req.Username); f != nil {
		return nil, nil, f
	}
	sess, body, f := a.loginWeb(ctx, req, rawBody)
	a.count(src, req.Username, f)
	return sess, body, f
}

// throttled refuses a login attempt while src is locked out for user.
func (a *Auth) throttled(src, user string) *Failure {
	d := a.thr.wait(src, user)
	if d <= 0 {
		return nil
	}
	return &Failure{Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {strconv.Itoa(int(d.Seconds()) + 1)}},
		Body: a.api().WebMessage(http.StatusTooManyRequests, "too many failed logins; try again later", "")}
}

// Throttled and Count apply the login throttle to logins the hub checks elsewhere
// (the setup page).
func (a *Auth) Throttled(src, user string) *Failure { return a.throttled(src, user) }

func (a *Auth) Count(src, user string, failed bool) {
	if failed {
		a.thr.fail(src, user)
	} else {
		a.thr.ok(src, user)
	}
}

func (a *Auth) count(src, user string, f *Failure) {
	switch {
	case f == nil:
		a.thr.ok(src, user)
	case f.Status == http.StatusUnauthorized:
		a.thr.fail(src, user)
	}
}

func (a *Auth) loginWeb(ctx context.Context, req LoginRequest, rawBody []byte) (*core.Session, any, *Failure) {
	if a.H.Online() {
		resp, err := a.H.Central.Do(ctx, "POST", "/api/auth/login", rawBody, central.Auth{}, http.Header{"Content-Type": {"application/json"}})
		if err == nil {
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			var body struct {
				LoginStatus string `json:"loginStatus"`
				RedirectURL string `json:"redirectUrl"`
			}
			json.Unmarshal(b, &body)
			if resp.StatusCode != http.StatusOK || body.LoginStatus != "SUCCESS" {
				return nil, nil, &Failure{Status: resp.StatusCode, Raw: b}
			}
			cookie := centralCookie(resp)
			sess := a.registerOnline(ctx, req.Username, req.Password, central.Auth{Cookie: cookie})
			return sess, a.api().LoginSuccess(a.relative(body.RedirectURL)), nil
		}
		if !central.IsNetwork(err) {
			return nil, nil, &Failure{Status: 502, Body: a.api().WebMessage(502, err.Error(), "")}
		}
	}
	sess, f := a.loginOffline(req.Username, req.Password)
	if f != nil {
		return nil, nil, f
	}
	return sess, a.api().LoginSuccess("/"), nil
}

// LoginBasic handles HTTP Basic auth (Android and API clients) from src. It runs the
// online or offline login the first time a credential is seen (spec 7.1).
func (a *Auth) LoginBasic(ctx context.Context, src, username, password string) (*core.Session, *Failure) {
	if cached, _ := a.H.Sessions.Basic(username, password); cached == nil {
		if f := a.throttled(src, username); f != nil {
			return nil, f
		}
	}
	sess, f := a.loginBasic(ctx, username, password)
	a.count(src, username, f)
	return sess, f
}

func (a *Auth) loginBasic(ctx context.Context, username, password string) (*core.Session, *Failure) {
	cached, fresh := a.H.Sessions.Basic(username, password)
	if cached != nil && (fresh || !a.H.Online()) {
		return cached, nil
	}
	if a.H.Online() {
		auth := central.Auth{Username: username, Password: password}
		resp, err := a.H.Central.Do(ctx, "GET", "/api/me?fields=id", nil, auth, nil)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				if resp.StatusCode == http.StatusUnauthorized {
					// Disabled account or changed password: end the user's hub
					// sessions, and keep them from logging in offline until an
					// online login succeeds again.
					a.H.Sessions.DropUser(username)
					if u, _ := a.H.Store.UserByName(username); u != nil {
						a.H.Store.SetNeedsLogin(u.ID, true)
					}
				}
				return nil, &Failure{Status: resp.StatusCode, Raw: b, Header: http.Header{"Content-Type": resp.Header["Content-Type"]}}
			}
			if cached != nil {
				a.H.Sessions.Verified(cached)
				return cached, nil
			}
			sess := a.registerOnline(ctx, username, password, auth)
			a.H.Sessions.SetBasic(username, password, sess)
			return sess, nil
		}
		if !central.IsNetwork(err) {
			return nil, &Failure{Status: 502, Body: a.api().WebMessage(502, err.Error(), "")}
		}
		if cached != nil {
			return cached, nil // central went away mid-check; the credential was good
		}
	}
	sess, f := a.loginOffline(username, password)
	if f == nil {
		a.H.Sessions.SetBasic(username, password, sess)
	}
	return sess, f
}

// registerOnline runs steps 2-5 of online login after central accepted the password.
// Failures here never fail the login: the user can still work through the hub online.
func (a *Auth) registerOnline(ctx context.Context, username, password string, with central.Auth) *core.Session {
	h := a.H
	me, err := h.FetchMe(ctx, with)
	if err != nil {
		slog.Warn("login: /api/me failed", "user", username, "err", err)
		return h.Sessions.New(0, username, with)
	}
	userID, err := a.ensureKeySlot(username, me.ID, password)
	if err != nil {
		slog.Warn("login: user not registered on hub", "user", username, "err", err)
		return h.Sessions.New(0, username, with)
	}
	u, _ := h.Store.UserByID(userID)
	if _, err := h.RefreshServerVersion(ctx, with); err != nil {
		slog.Warn("login: system info", "err", err)
	}
	if err := h.EnsurePAT(ctx, u, with, password); err != nil { // replaces a refused PAT (needs_login)
		slog.Warn("login: PAT", "user", username, "err", err)
	}
	h.Store.SetNeedsLogin(userID, false) // central just accepted the password
	h.AddUserPrograms(ctx, with)
	if err := h.RefreshAccess(ctx, userID); err != nil {
		slog.Warn("login: access refresh", "user", username, "err", err)
	}
	a.storeMe(ctx, userID)
	h.Store.TouchLogin(userID)
	slog.Info("online login", "user", username)
	return h.Sessions.New(userID, username, with)
}

// MeCacheKey is where the user's full /api/me is kept; offline, /api/me with any
// fields selection is answered from it (Capture asks for many variants).
const MeCacheKey = "hub:/api/me"

func (a *Auth) storeMe(ctx context.Context, userID int64) {
	auth, err := a.H.UpstreamAuth(userID)
	if err != nil {
		return
	}
	resp, err := a.H.Central.Do(ctx, "GET", "/api/me", nil, auth, nil)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		a.H.Store.PutCache(userID, "GET", MeCacheKey, &store.CachedResponse{Status: 200,
			Header: http.Header{"Content-Type": {"application/json;charset=UTF-8"}}, Body: b})
	}
}

var errLockedNewUser = errors.New("store is locked and this user has no key slot; a known user must log in first")

// ensureKeySlot makes sure the user has a key slot that opens with this password,
// unlocking the store if needed (spec 7.1 steps 3-4, spec 9.1). The slot is the only
// password verifier the hub keeps.
func (a *Auth) ensureKeySlot(username, userUID, password string) (int64, error) {
	h := a.H
	u, err := h.Store.UserByName(username)
	if err != nil {
		return 0, err
	}
	if !h.Store.Unlocked() {
		if u == nil {
			return 0, errLockedNewUser
		}
		dk, err := hubcrypto.UnwrapDK(u.WrappedDK, password, u.KEKSalt)
		if err != nil {
			// Password changed on central while the store was locked; the old slot
			// cannot be opened with the new password.
			return 0, errors.New("key slot does not open with the current password; another known user must log in first")
		}
		if err := h.Unlock(dk); err != nil {
			return 0, err
		}
		if u, err = h.Store.UserByID(u.ID); err != nil {
			return 0, err
		}
		if u.UserUID != userUID {
			return a.writeSlot(username, userUID, password)
		}
		return u.ID, nil
	}
	if u != nil && u.UserUID == userUID && slotOpens(u, password) {
		return u.ID, nil
	}
	// New user, or password changed (detected at online login): replace the slot.
	return a.writeSlot(username, userUID, password)
}

func slotOpens(u *store.User, password string) bool {
	_, err := hubcrypto.UnwrapDK(u.WrappedDK, password, u.KEKSalt)
	return err == nil
}

func (a *Auth) writeSlot(username, userUID, password string) (int64, error) {
	dk, err := a.H.Store.DK()
	if err != nil {
		return 0, err
	}
	salt, err := hubcrypto.RandomBytes(16)
	if err != nil {
		return 0, err
	}
	wrapped, err := hubcrypto.WrapDK(dk, password, salt)
	if err != nil {
		return 0, err
	}
	return a.H.Store.UpsertCredentials(username, userUID, salt, wrapped)
}

func (a *Auth) loginOffline(username, password string) (*core.Session, *Failure) {
	h := a.H
	u, err := h.Store.UserByName(username)
	if err != nil || u == nil {
		// The same answer, after the same work, as a wrong password: offline the hub
		// must not tell which usernames it knows (review M3).
		hubcrypto.DeriveKEK(password, dummySalt)
		st, body := a.api().LoginFailure()
		return nil, &Failure{Status: st, Body: body}
	}
	dk, err := hubcrypto.UnwrapDK(u.WrappedDK, password, u.KEKSalt)
	if err != nil {
		st, body := a.api().LoginFailure()
		return nil, &Failure{Status: st, Body: body}
	}
	if u.NeedsLogin {
		// Central refused this user's token or password since the last online login
		// (spec 8.3 cannot see revoked access offline; this it can see).
		return nil, &Failure{Status: 401, Body: a.api().WebMessage(401, needsOnlineAgain, "")}
	}
	if !h.Store.Unlocked() {
		if err := h.Unlock(dk); err != nil {
			return nil, &Failure{Status: 500, Body: a.api().WebMessage(500, "unlock failed", "")}
		}
	}
	h.Store.TouchLogin(u.ID)
	slog.Info("offline login", "user", username)
	return h.Sessions.New(u.ID, username, central.Auth{}), nil
}

// centralCookie returns "NAME=value" of central's session cookie. The name is set
// per instance (JSESSIONID, JSESSIONID_<name>, ...), so match the prefix.
func centralCookie(resp *http.Response) string {
	var parts []string
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "JSESSIONID") || strings.HasPrefix(c.Name, "SESSION") {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

func (a *Auth) relative(u string) string {
	if base := a.H.Central.BaseURL(); base != "" && strings.HasPrefix(u, base) {
		u = strings.TrimPrefix(u, base)
	}
	if u == "" {
		return "/"
	}
	return u
}

// --- request identification ---------------------------------------------------------

// Identify returns the request's session: hub cookie or HTTP Basic. A Basic credential
// that fails login yields a Failure to send back.
func (a *Auth) Identify(r *http.Request) (*core.Session, *Failure) {
	if s := a.H.Sessions.FromCookie(r); s != nil {
		if !a.mustLogInAgain(s) {
			return s, nil
		}
		return nil, &Failure{Status: 401, Body: a.api().WebMessage(401, needsOnlineAgain, "")}
	}
	if user, pw, ok := r.BasicAuth(); ok {
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		s, f := a.LoginBasic(ctx, Source(r), user, pw)
		if s != nil && a.mustLogInAgain(s) {
			// Online, a fresh Basic login may clear the flag; offline it refuses.
			s, f = a.LoginBasic(ctx, Source(r), user, pw)
			if s != nil && a.mustLogInAgain(s) {
				return nil, &Failure{Status: 401, Body: a.api().WebMessage(401, needsOnlineAgain, "")}
			}
		}
		return s, f
	}
	return nil, nil
}

// mustLogInAgain ends a session whose user central has refused since it began (a
// push or access refresh got 401). Without this a disabled user kept reading and
// writing through the hub until it restarted (review M4).
func (a *Auth) mustLogInAgain(s *core.Session) bool {
	if s.UserID == 0 {
		return false
	}
	u, err := a.H.Store.UserByID(s.UserID)
	if err != nil || u == nil || !u.NeedsLogin {
		return false
	}
	a.H.Sessions.DropUser(s.Username)
	return true
}

// WriteFailure sends a login failure.
func WriteFailure(w http.ResponseWriter, f *Failure) {
	for k, v := range f.Header {
		w.Header()[k] = v
	}
	if f.Raw != nil {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json;charset=UTF-8")
		}
		w.WriteHeader(f.Status)
		w.Write(f.Raw)
		return
	}
	core.WriteJSON(w, f.Status, f.Body)
}

// --- handlers --------------------------------------------------------------------------

// HandleLogin serves POST /api/auth/login.
func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		a.H.WebError(w, 405, "Method not allowed", "")
		return
	}
	raw, _ := core.ReadBody(r, 1<<16)
	var req LoginRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.Username == "" {
		a.H.WebError(w, 400, "Invalid login request", "")
		return
	}
	sess, body, f := a.LoginWeb(r.Context(), Source(r), req, raw)
	if f != nil {
		WriteFailure(w, f)
		return
	}
	core.SetSessionCookie(w, sess, a.H.Cfg.TLS, a.H.Sessions.Max)
	core.WriteJSON(w, 200, body)
}

// HandleLogout serves the 2.42 header bar's logout (GET .../logout.action -> 302 /).
func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if s := a.H.Sessions.FromCookie(r); s != nil {
		a.H.Sessions.Delete(s.Token)
		a.H.Sessions.DropBasic(s.Username)
		if s.Upstream.Cookie != "" && a.H.Online() {
			// End the central session the login created too.
			if resp, err := a.H.Central.Do(r.Context(), "GET", "/dhis-web-commons-security/logout.action", nil, s.Upstream, nil); err == nil {
				resp.Body.Close()
			}
		}
	}
	core.ClearSessionCookie(w, a.H.Cfg.TLS)
	http.Redirect(w, r, "/", http.StatusFound)
}

// dummySalt is used to spend the cost of a key slot check on unknown usernames.
var dummySalt = []byte("facility-hub-no-such-user")
