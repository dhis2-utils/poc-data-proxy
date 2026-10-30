package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/compat"
	"github.com/dhis2/facility-hub/internal/store"
)

type idRef struct {
	ID string `json:"id"`
}

func ids(refs []idRef) []string {
	out := []string{}
	for _, r := range refs {
		out = append(out, r.ID)
	}
	return out
}

type dataAccess struct {
	Access struct {
		Data access.Perm `json:"data"`
	} `json:"access"`
}

// Me is the subset of /api/me the hub uses.
type Me struct {
	ID                         string   `json:"id"`
	Username                   string   `json:"username"`
	OrganisationUnits          []idRef  `json:"organisationUnits"`
	TeiSearchOrganisationUnits []idRef  `json:"teiSearchOrganisationUnits"`
	DataViewOrganisationUnits  []idRef  `json:"dataViewOrganisationUnits"`
	Authorities                []string `json:"authorities"`
	UserGroups                 []idRef  `json:"userGroups"`
}

// Belongs reports whether the user may register on the hub: the facility (with this
// path) is in their capture scope.
func (m *Me) Belongs(facilityPath string) bool {
	// The facility, or an org unit above it: capture scope includes what is below.
	for _, o := range m.OrganisationUnits {
		if strings.Contains(facilityPath+"/", "/"+o.ID+"/") {
			return true
		}
	}
	return false
}

const meFields = "id,username,organisationUnits[id],teiSearchOrganisationUnits[id],dataViewOrganisationUnits[id],authorities,userGroups[id]"

func (h *Hub) FetchMe(ctx context.Context, auth central.Auth) (*Me, error) {
	me := &Me{}
	err := h.Central.JSON(ctx, "GET", central.Q("/api/me", "fields", meFields), nil, auth, me)
	return me, err
}

// FetchAccess builds a user's access list (spec 8.1).
func (h *Hub) FetchAccess(ctx context.Context, auth central.Auth) (*access.List, error) {
	me, err := h.FetchMe(ctx, auth)
	if err != nil {
		return nil, err
	}
	l := &access.List{UserUID: me.ID, Username: me.Username, OrgUnits: ids(me.OrganisationUnits),
		SearchOrgUnits: ids(me.TeiSearchOrganisationUnits), Authorities: me.Authorities, UserGroups: ids(me.UserGroups),
		Programs: map[string]access.Perm{}, Stages: map[string]access.Perm{}, TETs: map[string]access.Perm{}}
	progs := h.Catalog().ProgramIDs()
	if len(progs) == 0 {
		return l, nil
	}
	var resp struct {
		Programs []struct {
			ID string `json:"id"`
			dataAccess
			TrackedEntityType struct {
				ID string `json:"id"`
				dataAccess
			} `json:"trackedEntityType"`
			ProgramStages []struct {
				ID string `json:"id"`
				dataAccess
			} `json:"programStages"`
		} `json:"programs"`
	}
	path := central.Q("/api/programs", "filter", "id:in:["+strings.Join(progs, ",")+"]",
		"fields", "id,access[data[read,write]],trackedEntityType[id,access[data[read,write]]],programStages[id,access[data[read,write]]]",
		"paging", "false")
	if err := h.Central.JSON(ctx, "GET", path, nil, auth, &resp); err != nil {
		return nil, err
	}
	for _, p := range resp.Programs {
		l.Programs[p.ID] = p.Access.Data
		if p.TrackedEntityType.ID != "" { // event programs have none
			l.TETs[p.TrackedEntityType.ID] = p.TrackedEntityType.Access.Data
		}
		for _, s := range p.ProgramStages {
			l.Stages[s.ID] = s.Access.Data
		}
	}
	// Relationship types decide which relationships central shows: part of what makes
	// two users' listings the same (access.List.GroupKey).
	var rts struct {
		RelationshipTypes []struct {
			ID string `json:"id"`
			dataAccess
		} `json:"relationshipTypes"`
	}
	if err := h.Central.JSON(ctx, "GET", central.Q("/api/relationshipTypes", "fields", "id,access[data[read,write]]",
		"paging", "false"), nil, auth, &rts); err != nil {
		if central.IsNetwork(err) {
			return nil, err
		}
		l.Ungroupable = true
	} else {
		l.RelationshipTypes = map[string]access.Perm{}
		for _, rt := range rts.RelationshipTypes {
			l.RelationshipTypes[rt.ID] = rt.Access.Data
		}
	}
	if combos := h.Catalog().AttributeCombos(); len(combos) > 0 {
		var cc struct {
			Combos []struct {
				Categories []struct {
					Options []struct {
						ID string `json:"id"`
						dataAccess
					} `json:"categoryOptions"`
				} `json:"categories"`
			} `json:"categoryCombos"`
		}
		if err := h.Central.JSON(ctx, "GET", central.Q("/api/categoryCombos", "filter", "id:in:["+strings.Join(combos, ",")+"]",
			"fields", "id,categories[categoryOptions[id,access[data[read,write]]]]", "paging", "false"), nil, auth, &cc); err != nil {
			if central.IsNetwork(err) {
				return nil, err
			}
			l.Ungroupable = true // unknown: no offline writes with an option combination, pulled alone
		} else {
			l.CategoryOptions, l.ReadableOptions = map[string]bool{}, map[string]bool{}
			for _, c := range cc.Combos {
				for _, cat := range c.Categories {
					for _, o := range cat.Options {
						if o.Access.Data.Write {
							l.CategoryOptions[o.ID] = true
						}
						if o.Access.Data.Read {
							l.ReadableOptions[o.ID] = true
						}
					}
				}
			}
		}
	}
	return l, nil
}

