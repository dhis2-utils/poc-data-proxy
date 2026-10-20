package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

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
