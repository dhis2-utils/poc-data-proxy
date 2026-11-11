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
// state under stateKey; a missing key is stored as {} (the defaults). An approved hub
// takes a setting only signed for it, and otherwise keeps the last one it took (review
// R-07). A hub without a move key cannot check a setting, so it uses the defaults.
func (h *Hub) refreshConfig(ctx context.Context, key, stateKey string) error {
	if !h.Approved() {
		return h.Store.SetState(stateKey, json.RawMessage(`{}`))
	}
	kind := "facility-hub-config/" + key
	var raw json.RawMessage
	err := h.Central.JSON(ctx, "GET", "/api/dataStore/"+kind, nil, h.HubAuth(), &raw)
	switch {
	case central.Status(err) == http.StatusNotFound:
		h.NoteUnsigned(kind, false)
		return h.Store.SetState(stateKey, json.RawMessage(`{}`))
	case err != nil:
		return err
	case h.Signed(kind, raw):
		return h.Store.SetState(stateKey, raw)
	case emptyObject(raw):
		h.NoteUnsigned(kind, false) // not set: nothing ignored worth showing
	}
	return nil
}

// configState decodes the setting refreshConfig stored under stateKey into v; v keeps
// its zero value when there is none.
func (h *Hub) configState(stateKey string, v any) {
	if b, err := h.Store.State(stateKey); err == nil && b != nil {
		json.Unmarshal(b, v)
	}
}

// WritableByAll reports whether central lets any user write the dataStore entry at
// path: its sharing's public access, as 2.42 shows it in the entry's metaData.
func (h *Hub) WritableByAll(ctx context.Context, path string) (bool, error) {
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
		o, err := h.WritableByAll(ctx, path)
		if central.IsNetwork(err) {
			return
		}
		// Shared again too while central has dropped a group from it: once a superuser
		// gives the hub owners read access to the group, sharing it again clears this.
		if err == nil && path == RegistryPath(h.HubID()) && (o || h.Store.Meta(MetaRegistryDropped) != "") {
			o = !h.ProtectRegistry(ctx, h.HubAuth()) && o
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
// sharing. Set-up needs the hub owners group named (OwnersNamed); an entry read
// without one leaves the sharing as it is. It reports whether the entry is shared so.
func (h *Hub) ProtectRegistry(ctx context.Context, auth central.Auth) bool {
	acc, err := h.ReadAccounts(ctx, auth)
	if err != nil || acc.Owners == "" {
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
	h.noteDroppedGroups(ctx, auth, acc.Owners, acc.Group, acc.Managers)
	return true
}

// MetaRegistryDropped lists the groups central left out of the registry entry's
// sharing, as last shared.
const MetaRegistryDropped = "registry_dropped"

// noteDroppedGroups reads the registry entry's sharing back: DHIS2 (2.42, 2.43) answers
// 200 to a sharing that names a group the sender cannot see, and drops the group. Without
// the hub accounts group the hub's own account could not write its entry.
func (h *Hub) noteDroppedGroups(ctx context.Context, auth central.Auth, groups ...string) {
	var meta struct {
		Sharing struct {
			UserGroups map[string]any `json:"userGroups"`
		} `json:"sharing"`
	}
	if err := h.Central.JSON(ctx, "GET", RegistryPath(h.HubID())+"/metaData", nil, auth, &meta); err != nil {
		return
	}
	var dropped []string
	for _, g := range groups {
		if _, ok := meta.Sharing.UserGroups[g]; g != "" && !ok {
			dropped = append(dropped, g)
		}
	}
	if len(dropped) > 0 {
		slog.Warn("central dropped groups from this hub's registry sharing: the account that set the hub up cannot see them. A superuser must save the groups in the Facility Hub app's Settings", "groups", dropped)
	}
	h.Store.SetMeta(MetaRegistryDropped, strings.Join(dropped, " "))
}

// RegistryDropped is what noteDroppedGroups last found.
func (h *Hub) RegistryDropped() []string {
	return strings.Fields(h.Store.Meta(MetaRegistryDropped))
}

// Unprotected is what CheckProtection last found.
func (h *Hub) Unprotected() []string {
	return strings.Fields(h.Store.Meta(MetaUnprotected))
}
