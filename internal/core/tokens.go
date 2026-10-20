package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
)

// Personal access tokens on DHIS2 2.42.6 outlive their user: disabling the user,
// changing their password or expiring their account does not stop them, and nobody but
// the owner can delete one, not even a superuser (FINDINGS.md, "Tokens outlive their
// user"). So the hub shares each token it makes with the hub managers, who can then
// delete it, and lists them in its registry entry for the Facility Hub app's "Revoke all
// tokens of this hub". How long tokens live is the country's setting.

const tokenSettingsKey = "token_settings"

// TokenLife is how long new tokens live: facility-hub-config/tokens on central, kept
// within 7–365 days, else pat_expiry_days.
func (h *Hub) TokenLife() time.Duration {
	days := h.Cfg.PATExpiryDays
	var s struct {
		ExpiryDays int `json:"expiryDays"`
	}
	if b, err := h.Store.State(tokenSettingsKey); err == nil && b != nil && json.Unmarshal(b, &s) == nil && s.ExpiryDays > 0 {
		days = min(max(s.ExpiryDays, 7), 365)
	}
	return time.Duration(days) * 24 * time.Hour
}

// renewToken reports whether a token expiring at exp is replaced now: within a week of
// its end, or living longer than the country now allows.
func (h *Hub) renewToken(exp time.Time) bool {
	left := time.Until(exp)
	return left <= 7*24*time.Hour || left > h.TokenLife()+24*time.Hour
}

// refreshTokenSettings reads facility-hub-config/tokens; a missing key means
// pat_expiry_days.
func (h *Hub) refreshTokenSettings(ctx context.Context) error {
	var raw json.RawMessage
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-config/tokens", nil, h.HubAuth(), &raw)
	if central.Status(err) == http.StatusNotFound {
		raw, err = json.RawMessage(`{}`), nil
	}
	if err != nil {
		return err
	}
	return h.Store.SetState(tokenSettingsKey, raw)
}

// shareWithManagers shares a new token with the hub managers group named in
// facility-hub-admin/accounts, as its owner. Best effort: a token that is not shared
// can still be deleted by its owner, and the app reports the ones it could not reach.
func (h *Hub) shareWithManagers(ctx context.Context, owner central.Auth, uid string) {
	var acc struct {
		Managers string `json:"managers"`
	}
	if err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-admin/accounts", nil, owner, &acc); err != nil || acc.Managers == "" {
		return // no hub managers named yet
	}
	body := map[string]any{"object": map[string]any{"publicAccess": "--------",
		"userGroupAccesses": []map[string]string{{"id": acc.Managers, "access": "rw------"}}}}
	if err := h.Central.JSON(ctx, "POST", "/api/sharing?type=apiToken&id="+url.QueryEscape(uid), body, owner, nil); err != nil {
		slog.Warn("token not shared with the hub managers", "err", err)
	}
}

// TokenUIDs lists the central IDs of the tokens this hub holds: its own and its users'.
func (h *Hub) TokenUIDs() []string {
	var out []string
	if rt := h.Runtime(); rt != nil && rt.HubTokenUID != "" {
		out = append(out, rt.HubTokenUID)
	}
	users, _ := h.Store.Users()
	for _, u := range users {
		if u.PATUID != "" && u.HasPAT {
			out = append(out, u.PATUID)
		}
	}
	slices.Sort(out)
	return out
}

// Revoked reports whether the Facility Hub app revoked this hub's tokens.
func (h *Hub) Revoked() bool { return h.Store.Meta(MetaRetired) == retiredRevoked }

// DropTokens deletes on central every token the hub still holds, each as its owner,
// and forgets them. A revoked hub makes no new ones.
func (h *Hub) DropTokens(ctx context.Context) {
	users, _ := h.Store.Users()
	for _, u := range users {
		if !u.HasPAT {
			continue
		}
		if key, err := h.Store.PAT(u); err == nil && u.PATUID != "" {
			h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(u.PATUID), nil, central.Auth{PAT: key}, nil)
		}
		h.Store.SetPAT(u.ID, "", "", "")
		h.Store.SetPassword(u.ID, "")
	}
	if rt := h.Runtime(); rt != nil && rt.HubToken != "" {
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(rt.HubTokenUID), nil, h.HubAuth(), nil)
		h.UpdateRuntime(func(rt *Runtime) {
			rt.HubToken, rt.HubTokenUID, rt.HubTokenExp = "", "", ""
			rt.HubPassword, rt.HubPasswordNext = "", ""
		})
	}
	slog.Warn("tokens revoked on central: this hub dropped the tokens it held")
}