// RefreshAccess fetches and stores a user's access list.
func (h *Hub) RefreshAccess(ctx context.Context, userID int64) error {
	auth, err := h.UpstreamAuth(userID)
	if err != nil {
		return err
	}
	return h.RefreshAccessWith(ctx, userID, auth)
}

// RefreshAccessWith is RefreshAccess with a given credential: at login, the one the
// user just logged in with, which works even when the hub could not store a token.
func (h *Hub) RefreshAccessWith(ctx context.Context, userID int64, auth central.Auth) error {
	l, err := h.FetchAccess(ctx, auth)
	if err != nil {
		if central.Status(err) == http.StatusUnauthorized {
			h.Store.SetNeedsLogin(userID, true)
		}
		return err
	}
	if err := h.RefreshAggAccess(ctx, userID, auth, l.Authorities); err != nil {
		if central.IsNetwork(err) {
			return err
		}
		slog.Warn("aggregate access refresh", "user", l.Username, "err", err)
	}
	return h.SetAccess(userID, l)
}

// EnsurePAT creates a PAT for the user unless a valid one is stored (spec 7.1 step 2).
// with is the credential to create it with (the user's fresh central session).
// If central refuses, the encrypted password is stored instead (spec 7.1 fallback).
// One user's logins run it one at a time: concurrent logins within the same second
// would each create a token on central, and the hub keeps only the last, leaving the
// others valid there unlisted in the registry (S1).
func (h *Hub) EnsurePAT(ctx context.Context, u *store.User, with central.Auth, password string) error {
	lk := h.userLock(u.Username)
	lk.Lock()
	defer lk.Unlock()
	if fresh, err := h.Store.UserByID(u.ID); err == nil && fresh != nil {
		u = fresh // another login of this user may have stored a PAT meanwhile
	}
	if h.Revoked() {
		return errors.New("this hub's tokens were revoked on central; reset it before use")
	}
	if u.HasPAT && !u.NeedsLogin {
		if exp, err := time.Parse(time.RFC3339, u.PATExpires); err == nil && !h.renewToken(exp) {
			return nil
		}
	}
	api := h.API()
	if api == nil {
		return fmt.Errorf("unsupported server version %q", h.ServerVersion())
	}
	exp := time.Now().Add(h.TokenLife())
	uid, key, err := h.CreatePAT(ctx, with, exp)
	if err != nil {
		if !h.Cfg.PasswordFallback {
			return fmt.Errorf("PAT creation failed; this user's offline changes cannot be sent: %w", err)
		}
		if storeErr := h.Store.SetPassword(u.ID, password); storeErr != nil {
			return storeErr
		}
		return fmt.Errorf("PAT creation failed, storing encrypted password instead: %w", err)
	}
	if u.PATUID != "" && u.PATUID != uid {
		// Best effort: revoke the token this one replaces.
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(u.PATUID), nil, with, nil)
	}
	h.Store.SetPassword(u.ID, "")
	return h.Store.SetPAT(u.ID, key, uid, exp.UTC().Format(time.RFC3339))
}

