package core

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/hubaccount"
)

// Instructions in central's dataStore count only with a code made with this hub's move
// key (docs/design/2026-11-11-signed-instructions.md, review R-07): sharing
// cannot tell the Facility Hub app's writes from those of any DHIS2 user who created
// the entry first. A hub without a move key (not approved) cannot check codes; each
// caller says what it does then.

// MetaUnsigned lists the entries this hub ignored at their last read, for its admin
// page and heartbeat: they carry no valid code for it, or an older one than it took.
const MetaUnsigned = "unsigned"

// metaAccepted + kind: the issued time of the last instruction of that kind taken.
const metaAccepted = "instruction_accepted:"

var unsignedMu sync.Mutex // MetaUnsigned is read, changed and written

// Approved reports whether this hub holds a move key, which signed instructions need.
func (h *Hub) Approved() bool {
	rt := h.Runtime()
	return rt != nil && len(rt.MoveKey) > 0
}

// Signed reports whether obj, a JSON object read from the entry kind
// ("<namespace>/<key>"), carries a valid code for this hub, issued no earlier than the
// last object of that kind it took (equal: the same entry read again; earlier: a
// replay). It then records obj as taken; otherwise it lists the entry as ignored.
func (h *Hub) Signed(kind string, obj json.RawMessage) bool {
	ok := h.signed(kind, obj)
	h.NoteUnsigned(kind, !ok)
	return ok
}

func (h *Hub) signed(kind string, obj json.RawMessage) bool {
	rt := h.Runtime()
	if rt == nil || len(rt.MoveKey) == 0 {
		return false
	}
	var s struct {
		Issued string            `json:"issued"`
		Codes  map[string]string `json:"codes"`
	}
	if json.Unmarshal(obj, &s) != nil || s.Codes[h.HubID()] == "" {
		return false
	}
	issued, err := time.Parse(time.RFC3339Nano, s.Issued)
	if err != nil {
		return false
	}
	want, err := hubaccount.InstructionCode(rt.MoveKey, h.HubID(), kind, obj)
	if err != nil || !hmac.Equal([]byte(want), []byte(s.Codes[h.HubID()])) {
		slog.Warn("an instruction on central has no valid code for this hub: ignored", "entry", kind)
		return false
	}
	if last, err := time.Parse(time.RFC3339Nano, h.Store.Meta(metaAccepted+kind)); err == nil && issued.Before(last) {
		slog.Warn("an instruction on central is older than the last one taken: ignored", "entry", kind, "issued", s.Issued)
		return false
	}
	h.Store.SetMeta(metaAccepted+kind, s.Issued)
	return true
}

// NoteUnsigned lists kind as ignored, or no longer. Only an approved hub lists
// anything: one without a move key ignores these entries by design.
func (h *Hub) NoteUnsigned(kind string, ignored bool) {
	unsignedMu.Lock()
	defer unsignedMu.Unlock()
	list := strings.Fields(h.Store.Meta(MetaUnsigned))
	has := slices.Contains(list, kind)
	switch {
	case ignored && !has && h.Approved():
		list = append(list, kind)
	case !ignored && has:
		list = slices.DeleteFunc(list, func(k string) bool { return k == kind })
	default:
		return
	}
	h.Store.SetMeta(MetaUnsigned, strings.Join(list, " "))
}

// Unsigned is the entries this hub ignored at their last read (NoteUnsigned).
func (h *Hub) Unsigned() []string { return strings.Fields(h.Store.Meta(MetaUnsigned)) }

// emptyObject reports whether raw is an object with nothing in it but a code: the
// value the hubs read as "not set".
func emptyObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	delete(m, "issued")
	delete(m, "codes")
	return len(m) == 0
}

// Accounts is facility-hub-admin/accounts: the groups and role the Facility Hub app
// names (docs/FACILITY-HUB-APP.md).
type Accounts struct {
	Group    string `json:"group"`
	Role     string `json:"role"`
	Managers string `json:"managers"`
	Owners   string `json:"owners"`
}

const accountsKey = "accounts_signed" // the last signed accounts entry taken

// ReadAccounts reads facility-hub-admin/accounts with auth. An approved hub takes a
// signed entry, and otherwise keeps to the last signed one it took. A hub that has
// never taken one, as any hub before its approval, uses the entry as read: set-up
// needs the hub owners group before any approval (the spec's remaining risks).
func (h *Hub) ReadAccounts(ctx context.Context, auth central.Auth) (Accounts, error) {
	const kind = "facility-hub-admin/accounts"
	var raw json.RawMessage
	var acc Accounts
	if err := h.Central.JSON(ctx, "GET", "/api/dataStore/"+kind, nil, auth, &raw); err != nil {
		return acc, err
	}
	json.Unmarshal(raw, &acc)
	if h.Approved() && !emptyObject(raw) && h.Signed(kind, raw) {
		h.Store.SetState(accountsKey, raw)
		return acc, nil
	}
	if b, err := h.Store.State(accountsKey); err == nil && b != nil {
		var last Accounts
		json.Unmarshal(b, &last)
		return last, nil
	}
	return acc, nil
}

// takeSignedStatus acts on the signed status in this approved hub's registry entry
// (review R-23): "revoked" revokes its tokens, and a newer other status undoes it.
// status is the entry's plain status, which counts for nothing here: any user who
// could write the entry could write it.
func (h *Hub) takeSignedStatus(ctx context.Context, status string, signed json.RawMessage) {
	kind := "facility-hub-registry/" + h.HubID()
	var s struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(signed, &s) != nil || s.Status == "" {
		h.NoteUnsigned(kind, status == "revoked")
		return
	}
	if !h.Signed(kind, signed) {
		return
	}
	switch {
	case s.Status == "revoked" && !h.Revoked():
		slog.Warn("the Facility Hub app revoked this hub's tokens")
		h.SetRevoked()
		h.DropTokens(ctx)
	case s.Status != "revoked" && h.Revoked():
		slog.Info("the Facility Hub app made this hub active again: making tokens again")
		retired := ""
		if status == "retired" {
			retired = "1"
		}
		h.Store.SetMeta(MetaRetired, retired)
	}
}

// TakeRegistryStatus reads the revocation from this hub's registry entry, as read
// with any credentials. Only an approved hub uses it; it reports whether it did.
func (h *Hub) TakeRegistryStatus(ctx context.Context, reg json.RawMessage) bool {
	if !h.Approved() {
		return false
	}
	var r struct {
		Status       string          `json:"status"`
		SignedStatus json.RawMessage `json:"signedStatus"`
	}
	json.Unmarshal(reg, &r)
	h.takeSignedStatus(ctx, r.Status, r.SignedStatus)
	return true
}
