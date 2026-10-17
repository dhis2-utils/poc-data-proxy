package core

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/hubaccount"
)

// The hub's own account (docs/DEPLOYMENT.md, "The hub's own account"): at set-up the
// hub asks for one in its registry entry; "hub approve", run by someone allowed to
// create users, creates it and hands it over encrypted to the hub's box key; the hub
// takes it up here, at a catalog refresh. From then on its housekeeping depends on
// no staff member, and its token can be sent, for the typed-in check of a central
// move, without exposing anyone's data access.

// refreshAccount publishes the box key of a hub set up before accounts existed, and
// takes up an account handed over.
func (h *Hub) refreshAccount(ctx context.Context) {
	rt := h.Runtime()
	if rt == nil || rt.TokenOwner() == h.HubID() {
		return
	}
	path := "/api/dataStore/facility-hub-registry/" + url.PathEscape(h.HubID())
	var reg map[string]any
	if err := h.Central.JSON(ctx, "GET", path, nil, h.HubAuth(), &reg); err != nil {
		return
	}
	if rt.BoxKey == nil {
		pub, priv, err := hubaccount.NewBoxKey()
		if err != nil {
			return
		}
		if err := h.UpdateRuntime(func(rt *Runtime) { rt.BoxKey = priv }); err != nil {
			return
		}
		reg["boxKey"], reg["accountRequested"] = base64.StdEncoding.EncodeToString(pub), true
		h.Central.JSON(ctx, "PUT", path, reg, h.HubAuth(), nil)
		return
	}
	acc, _ := reg["account"].(map[string]any)
	box, err := base64.StdEncoding.DecodeString(fmt.Sprint(acc["box"]))
	if acc == nil || err != nil {
		return
	}
	plain, err := hubaccount.Open(rt.BoxKey, box, h.HubID())
	var creds hubaccount.Credentials
	if err != nil || json.Unmarshal(plain, &creds) != nil {
		slog.Warn("the account handed to this hub does not open", "err", err)
		return
	}
	if err := h.adoptAccount(ctx, creds); err != nil {
		slog.Warn("the hub's own account not taken up", "err", err)
	}
}

// adoptAccount swaps the hub's token for one of its own account, after checking the
// account is the hub's (named for it, assigned to its facility only) and can do the
// housekeeping; the old token is deleted and the credentials leave the registry.
func (h *Hub) adoptAccount(ctx context.Context, creds hubaccount.Credentials) error {
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	if creds.Username != h.HubID() {
		return fmt.Errorf("an account named %q is not this hub's", creds.Username)
	}
	basic := central.Auth{Username: creds.Username, Password: creds.Password}
	me, err := h.FetchMe(ctx, basic)
	if err != nil {
		return err
	}
	if me.Username != h.HubID() || len(me.OrganisationUnits) != 1 || me.OrganisationUnits[0].ID != h.Facility() {
		return errors.New("the account is not this hub's, assigned to its facility only")
	}
	exp := time.Now().Add(time.Duration(h.Cfg.PATExpiryDays) * 24 * time.Hour)
	uid, key, err := h.CreatePAT(ctx, basic, exp)
	if err != nil {
		return fmt.Errorf("creating a token: %w", err)
	}
	newAuth := central.Auth{PAT: key}
	path := "/api/dataStore/facility-hub-registry/" + url.PathEscape(h.HubID())
	var reg map[string]any
	err = h.Central.JSON(ctx, "GET", path, nil, newAuth, &reg)
	if err == nil {
		delete(reg, "account")
		delete(reg, "accountRequested")
		reg["hubTokenOwner"] = h.HubID()
		err = h.Central.JSON(ctx, "PUT", path, reg, newAuth, nil)
	}
	if err != nil {
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(uid), nil, newAuth, nil)
		return fmt.Errorf("the account cannot write the hub's records: %w", err)
	}
	rt := h.Runtime()
	oldUID, oldKey := rt.HubTokenUID, rt.HubToken
	if err := h.UpdateRuntime(func(rt *Runtime) {
		rt.HubToken, rt.HubTokenUID, rt.HubTokenExp = key, uid, exp.UTC().Format(time.RFC3339)
		rt.HubTokenOwner, rt.HubPassword, rt.MoveSecret = h.HubID(), creds.Password, creds.MoveSecret
	}); err != nil {
		return err
	}
	if oldUID != "" && oldUID != uid {
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(oldUID), nil, central.Auth{PAT: oldKey}, nil)
	}
	slog.Info("the hub now uses its own account", "account", h.HubID())
	return nil
}

// ErrNoOwnAccount: a typed-in central move needs the hub's own account.
var ErrNoOwnAccount = errors.New("only a hub with its own account on central can move to a typed-in address; ask the national team to approve this hub")

// TypedMove switches to a central address a staff member typed in after an unplanned
// move (docs/DEPLOYMENT.md, section 6): only while the current address does not
// answer, only with the hub's own account, and only to a server that returns this
// hub's move secret (facility-hub-accounts/<hub ID>, readable by this account only).
// The hub's own token is all the check sends: it opens no one's data.
func (h *Hub) TypedMove(ctx context.Context, raw string) error {
	rt := h.Runtime()
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	switch {
	case rt == nil || rt.TokenOwner() != h.HubID() || rt.MoveSecret == "":
		return ErrNoOwnAccount
	case h.Online():
		return errors.New("central still answers at its address: no move needed")
	case h.SetupMode() == SetupPinned:
		return errors.New("this hub's address is fixed in its configuration")
	case raw == h.Central.BaseURL() || !h.movable(raw):
		return errors.New("enter central's new https:// address")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", raw+"/api/dataStore/facility-hub-accounts/"+url.PathEscape(h.HubID()), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	h.HubAuth().Apply(req.Header)
	resp, err := h.Central.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", raw, err)
	}
	defer resp.Body.Close()
	var got struct {
		MoveSecret string `json:"moveSecret"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&got) != nil ||
		subtle.ConstantTimeCompare([]byte(got.MoveSecret), []byte(rt.MoveSecret)) != 1 {
		return errors.New("that server is not this country's central DHIS2")
	}
	old := h.Central.BaseURL()
	h.Store.SetMeta(MetaCentralURL, raw)
	h.Store.SetMeta(MetaCentralNext, "")
	h.Central.SetBaseURL(raw)
	h.Monitor.Probe(ctx, raw)
	slog.Warn("central moved (typed in): the hub now uses the new address", "from", old, "to", raw)
	return nil
}