// RenewHubToken replaces the hub-level token when it expires within a week. Without
// this, catalog, issue reports, pool refills and the heartbeat all start failing
// pat_expiry_days after setup. A PAT may create a PAT on 2.42
// (testdata/2.42/apiToken_create_with_pat.json).
func (h *Hub) RenewHubToken(ctx context.Context) error {
	return h.renewHubToken(ctx, false)
}

// ReplaceHubToken makes a new hub token now: central refused the current one (401),
// deleted on central though the hub's own account still works (G9).
func (h *Hub) ReplaceHubToken(ctx context.Context) error {
	return h.renewHubToken(ctx, true)
}

func (h *Hub) renewHubToken(ctx context.Context, now bool) error {
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	rt := h.Runtime()
	if rt == nil || rt.HubToken == "" {
		return nil
	}
	exp, _ := time.Parse(time.RFC3339, rt.HubTokenExp) // unreadable: renew now
	if !now && !h.renewToken(exp) {
		return nil
	}
	newExp := time.Now().Add(h.TokenLife())
	uid, key, err := h.CreatePAT(ctx, h.HubAuth(), newExp)
	owner := rt.HubTokenOwner
	if central.Status(err) == http.StatusUnauthorized && rt.HubPassword != "" {
		// The hub's own account, whose token lapsed while offline: a new one with its
		// password.
		uid, key, err = h.CreatePAT(ctx, central.Auth{Username: h.HubID(), Password: rt.HubPassword}, newExp)
		if central.Status(err) == http.StatusUnauthorized && rt.HubPasswordNext != "" {
			uid, key, err = h.CreatePAT(ctx, central.Auth{Username: h.HubID(), Password: rt.HubPasswordNext}, newExp)
		}
		owner = h.HubID()
	}
	if err != nil {
		return fmt.Errorf("renewing hub token: %w", err)
	}
	oldUID := rt.HubTokenUID
	if err := h.UpdateRuntime(func(rt *Runtime) {
		rt.HubToken, rt.HubTokenUID, rt.HubTokenExp = key, uid, newExp.UTC().Format(time.RFC3339)
		rt.HubTokenOwner = owner
	}); err != nil {
		return err
	}
	if oldUID != "" && oldUID != uid {
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(oldUID), nil, h.HubAuth(), nil)
	}
	slog.Info("hub token renewed", "expires", newExp.UTC().Format(time.RFC3339))
	return nil
}

// CreatePAT creates a personal access token on central.
// A revoked hub makes none: the registry entry is read first, with the creating
// credentials, since the hub's own token may be gone with the revocation (bug review
// 2026-10-20).
func (h *Hub) CreatePAT(ctx context.Context, with central.Auth, exp time.Time) (uid, key string, err error) {
	if h.HubID() != "" && !h.Revoked() {
		var reg struct {
			Status string `json:"status"`
		}
		if h.Central.JSON(ctx, "GET", RegistryPath(h.HubID()), nil, with, &reg) == nil && reg.Status == "revoked" && h.revokedForReal(ctx) {
			h.SetRevoked()
			h.DropTokens(ctx)
		}
	}
	if h.Revoked() {
		return "", "", errors.New("this hub's tokens were revoked on central; reset it before use")
	}
	api := h.API()
	if api == nil {
		return "", "", fmt.Errorf("unsupported server version %q", h.ServerVersion())
	}
	resp, err := h.Central.Do(ctx, "POST", "/api/apiToken", mustJSON(api.PATRequest(exp.UnixMilli())), with, nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", "", &central.HTTPError{Status: resp.StatusCode, Body: b}
	}
	if uid, key, err = api.PATResponse(b); err == nil {
		h.shareWithManagers(ctx, central.Auth{PAT: key}, uid)
	}
	return uid, key, err
}

