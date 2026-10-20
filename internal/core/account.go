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
)

// The hub's own account (docs/DEPLOYMENT.md, "The hub's own account"): at set-up the
// hub asks for one in its registry entry and shows a code made from its key on its
// admin page; the national team approves it in the Facility Hub app with that code,
// which hands the account over encrypted to the hub's key; the hub takes it up here,
// at a catalog refresh, and at once replaces its password and token, so whoever saw
// them on the way is shut out. From then on its housekeeping depends on no staff
// member. The approval also gives the hub a move key, for the code that comes with a
// central address typed in after an unplanned move.

// MetaAccountRotate, when set, says the hub's own account needs a new password and
// token: it was just handed over.
const MetaAccountRotate = "account_rotate"

// AccountCode is the code a hub waiting for an account shows on its admin page, for
// the national team to approve it with; "" when it is not waiting.
func (h *Hub) AccountCode() string {
	rt := h.Runtime()
	if rt == nil || rt.BoxKey == nil || rt.TokenOwner() == h.HubID() {
		return ""
	}
	pub, err := hubaccount.PublicKey(rt.BoxKey)
	if err != nil {
		return ""
	}
	return hubaccount.AccountCode(pub, h.HubID(), h.Facility())
}

// refreshAccount asks for an account (publishing the hub's key) whenever the registry
// entry does not show the hub's own request or an account for it, takes up an account
// handed over, and finishes replacing the account's credentials.
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
	path := RegistryPath(h.HubID())
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
	askAgain := func(why string) {
		reg["boxKey"], reg["accountRequested"] = base64.StdEncoding.EncodeToString(pub), true
		delete(reg, "account")
		if err := h.Central.JSON(ctx, "PUT", path, reg, h.HubAuth(), nil); err != nil {
			slog.Warn("the hub's request for its own account not published", "err", err)
			return
		}
		slog.Info("the hub asks for its own account", "why", why, "code", hubaccount.AccountCode(pub, h.HubID(), h.Facility()))
	}
	acc, _ := reg["account"].(map[string]any)
	published, _ := base64.StdEncoding.DecodeString(fmt.Sprint(reg["boxKey"]))
	switch {
	case !bytes.Equal(published, pub):
		askAgain("the registry holds another key") // an entry someone changed, or none yet
		return
	case acc == nil:
		if reg["accountRequested"] != true {
			askAgain("no request in the registry")
		}
		return
	}
	box, err := base64.StdEncoding.DecodeString(fmt.Sprint(acc["box"]))
	var plain []byte
	if err == nil {
		plain, err = hubaccount.Open(rt.BoxKey, box, h.HubID())
	}
	var creds hubaccount.Credentials
	if err != nil || json.Unmarshal(plain, &creds) != nil {
		askAgain("the account handed over does not open")
		return
	}
	if err := h.adoptAccount(ctx, creds); errors.Is(err, errNotFit) {
		askAgain(err.Error()) // the national team sees it waiting, and approves again
	} else if err != nil {
		slog.Warn("the hub's own account not taken up", "err", err)
	}
}

// adoptAccount swaps the hub's token for one of its own account, after checking the
// account is the hub's: named for it, assigned to its facility only (capture, data
// view and search), with no authorities, and able to do the housekeeping. The old
// token is deleted, and the credentials are replaced and leave the registry.
// errNotFit: the account handed over is not one the hub may use; it asks again.
var errNotFit = errors.New("the account handed over is not fit for this hub")

