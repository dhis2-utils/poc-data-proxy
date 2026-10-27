package core

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/dhis2/facility-hub/internal/central"
)

// RegistryPath is a hub's entry in central's facility-hub-registry.
func RegistryPath(hubID string) string {
	return "/api/dataStore/facility-hub-registry/" + url.PathEscape(hubID)
}

// EditRegistry reads this hub's registry entry, lets edit change it, and writes it back
// when edit reports a change. Fields others wrote (the Facility Hub app, a replacing
// hub) are kept.
func (h *Hub) EditRegistry(ctx context.Context, auth central.Auth, edit func(reg map[string]any) bool) error {
	path := RegistryPath(h.HubID())
	var reg map[string]any
	if err := h.Central.JSON(ctx, "GET", path, nil, auth, &reg); err != nil {
		return err
	}
	if reg == nil || !edit(reg) {
		return nil
	}
	return h.Central.JSON(ctx, "PUT", path, reg, auth, nil)
}

// refreshConfig reads the country's setting facility-hub-config/<key> into the store's
// state under stateKey; a missing key is stored as {} (the defaults).
func (h *Hub) refreshConfig(ctx context.Context, key, stateKey string) error {
	var raw json.RawMessage
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-config/"+key, nil, h.HubAuth(), &raw)
	if central.Status(err) == http.StatusNotFound {
		raw, err = json.RawMessage(`{}`), nil
	}
	if err != nil {
		return err
	}
	return h.Store.SetState(stateKey, raw)
}

// configState decodes the setting refreshConfig stored under stateKey into v; v keeps
// its zero value when there is none.
func (h *Hub) configState(stateKey string, v any) {
	if b, err := h.Store.State(stateKey); err == nil && b != nil {
		json.Unmarshal(b, v)
	}
}

// writableByAll reports whether central lets any user write the dataStore entry at
// path: its sharing's public access, as 2.42 shows it in the entry's metaData.
func (h *Hub) writableByAll(ctx context.Context, path string) (bool, error) {
	var meta struct {
		Sharing struct {
			Public string `json:"public"`
		} `json:"sharing"`
	}
	if err := h.Central.JSON(ctx, "GET", path+"/metaData", nil, h.HubAuth(), &meta); err != nil {
		return false, err
	}
	return len(meta.Sharing.Public) < 2 || meta.Sharing.Public[1] == 'w', nil
}

// MetaUnprotected lists the dataStore entries the hub acts on that any user may
// write, as last checked.
const MetaUnprotected = "unprotected"

// CheckProtection notes which of the dataStore entries the hub acts on any user may
// write (docs/DEPLOYMENT.md, "Protect the data store namespaces"), for the heartbeat
// and the admin page. Missing entries are not listed.
func (h *Hub) CheckProtection(ctx context.Context) {
	var open []string
	for _, path := range []string{"/api/dataStore/facility-hub-admin/accounts", "/api/dataStore/facility-hub-admin/central-move",
		"/api/dataStore/facility-hub-admin/release", "/api/dataStore/facility-hub-admin/retries", RegistryPath(h.HubID())} {
		o, err := h.writableByAll(ctx, path)
		if central.IsNetwork(err) {
			return
		}
		if err == nil && o && path == RegistryPath(h.HubID()) {
			o = !h.ProtectRegistry(ctx, h.HubAuth())
		}
		if err == nil && o {
			open = append(open, strings.TrimPrefix(path, "/api/dataStore/"))
		}
	}
	if len(open) > 0 {
		slog.Warn("dataStore entries the hub acts on are writable by any user; protect them on central (docs/DEPLOYMENT.md)", "entries", open)
	}
	h.Store.SetMeta(MetaUnprotected, strings.Join(open, " "))
}

// ProtectRegistry shares this hub's registry entry as readable by all and writable by
// the groups named in facility-hub-admin/accounts: the hub owners (set-up, take-over,
// replacement), the hub accounts and the hub managers (approvals). A new entry is
// writable by any user on 2.42; its owner (whoever set the hub up) may change its
// sharing. Without a hub owners group named, the entry is left as it is: a staff
// member could no longer take the hub over. It reports whether the entry is shared so.
func (h *Hub) ProtectRegistry(ctx context.Context, auth central.Auth) bool {
	var acc struct{ Group, Managers, Owners string }
	if err := h.Central.JSON(ctx, "GET", "/api/dataStore/facility-hub-admin/accounts", nil, auth, &acc); err != nil || acc.Owners == "" {
		return false
	}
	var meta struct {
		ID string `json:"id"`
	}
	if err := h.Central.JSON(ctx, "GET", RegistryPath(h.HubID())+"/metaData", nil, auth, &meta); err != nil {
		return false
	}
	groups := []map[string]string{}
	for _, g := range []string{acc.Owners, acc.Group, acc.Managers} {
		if g != "" {
			groups = append(groups, map[string]string{"id": g, "access": "rw------"})
		}
	}
	body := map[string]any{"object": map[string]any{"publicAccess": "r-------", "userAccesses": []any{}, "userGroupAccesses": groups}}
	if err := h.Central.JSON(ctx, "POST", "/api/sharing?type=dataStore&id="+url.QueryEscape(meta.ID), body, auth, nil); err != nil {
		slog.Warn("the hub's registry entry is not protected", "err", err)
		return false
	}
	return true
}

// Unprotected is what CheckProtection last found.
func (h *Hub) Unprotected() []string {
	return strings.Fields(h.Store.Meta(MetaUnprotected))
}