// RefreshServerVersion reads central's version (spec 6.2 step 5, refreshed on sync).
func (h *Hub) RefreshServerVersion(ctx context.Context, auth central.Auth) (string, error) {
	var info struct {
		Version  string `json:"version"`
		TimeZone string `json:"serverTimeZoneId"`
	}
	if err := h.Central.JSON(ctx, "GET", "/api/system/info", nil, auth, &info); err != nil {
		return "", err
	}
	if info.Version != "" {
		h.Store.SetMeta(MetaServerVersion, info.Version)
	}
	if info.TimeZone != "" && info.TimeZone != h.Store.Meta(MetaServerTimeZone) {
		h.Store.SetMeta(MetaServerTimeZone, info.TimeZone)
	}
	return info.Version, nil
}

// RefreshCatalog rebuilds the facility's programs as the union of what the hub token
// and each registered user can see. The setup user, often a district officer, may
// lack metadata access to programs facility staff work in; a catalog from the hub
// token alone would never cache those. If any user's view cannot be read, programs
// already known are kept, so a transient failure does not drop a program.
func (h *Hub) RefreshCatalog(ctx context.Context) error {
	facility := h.Facility()
	cat, err := h.FetchCatalog(ctx, h.HubAuth(), facility)
	if central.IsNetwork(err) {
		return err
	}
	h.refreshCentralMove(ctx)
	h.refreshAccount(ctx)
	if perr := h.refreshFacilityPath(ctx); central.IsNetwork(perr) {
		return perr
	}
	complete := err == nil
	if cat == nil {
		cat = &access.Catalog{Programs: map[string]*access.ProgramMeta{}, TETAttributes: map[string][]access.AttrMeta{}}
	}
	users, uerr := h.Store.Users()
	if uerr != nil {
		return uerr
	}
	for _, u := range users {
		if u.CannotPush() {
			continue
		}
		auth, aerr := h.UpstreamAuth(u.ID)
		if aerr != nil {
			complete = false
			continue
		}
		uc, ferr := h.FetchCatalog(ctx, auth, facility)
		if central.IsNetwork(ferr) {
			return ferr
		}
		if ferr != nil {
			complete = false
			continue
		}
		cat.Merge(uc)
	}
	if !complete {
		cat.Merge(h.Catalog())
	}
	if uerr := h.UpdateRuntime(func(rt *Runtime) { rt.Catalog = cat }); uerr != nil {
		return uerr
	}
	// The country's settings from the Facility Hub app (facility-hub-config).
	for key, state := range map[string]string{"aggregate": aggRetentionKey, "retention": retentionKey, "tokens": tokenSettingsKey} {
		if cerr := h.refreshConfig(ctx, key, state); cerr != nil && central.IsNetwork(cerr) {
			return cerr
		}
	}
	if err != nil {
		return fmt.Errorf("catalog with hub token: %w", err)
	}
	return nil
}

// refreshFacilityPath keeps the facility's path as central has it: a facility moved
// to another district changes who is its staff (access.List.InFacility), and the
// access refresh after the catalog's applies it (A12).
func (h *Hub) refreshFacilityPath(ctx context.Context) error {
	facility := h.Facility()
	if facility == "" {
		return nil
	}
	var ou struct {
		Path string `json:"path"`
	}
	err := h.Central.JSON(ctx, "GET", central.Q("/api/organisationUnits/"+url.PathEscape(facility), "fields", "path"), nil, h.HubAuth(), &ou)
	if err != nil || !strings.HasSuffix(ou.Path, "/"+facility) || ou.Path == h.Runtime().FacilityPath {
		return err
	}
	slog.Info("the facility's path changed on central", "was", h.Runtime().FacilityPath, "now", ou.Path)
	return h.UpdateRuntime(func(rt *Runtime) { rt.FacilityPath = ou.Path })
}

