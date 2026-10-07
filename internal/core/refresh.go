package core

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/access"
	"github.com/dhis2/facility-hub/internal/central"
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
	Authorities                []string `json:"authorities"`
	UserGroups                 []idRef  `json:"userGroups"`
}

const meFields = "id,username,organisationUnits[id],teiSearchOrganisationUnits[id],authorities,userGroups[id]"

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
		l.TETs[p.TrackedEntityType.ID] = p.TrackedEntityType.Access.Data
		for _, s := range p.ProgramStages {
			l.Stages[s.ID] = s.Access.Data
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
	l, err := h.FetchAccess(ctx, auth)
	if err != nil {
		if central.Status(err) == http.StatusUnauthorized {
			h.Store.SetNeedsLogin(userID, true)
		}
		return err
	}
	return h.SetAccess(userID, l)
}

// EnsurePAT creates a PAT for the user unless a valid one is stored (spec 7.1 step 2).
// with is the credential to create it with (the user's fresh central session).
// If central refuses, the encrypted password is stored instead (spec 7.1 fallback).
func (h *Hub) EnsurePAT(ctx context.Context, u *store.User, with central.Auth, password string) error {
	if u.HasPAT && !u.NeedsLogin {
		if exp, err := time.Parse(time.RFC3339, u.PATExpires); err == nil && time.Until(exp) > 7*24*time.Hour {
			return nil
		}
	}
	api := h.API()
	if api == nil {
		return fmt.Errorf("unsupported server version %q", h.ServerVersion())
	}
	exp := time.Now().Add(time.Duration(h.Cfg.PATExpiryDays) * 24 * time.Hour)
	uid, key, err := h.CreatePAT(ctx, with, exp)
	if err != nil {
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

// CreatePAT creates a personal access token on central.
func (h *Hub) CreatePAT(ctx context.Context, with central.Auth, exp time.Time) (uid, key string, err error) {
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
	return api.PATResponse(b)
}

// RefreshServerVersion reads central's version (spec 6.2 step 5, refreshed on sync).
func (h *Hub) RefreshServerVersion(ctx context.Context, auth central.Auth) (string, error) {
	var info struct {
		Version string `json:"version"`
	}
	if err := h.Central.JSON(ctx, "GET", "/api/system/info", nil, auth, &info); err != nil {
		return "", err
	}
	if info.Version != "" {
		h.Store.SetMeta(MetaServerVersion, info.Version)
	}
	return info.Version, nil
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
			TrackedEntityType struct {
				ID                          string `json:"id"`
				TrackedEntityTypeAttributes []attr `json:"trackedEntityTypeAttributes"`
			} `json:"trackedEntityType"`
			ProgramStages []struct {
				ID        string `json:"id"`
				SortOrder int    `json:"sortOrder"`
			} `json:"programStages"`
			ProgramTrackedEntityAttributes []attr `json:"programTrackedEntityAttributes"`
		} `json:"programs"`
	}
	af := "trackedEntityAttribute[id,generated,unique,pattern]"
	path := central.Q("/api/programs", "filter", "programType:eq:WITH_REGISTRATION",
		"filter", "organisationUnits.id:eq:"+facility,
		"fields", "id,name,trackedEntityType[id,trackedEntityTypeAttributes["+af+"]],programStages[id,sortOrder],programTrackedEntityAttributes["+af+"]",
		"paging", "false")
	if err := h.Central.JSON(ctx, "GET", path, nil, auth, &resp); err != nil {
		return nil, err
	}
	c := &access.Catalog{Programs: map[string]*access.ProgramMeta{}, TETAttributes: map[string][]access.AttrMeta{}}
	for _, p := range resp.Programs {
		pm := &access.ProgramMeta{ID: p.ID, Name: p.Name, TET: p.TrackedEntityType.ID, Attributes: conv(p.ProgramTrackedEntityAttributes)}
		for _, s := range p.ProgramStages {
			pm.Stages = append(pm.Stages, s.ID)
		}
		c.Programs[p.ID] = pm
		c.TETAttributes[p.TrackedEntityType.ID] = conv(p.TrackedEntityType.TrackedEntityTypeAttributes)
	}
	c.Index()
	return c, nil
}