func (h *Hub) adoptAccount(ctx context.Context, creds hubaccount.Credentials) error {
	h.tokenMu.Lock()
	if h.Runtime().TokenOwner() == h.HubID() { // taken up meanwhile
		h.tokenMu.Unlock()
		return nil
	}
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
		return fmt.Errorf("%w: it is named %q", errNotFit, creds.Username)
	}
	basic := central.Auth{Username: creds.Username, Password: creds.Password}
	me, err := h.FetchMe(ctx, basic)
	if central.Status(err) == http.StatusUnauthorized {
		return fmt.Errorf("%w: central refuses its password (disabled, or approved again since)", errNotFit)
	}
	if err != nil {
		return err
	}
	if err := h.ownAccount(me); err != nil {
		return fmt.Errorf("%w: %v", errNotFit, err)
	}
	exp := time.Now().Add(h.TokenLife())
	uid, key, err := h.CreatePAT(ctx, basic, exp)
	if err != nil {
		return fmt.Errorf("creating a token: %w", err)
	}
	newAuth := central.Auth{PAT: key}
	// The credentials stay in the entry until the hub has saved them: if this answer is
	// lost, the next refresh takes them up again (rotateAccount removes them).
	err = h.EditRegistry(ctx, newAuth, func(reg map[string]any) bool {
		reg["hubTokenOwner"] = h.HubID()
		return true
	})
	if err != nil {
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(uid), nil, newAuth, nil)
		return fmt.Errorf("the account cannot write the hub's records: %w", err)
	}
	rt := h.Runtime()
	oldUID, oldKey := rt.HubTokenUID, rt.HubToken
	h.Store.SetMeta(MetaAccountRotate, "taken up")
	if err := h.UpdateRuntime(func(rt *Runtime) {
		rt.HubToken, rt.HubTokenUID, rt.HubTokenExp = key, uid, exp.UTC().Format(time.RFC3339)
		rt.HubTokenOwner, rt.HubPassword, rt.HubPasswordNext = h.HubID(), creds.Password, ""
		rt.MoveKey, rt.BoxKey = creds.MoveKey, nil
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

// rotateAccount gives the hub's own account a new password and a new token, and
// deletes the account's other tokens. Each is saved as soon as central has it, so a
// failure half-way loses nothing; the next refresh finishes.
func (h *Hub) rotateAccount(ctx context.Context) error {
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	rt := h.Runtime()
	if rt == nil || rt.TokenOwner() != h.HubID() || rt.HubPassword == "" {
		return nil
	}
	if next := rt.HubPasswordNext; next != "" {
		// An earlier change whose answer was lost: did central take it?
		_, err := h.FetchMe(ctx, central.Auth{Username: h.HubID(), Password: next})
		if err != nil && central.Status(err) != http.StatusUnauthorized {
			return err // no answer either way: ask again next time
		}
		if err := h.UpdateRuntime(func(rt *Runtime) {
			if err == nil {
				rt.HubPassword = next
			}
			rt.HubPasswordNext = ""
		}); err != nil {
			return err
		}
		rt = h.Runtime()
	}
	pw := hubaccount.NewPassword()
	if err := h.UpdateRuntime(func(rt *Runtime) { rt.HubPasswordNext = pw }); err != nil {
		return err
	}
	err := h.Central.JSON(ctx, "PUT", "/api/me/changePassword", map[string]string{"oldPassword": rt.HubPassword, "newPassword": pw}, h.HubAuth(), nil)
	if err != nil {
		return fmt.Errorf("changing the password: %w", err)
	}
	if err := h.UpdateRuntime(func(rt *Runtime) { rt.HubPassword, rt.HubPasswordNext = pw, "" }); err != nil {
		return err
	}
	exp := time.Now().Add(h.TokenLife())
	uid, key, err := h.CreatePAT(ctx, central.Auth{Username: h.HubID(), Password: pw}, exp)
	if err != nil {
		return fmt.Errorf("creating a token: %w", err)
	}
	if err := h.UpdateRuntime(func(rt *Runtime) {
		rt.HubToken, rt.HubTokenUID, rt.HubTokenExp = key, uid, exp.UTC().Format(time.RFC3339)
	}); err != nil {
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
	// The handed-over credentials leave the registry.
	if err := h.EditRegistry(ctx, h.HubAuth(), func(reg map[string]any) bool {
		if reg["account"] == nil && reg["accountRequested"] == nil {
			return false
		}
		delete(reg, "account")
		delete(reg, "accountRequested")
		return true
	}); err != nil {
		return fmt.Errorf("removing the handed-over account from the registry: %w", err)
	}
	h.Store.SetMeta(MetaAccountRotate, "")
	slog.Info("the hub's own account has a new password and token")
	return nil
}

// ErrNoMoveKey: a typed-in central move needs the move key an approval brings.
var ErrNoMoveKey = errors.New("only a hub the national team approved can move to a typed-in address; ask them to approve this hub")

// ErrWrongCode: the code is not the one for this hub and this address.
var ErrWrongCode = errors.New("the code is not the one for this hub and this address: check both with the national team")

// TypedMove switches to a central address a staff member typed in after an unplanned
// move (docs/DEPLOYMENT.md, section 6), with the code the national team gave for this
// hub and that address (hubaccount.MoveCode). The hub checks the code itself and sends
// nothing for it; it moves only while its current address does not answer (the demo
// uplink switch does not count), and never when hub.yaml pins the address.
func (h *Hub) TypedMove(ctx context.Context, raw, code string) error {
	rt := h.Runtime()
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	switch {
	case rt == nil || rt.MoveKey == nil:
		return ErrNoMoveKey
	case h.Monitor.Reachable():
		return errors.New("central still answers at its address: no move needed")
	case h.SetupMode() == SetupPinned:
		return errors.New("this hub's address is fixed in its configuration")
	case hubaccount.CheckAddress(raw) != nil:
		return fmt.Errorf("enter central's new address exactly as the national team gave it: %v", hubaccount.CheckAddress(raw))
	case raw == h.Central.BaseURL() || !h.movable(raw):
		return errors.New("enter central's new https:// address")
	case subtle.ConstantTimeCompare([]byte(hubaccount.NormalizeCode(hubaccount.MoveCode(rt.MoveKey, raw))),
		[]byte(hubaccount.NormalizeCode(code))) != 1:
		return ErrWrongCode
	}
	old := h.Central.BaseURL()
	h.Store.SetMeta(MetaCentralURL, raw)
	h.Store.SetMeta(MetaCentralNext, "")
	h.Central.SetBaseURL(raw)
	h.Monitor.Probe(ctx, raw)
	slog.Warn("central moved (typed in): the hub now uses the new address", "from", old, "to", raw)
	return nil
}