// revokedForReal tells a real revocation from a forged one, once the registry entry
// says "revoked": any user can write the entry until its namespace is protected, but
// only a real revocation deletes the hub's token, so central refuses it (L3).
func (h *Hub) revokedForReal(ctx context.Context) bool {
	err := h.Central.JSON(ctx, "GET", "/api/me?fields=id", nil, h.HubAuth(), nil)
	if central.Status(err) == http.StatusUnauthorized {
		return true
	}
	slog.Warn("the registry marks this hub's tokens as revoked, but central still takes its token: ignored (protect facility-hub-registry, docs/DEPLOYMENT.md)", "err", err)
	return false
}

// AddUserPrograms merges the programs one user can see into the catalog (at login,
// so a new user's programs are cached without waiting for the next sync).
func (h *Hub) AddUserPrograms(ctx context.Context, auth central.Auth) {
	if h.Runtime() == nil {
		return
	}
	uc, err := h.FetchCatalog(ctx, auth, h.Facility())
	if err != nil {
		slog.Warn("login: user's programs", "err", err)
		return
	}
	cat := *h.Catalog()
	cat.Programs = maps.Clone(cat.Programs)
	cat.TETAttributes = maps.Clone(cat.TETAttributes)
	before := len(cat.Programs)
	cat.Merge(uc)
	if len(cat.Programs) > before {
		h.UpdateRuntime(func(rt *Runtime) { rt.Catalog = &cat })
	}
}

