package core

import (
	"bytes"
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
	"github.com/dhis2/facility-hub/internal/hubcrypto"
)

// The hub's own account (docs/DEPLOYMENT.md, "The hub's own account"): at set-up the
// hub asks for one in its registry entry; "hub approve", run by someone allowed to
// create users, creates it and hands it over encrypted to the hub's box key; the hub
// takes it up here, at a catalog refresh, and at once replaces its password, token
// and move secret, so whoever saw them on the way is shut out. From then on its
// housekeeping depends on no staff member, and its token can be sent, for the
// typed-in check of a central move, without exposing anyone's data access.

// MetaAccountRotate, when set, says the hub's own account needs a new password, token
// and move secret: they were just handed over, or the token went to a server that
// failed the typed-in check. Typed-in moves wait for it.
const MetaAccountRotate = "account_rotate"

// refreshAccount publishes the box key of a hub set up before accounts existed (or
// whose key is not the one in its registry entry), takes up an account handed over,
// and replaces the account's credentials when they need it.
func (h *Hub) refreshAccount(ctx context.Context) {
	rt := h.Runtime()
	if rt == nil {
		return
	}
	if rt.TokenOwner() == h.HubID() {
		if h.Store.Meta(MetaAccountRotate) != "" {
			if err := h.rotateAccount(ctx); err != nil {
				slog.Warn("the hub's own account: credentials not replaced yet", "err", err)
			}
		}
		return
	}
	path := "/api/dataStore/facility-hub-registry/" + url.PathEscape(h.HubID())
	var reg map[string]any
	if err := h.Central.JSON(ctx, "GET", path, nil, h.HubAuth(), &reg); err != nil {
		return
	}
	if rt.BoxKey == nil {
		_, priv, err := hubaccount.NewBoxKey()
		if err != nil {
			return
		}
		if err := h.UpdateRuntime(func(rt *Runtime) { rt.BoxKey = priv }); err != nil {
			return
		}
		rt = h.Runtime()
	}
	pub, err := hubaccount.PublicKey(rt.BoxKey)
	if err != nil {
		return
	}
	// The registry must hold this hub's key: an entry whose key someone replaced would
	// have the account handed to them.
	if published, _ := base64.StdEncoding.DecodeString(fmt.Sprint(reg["boxKey"])); !bytes.Equal(published, pub) {
		reg["boxKey"], reg["accountRequested"] = base64.StdEncoding.EncodeToString(pub), true
		delete(reg, "account")
		if err := h.Central.JSON(ctx, "PUT", path, reg, h.HubAuth(), nil); err != nil {
			slog.Warn("the hub's key for its own account not published", "err", err)
		}
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
// account is the hub's: named for it, assigned to its facility only (capture, data
// view and search), with no authorities, and able to do the housekeeping. The old
// token is deleted, the credentials leave the registry, and they are replaced.
func (h *Hub) adoptAccount(ctx context.Context, creds hubaccount.Credentials) error {
	h.tokenMu.Lock()
	if err := h.adoptLocked(ctx, creds); err != nil {
		h.tokenMu.Unlock()
		return err
	}
	h.tokenMu.Unlock()
	if err := h.rotateAccount(ctx); err != nil {
		slog.Warn("the hub's own account: credentials not replaced yet; retried at the next refresh", "err", err)
	}
	return nil
}

func (h *Hub) adoptLocked(ctx context.Context, creds hubaccount.Credentials) error {
	if creds.Username != h.HubID() {
		return fmt.Errorf("an account named %q is not this hub's", creds.Username)
	}
	basic := central.Auth{Username: creds.Username, Password: creds.Password}
	me, err := h.FetchMe(ctx, basic)
	if err != nil {
		return err
	}
	if err := h.ownAccount(me); err != nil {
		return err
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
	h.Store.SetMeta(MetaAccountRotate, "taken up")
	if err := h.UpdateRuntime(func(rt *Runtime) {
		rt.HubToken, rt.HubTokenUID, rt.HubTokenExp = key, uid, exp.UTC().Format(time.RFC3339)
		rt.HubTokenOwner, rt.HubPassword, rt.MoveSecret = h.HubID(), creds.Password, creds.MoveSecret
		rt.BoxKey = nil
	}); err != nil {
		return err
	}
	if oldUID != "" && oldUID != uid {
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(oldUID), nil, central.Auth{PAT: oldKey}, nil)
	}
	slog.Info("the hub now uses its own account", "account", h.HubID())
	return nil
}

// ownAccount checks an account is fit to be the hub's: it sees the hub's facility
// only, whatever central shares publicly with its users, and can do nothing beyond
// the hub's housekeeping.
func (h *Hub) ownAccount(me *Me) error {
	if me.Username != h.HubID() {
		return errors.New("the account is not this hub's")
	}
	facilityOnly := func(ous []idRef, mayBeEmpty bool) bool {
		return (mayBeEmpty && len(ous) == 0) || (len(ous) == 1 && ous[0].ID == h.Facility())
	}
	if !facilityOnly(me.OrganisationUnits, false) || !facilityOnly(me.DataViewOrganisationUnits, true) ||
		!facilityOnly(me.TeiSearchOrganisationUnits, true) {
		return errors.New("the account is not assigned to this hub's facility only")
	}
	if len(me.Authorities) > 0 {
		return fmt.Errorf("the account has authorities (%s): a hub's account must have none", strings.Join(me.Authorities, ", "))
	}
	return nil
}

// rotateAccount gives the hub's own account a new password, a new token and a new
// move secret, and deletes the account's other tokens. Each is saved as soon as
// central has it, so a failure half-way loses nothing; the next refresh finishes.
func (h *Hub) rotateAccount(ctx context.Context) error {
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	rt := h.Runtime()
	if rt == nil || rt.TokenOwner() != h.HubID() || rt.HubPassword == "" {
		return nil
	}
	pw := hubaccount.NewPassword()
	err := h.Central.JSON(ctx, "PUT", "/api/me/changePassword", map[string]string{"oldPassword": rt.HubPassword, "newPassword": pw}, h.HubAuth(), nil)
	if err != nil {
		return fmt.Errorf("changing the password: %w", err)
	}
	if err := h.UpdateRuntime(func(rt *Runtime) { rt.HubPassword = pw }); err != nil {
		return err
	}
	exp := time.Now().Add(time.Duration(h.Cfg.PATExpiryDays) * 24 * time.Hour)
	uid, key, err := h.CreatePAT(ctx, central.Auth{Username: h.HubID(), Password: pw}, exp)
	if err != nil {
		return fmt.Errorf("creating a token: %w", err)
	}
	if err := h.UpdateRuntime(func(rt *Runtime) {
		rt.HubToken, rt.HubTokenUID, rt.HubTokenExp = key, uid, exp.UTC().Format(time.RFC3339)
	}); err != nil {
		return err
	}
	secret := hubaccount.NewSecret()
	if err := h.Central.JSON(ctx, "PUT", "/api/dataStore/facility-hub-accounts/"+url.PathEscape(h.HubID()),
		map[string]string{"moveSecret": secret}, h.HubAuth(), nil); err != nil {
		return fmt.Errorf("replacing the move secret: %w", err)
	}
	if err := h.UpdateRuntime(func(rt *Runtime) { rt.MoveSecret = secret }); err != nil {
		return err
	}
	var toks struct {
		Tokens []struct {
			ID string `json:"id"`
		} `json:"apiToken"`
	}
	if err := h.Central.JSON(ctx, "GET", central.Q("/api/apiToken", "fields", "id", "paging", "false"), nil, h.HubAuth(), &toks); err != nil {
		return fmt.Errorf("listing the account's tokens: %w", err)
	}
	for _, t := range toks.Tokens {
		if t.ID != uid {
			if err := h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(t.ID), nil, h.HubAuth(), nil); err != nil {
				return fmt.Errorf("deleting the account's other tokens: %w", err)
			}
		}
	}
	h.Store.SetMeta(MetaAccountRotate, "")
	slog.Info("the hub's own account has a new password, token and move secret")
	return nil
}

// ErrNoOwnAccount: a typed-in central move needs the hub's own account.
var ErrNoOwnAccount = errors.New("only a hub with its own account on central can move to a typed-in address; ask the national team to approve this hub")

// CheckPassword checks a staff member's password against their key slot on the hub,
// without central.
func (h *Hub) CheckPassword(username, password string) bool {
	u, err := h.Store.UserByName(username)
	if err != nil || u == nil {
		return false
	}
	_, err = hubcrypto.UnwrapDK(u.WrappedDK, password, u.KEKSalt)
	return err == nil
}

// TypedMove switches to a central address a staff member typed in after an unplanned
// move (docs/DEPLOYMENT.md, section 6): only with the hub's own account, only while
// the current address has not answered the probes for central_move_after (the demo
// uplink switch does not count), only with the staff member's password, and only to a
// server that returns this hub's move secret (facility-hub-accounts/<hub ID>, readable
// by this account only). The check sends the hub's own token, which opens no one's
// data; a server that receives it and fails the check could read the move secret
// with it, so typed-in moves then wait until the hub has reached central again and
// replaced its credentials.
func (h *Hub) TypedMove(ctx context.Context, raw, username, password string) error {
	rt := h.Runtime()
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	switch {
	case rt == nil || rt.TokenOwner() != h.HubID() || rt.MoveSecret == "":
		return ErrNoOwnAccount
	case h.Store.Meta(MetaAccountRotate) != "":
		return errors.New("an earlier address failed the check, or the hub's own account is new: the hub accepts a typed-in address again once it has reached central at its old address")
	case h.Monitor.Reachable():
		return errors.New("central still answers at its address: no move needed")
	case !h.downLongEnough():
		return fmt.Errorf("central has not answered for %s yet: wait, it may be a short outage", h.Cfg.CentralMoveAfter)
	case h.SetupMode() == SetupPinned:
		return errors.New("this hub's address is fixed in its configuration")
	case raw == h.Central.BaseURL() || !h.movable(raw):
		return errors.New("enter central's new https:// address")
	case !h.CheckPassword(username, password):
		return ErrWrongPassword
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
		// Nothing answered (a typo, most likely), or not with a trusted certificate:
		// the token did not go out.
		return fmt.Errorf("cannot reach %s: %w", raw, err)
	}
	defer resp.Body.Close()
	var got struct {
		MoveSecret string `json:"moveSecret"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&got) != nil ||
		subtle.ConstantTimeCompare([]byte(got.MoveSecret), []byte(rt.MoveSecret)) != 1 {
		h.Store.SetMeta(MetaAccountRotate, "typed-in check failed: "+raw)
		slog.Warn("typed-in central address failed the check; typed-in moves wait until central is reached again", "address", raw, "by", username)
		return errors.New("that server is not this country's central DHIS2; the hub accepts no other typed-in address until it has reached central again")
	}
	old := h.Central.BaseURL()
	h.Store.SetMeta(MetaCentralURL, raw)
	h.Store.SetMeta(MetaCentralNext, "")
	h.Central.SetBaseURL(raw)
	h.Monitor.Probe(ctx, raw)
	slog.Warn("central moved (typed in): the hub now uses the new address", "from", old, "to", raw, "by", username)
	return nil
}