// FetchCatalog derives the cached programs: tracker programs assigned to the facility
// (spec 6.2 step 6).
func (h *Hub) FetchCatalog(ctx context.Context, auth central.Auth, facility string) (*access.Catalog, error) {
	type attr struct {
		TrackedEntityAttribute struct {
			ID        string `json:"id"`
			Generated bool   `json:"generated"`
			Unique    bool   `json:"unique"`
			Pattern   string `json:"pattern"`
		} `json:"trackedEntityAttribute"`
	}
	conv := func(as []attr) []access.AttrMeta {
		out := []access.AttrMeta{}
		for _, a := range as {
			t := a.TrackedEntityAttribute
			out = append(out, access.AttrMeta{ID: t.ID, Generated: t.Generated, Unique: t.Unique, Pattern: t.Pattern})
		}
		return out
	}
	var resp struct {
		Programs []struct {
			ID                string `json:"id"`
			Name              string `json:"name"`
			OnlyEnrollOnce    bool   `json:"onlyEnrollOnce"`
			TrackedEntityType struct {
				ID                          string `json:"id"`
				TrackedEntityTypeAttributes []attr `json:"trackedEntityTypeAttributes"`
			} `json:"trackedEntityType"`
			ProgramStages []struct {
				ID         string `json:"id"`
				SortOrder  int    `json:"sortOrder"`
				Repeatable bool   `json:"repeatable"`
			} `json:"programStages"`
			CategoryCombo struct {
				ID        string `json:"id"`
				IsDefault *bool  `json:"isDefault"`
			} `json:"categoryCombo"`
			EnrollmentCategoryCombo struct {
				ID        string `json:"id"`
				IsDefault *bool  `json:"isDefault"`
			} `json:"enrollmentCategoryCombo"` // 2.43
			ProgramTrackedEntityAttributes []attr `json:"programTrackedEntityAttributes"`
			ExpiryPeriodType               string `json:"expiryPeriodType"`
			ExpiryDays                     int    `json:"expiryDays"`
			CompleteEventsExpiryDays       int    `json:"completeEventsExpiryDays"`
		} `json:"programs"`
	}
	af := "trackedEntityAttribute[id,generated,unique,pattern]"
	path := central.Q("/api/programs", "filter", "programType:eq:WITH_REGISTRATION",
		"filter", "organisationUnits.id:eq:"+facility,
		"fields", programFields(h.API(), af),
		"paging", "false")
	if err := h.Central.JSON(ctx, "GET", path, nil, auth, &resp); err != nil {
		return nil, err
	}
	c := &access.Catalog{Programs: map[string]*access.ProgramMeta{}, TETAttributes: map[string][]access.AttrMeta{},
		TypeAttributes: map[string]bool{}}
	var types struct {
		TrackedEntityTypes []struct {
			Attributes []struct {
				TrackedEntityAttribute idRef `json:"trackedEntityAttribute"`
			} `json:"trackedEntityTypeAttributes"`
		} `json:"trackedEntityTypes"`
	}
	if err := h.Central.JSON(ctx, "GET", central.Q("/api/trackedEntityTypes", "paging", "false",
		"fields", "trackedEntityTypeAttributes[trackedEntityAttribute[id]]"), nil, auth, &types); err != nil {
		return nil, err
	}
	for _, t := range types.TrackedEntityTypes {
		for _, a := range t.Attributes {
			c.TypeAttributes[a.TrackedEntityAttribute.ID] = true
		}
	}
	for _, p := range resp.Programs {
		pm := &access.ProgramMeta{ID: p.ID, Name: p.Name, TET: p.TrackedEntityType.ID, Attributes: conv(p.ProgramTrackedEntityAttributes),
			OnlyEnrollOnce:           p.OnlyEnrollOnce,
			AttributeCombo:           p.CategoryCombo.IsDefault != nil && !*p.CategoryCombo.IsDefault,
			EnrollmentAttributeCombo: p.EnrollmentCategoryCombo.IsDefault != nil && !*p.EnrollmentCategoryCombo.IsDefault,
			ExpiryPeriodType:         p.ExpiryPeriodType, ExpiryDays: p.ExpiryDays, CompleteEventsExpiryDays: p.CompleteEventsExpiryDays}
		if pm.AttributeCombo {
			pm.CategoryCombo = p.CategoryCombo.ID
		}
		if pm.EnrollmentAttributeCombo {
			pm.EnrollmentCategoryCombo = p.EnrollmentCategoryCombo.ID
		}
		for _, s := range p.ProgramStages {
			pm.Stages = append(pm.Stages, s.ID)
			if !s.Repeatable {
				pm.NonRepeatable = append(pm.NonRepeatable, s.ID)
			}
		}
		c.Programs[p.ID] = pm
		c.TETAttributes[p.TrackedEntityType.ID] = conv(p.TrackedEntityType.TrackedEntityTypeAttributes)
	}
	// Event programs (WITHOUT_REGISTRATION): events without a person or enrollment.
	var evResp struct {
		Programs []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			CategoryCombo struct {
				ID        string `json:"id"`
				IsDefault *bool  `json:"isDefault"`
			} `json:"categoryCombo"`
			ProgramStages []struct {
				ID string `json:"id"`
			} `json:"programStages"`
			ExpiryPeriodType         string `json:"expiryPeriodType"`
			ExpiryDays               int    `json:"expiryDays"`
			CompleteEventsExpiryDays int    `json:"completeEventsExpiryDays"`
		} `json:"programs"`
	}
	if err := h.Central.JSON(ctx, "GET", central.Q("/api/programs", "filter", "programType:eq:WITHOUT_REGISTRATION",
		"filter", "organisationUnits.id:eq:"+facility,
		"fields", "id,name,categoryCombo[id,isDefault],programStages[id],expiryPeriodType,expiryDays,completeEventsExpiryDays",
		"paging", "false"), nil, auth, &evResp); err != nil {
		return nil, err
	}
	for _, p := range evResp.Programs {
		pm := &access.ProgramMeta{ID: p.ID, Name: p.Name, Event: true, Attributes: []access.AttrMeta{},
			AttributeCombo:           p.CategoryCombo.IsDefault != nil && !*p.CategoryCombo.IsDefault,
			ExpiryPeriodType:         p.ExpiryPeriodType, ExpiryDays: p.ExpiryDays, CompleteEventsExpiryDays: p.CompleteEventsExpiryDays}
		if pm.AttributeCombo {
			pm.CategoryCombo = p.CategoryCombo.ID
		}
		for _, s := range p.ProgramStages {
			pm.Stages = append(pm.Stages, s.ID)
		}
		c.Programs[p.ID] = pm
	}
	if combos := c.AttributeCombos(); len(combos) > 0 {
		var cocs struct {
			COCs []struct {
				ID            string  `json:"id"`
				CategoryCombo idRef   `json:"categoryCombo"`
				Options       []idRef `json:"categoryOptions"`
			} `json:"categoryOptionCombos"`
		}
		if err := h.Central.JSON(ctx, "GET", central.Q("/api/categoryOptionCombos", "filter",
			"categoryCombo.id:in:["+strings.Join(combos, ",")+"]", "fields", "id,categoryCombo[id],categoryOptions[id]",
			"paging", "false"), nil, auth, &cocs); err != nil {
			return nil, err
		}
		c.AOCs, c.ComboAOCs = map[string][]string{}, map[string][]string{}
		for _, coc := range cocs.COCs {
			for _, o := range coc.Options {
				c.AOCs[coc.ID] = append(c.AOCs[coc.ID], o.ID)
			}
			c.ComboAOCs[coc.CategoryCombo.ID] = append(c.ComboAOCs[coc.CategoryCombo.ID], coc.ID)
		}
	}
	c.Index()
	return c, nil
}

// programFields is the catalog's field selection for the server's version.
func programFields(api compat.API, af string) string {
	if api == nil {
		api = compat.For("2.42")
	}
	return api.ProgramFields(af)
}

// ErrNotHubOwner: the user's account cannot write the facility-hub namespaces on
// central, which countries share with a "hub owners" group (docs/DEPLOYMENT.md).
var ErrNotHubOwner = errors.New("your account cannot write the hub's records on central: ask the national team or the district to add you to the hub owners group")

// ErrWrongPassword: central refused the password. An account with two-factor login
// gets this too: central refuses its password alone.
var ErrWrongPassword = errors.New("wrong password (an account with two-factor login cannot take the hub over)")

// TokenOwner is the user whose token the hub uses for its housekeeping.
func (rt *Runtime) TokenOwner() string {
	if rt == nil {
		return ""
	}
	if rt.HubTokenOwner != "" {
		return rt.HubTokenOwner
	}
	return rt.SetupBy
}

// TakeOverHubToken moves the hub's housekeeping (heartbeat, issue reports, registry,
// program list, generated IDs, upgrade checks) to a token of this user, who must be
// assigned to the facility, and deletes the old token: for when the person who set
// the hub up leaves (docs/DEPLOYMENT.md, "The hub's own account").
func (h *Hub) TakeOverHubToken(ctx context.Context, username, password string) error {
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	rt := h.Runtime()
	if rt == nil {
		return errors.New("the hub is not set up")
	}
	with := central.Auth{Username: username, Password: password}
	me, err := h.FetchMe(ctx, with)
	if central.Status(err) == http.StatusUnauthorized {
		return ErrWrongPassword
	}
	if err != nil {
		return err
	}
	if !me.Belongs(h.FacilityPath()) {
		return fmt.Errorf("%s is not assigned to this facility", username)
	}
	exp := time.Now().Add(h.TokenLife())
	uid, key, err := h.CreatePAT(ctx, with, exp)
	if err != nil {
		return fmt.Errorf("creating a token: %w", err)
	}
	// The new token must do the housekeeping before the old one goes: countries protect
	// the facility-hub-* namespaces, and a token that cannot write them would leave the
	// hub silent. Writing the registry entry, with its new owner, is the test.
	newAuth := central.Auth{PAT: key}
	err = h.EditRegistry(ctx, newAuth, func(reg map[string]any) bool {
		reg["hubTokenOwner"] = me.Username
		return true
	})
	if err != nil {
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(uid), nil, newAuth, nil)
		if central.Status(err) == http.StatusForbidden {
			return ErrNotHubOwner
		}
		return fmt.Errorf("writing the hub's registry entry: %w", err)
	}
	oldUID, oldKey := rt.HubTokenUID, rt.HubToken
	if err := h.UpdateRuntime(func(rt *Runtime) {
		rt.HubToken, rt.HubTokenUID, rt.HubTokenExp = key, uid, exp.UTC().Format(time.RFC3339)
		rt.HubTokenOwner = me.Username
		// The hub's own account, if it had one, is set aside: the hub asks for an
		// account again at its next refresh.
		rt.HubPassword, rt.HubPasswordNext = "", ""
	}); err != nil {
		return err
	}
	if oldUID != "" && oldUID != uid {
		// Best effort, as its owner: the old token may already be dead with them.
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(oldUID), nil, central.Auth{PAT: oldKey}, nil)
	}
	slog.Info("hub token taken over", "by", me.Username, "from", rt.TokenOwner())
	return nil
}
