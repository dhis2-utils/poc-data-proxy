package ui

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/central"
	"github.com/dhis2/facility-hub/internal/compat"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/store"
)

// pendingSetup holds the setup user's credentials between form steps (memory only).
type pendingSetup struct {
	Nonce      string
	CentralURL string
	Username   string
	Password   string
	Me         *setupMe
	Version    string
	Facilities []orgUnit
	Facility   *orgUnit
	Others     []registryEntry
	Expires    time.Time
}

type orgUnit struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Code  string `json:"code"`
	Level int    `json:"level"`
	Path  string `json:"path"`
}

type setupMe struct {
	ID                string    `json:"id"`
	Username          string    `json:"username"`
	UserGroups        []idOnly  `json:"userGroups"`
	OrganisationUnits []orgUnit `json:"organisationUnits"`
}

type idOnly struct {
	ID string `json:"id"`
}

type registryEntry struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

type setupFlow struct {
	mu      sync.Mutex
	pending *pendingSetup
}

func (s *setupFlow) get(nonce string) *pendingSetup {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil || s.pending.Nonce != nonce || time.Now().After(s.pending.Expires) {
		return nil
	}
	return s.pending
}

func (u *UI) handleSetup(w http.ResponseWriter, r *http.Request) {
	h := u.H
	if h.Mode() != core.ModeSetup {
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
		return
	}
	if r.Method == http.MethodGet {
		u.render(w, "setup", u.setupForm(nil))
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	r.ParseForm()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	var p *pendingSetup
	var err error
	switch r.FormValue("step") {
	case "login":
		src, user := auth.Source(r), r.FormValue("username")
		if f := u.Auth.Throttled(src, user); f != nil {
			err = errors.New("too many failed logins; try again later")
			break
		}
		if h.SetupMode() == core.SetupToken {
			if err = checkSetupToken(h, r.FormValue("setup_token")); err != nil {
				u.Auth.Count(src, user, true)
				break
			}
		}
		p, err = u.setupLogin(ctx, r.FormValue("central_url"), user, r.FormValue("password"))
		u.Auth.Count(src, user, err != nil)
	case "facility":
		if p = u.setup.get(r.FormValue("nonce")); p == nil {
			err = errors.New("setup session expired, start again")
			break
		}
		i := slices.IndexFunc(p.Facilities, func(o orgUnit) bool { return o.ID == r.FormValue("facility") })
		if i < 0 {
			err = errors.New("pick a facility from the list")
			break
		}
		f := p.Facilities[i]
		p.Facility = &f
		err = u.checkRegistry(ctx, p)
	case "confirm":
		if p = u.setup.get(r.FormValue("nonce")); p == nil {
			err = errors.New("setup session expired, start again")
			break
		}
		if len(p.Others) > 0 && r.FormValue("replace") != "yes" {
			err = errors.New("another hub is registered for this facility; tick the box to replace it")
		}
	}
	if err != nil {
		data := u.setupForm(p)
		data["Error"] = err.Error()
		u.render(w, "setup", data)
		return
	}
	switch {
	case p.Facility == nil:
		u.render(w, "setup", map[string]any{"Pending": p, "Step": "facility", "HubID": h.HubID()})
	case len(p.Others) > 0 && r.FormValue("step") != "confirm":
		u.render(w, "setup", map[string]any{"Pending": p, "Step": "confirm", "HubID": h.HubID()})
	default:
		sess, err := u.completeSetup(ctx, p)
		if err != nil {
			slog.Error("setup failed", "err", err)
			u.render(w, "setup", map[string]any{"Error": "Setup failed: " + err.Error(), "CentralURL": p.CentralURL, "HubID": h.HubID(), "Step": ""})
			return
		}
		u.setup.mu.Lock()
		u.setup.pending = nil
		u.setup.mu.Unlock()
		os.Remove(setupTokenPath(h)) // used; a reset makes a new one
		core.SetSessionCookie(w, sess, h.Cfg.TLS, h.Sessions.Max)
		u.render(w, "setup_done", map[string]any{"Facility": p.Facility, "HubID": h.HubID()})
	}
}

// setupForm is the data for the first setup page. The URL shown comes only from the
// image, never from an earlier submission, which a cross-site post could have made.
func (u *UI) setupForm(p *pendingSetup) map[string]any {
	h := u.H
	return map[string]any{"CentralURL": h.Cfg.CentralURL, "Pinned": h.SetupMode() == core.SetupPinned,
		"NeedToken": h.SetupMode() == core.SetupToken, "Hosts": strings.Join(h.Cfg.CentralHosts, ", "),
		"HubID": h.HubID(), "Pending": p, "Step": ""}
}

// setupLogin runs spec 6.2 steps 2-5.
func (u *UI) setupLogin(ctx context.Context, centralURL, username, password string) (p *pendingSetup, err error) {
	h := u.H
	centralURL = strings.TrimRight(strings.TrimSpace(centralURL), "/")
	switch {
	case h.SetupMode() == core.SetupPinned || centralURL == "":
		centralURL = h.Cfg.CentralURL
	case !h.CentralAllowed(centralURL):
		// Whoever reaches the setup page must not point the hub, and every later staff
		// login, at another server (review M8, REVIEW-2 F1).
		if len(h.Cfg.CentralHosts) > 0 {
			return nil, fmt.Errorf("the central server must be an https URL on %s", strings.Join(h.Cfg.CentralHosts, ", "))
		}
		return nil, errors.New("enter the central server's https:// URL")
	}
	if centralURL == "" {
		return nil, errors.New("enter the central server's https:// URL")
	}
	prev := h.Central.BaseURL()
	defer func() {
		if err != nil {
			h.Central.SetBaseURL(prev) // a failed attempt leaves nothing behind
		}
	}()
	h.Central.SetBaseURL(centralURL)
	if !h.Monitor.Probe(ctx, centralURL) {
		return nil, errors.New("central server is not reachable; the hub must be online during setup")
	}
	auth := central.Auth{Username: username, Password: password}
	me := &setupMe{}
	err = h.Central.JSON(ctx, "GET", central.Q("/api/me", "fields", "id,username,userGroups[id],organisationUnits[id,name,code,level,path]"), nil, auth, me)
	if err != nil {
		if central.Status(err) == 401 {
			return nil, errors.New("wrong username or password")
		}
		return nil, err
	}
	version, err := h.RefreshServerVersion(ctx, auth)
	if err != nil {
		return nil, fmt.Errorf("reading server version: %w", err)
	}
	if compat.For(version) == nil {
		return nil, fmt.Errorf("this hub release does not support DHIS2 %s (supported: %v); upgrade the hub first", version, compat.SupportedMajors())
	}
	p = &pendingSetup{Nonce: core.NewUID(), CentralURL: centralURL, Username: username, Password: password, Me: me,
		Version: version, Expires: time.Now().Add(5 * time.Minute)} // holds the password
	// The facility-level org units in the user's scope: one is taken as it is; several
	// are offered. A single org unit above that level (a district officer's) is not a
	// facility: its facilities are offered.
	if p.Facilities, err = u.facilityChoices(ctx, auth, me.OrganisationUnits); err != nil {
		return nil, err
	}
	switch len(p.Facilities) {
	case 0:
		return nil, errors.New("no facility-level org units in your scope")
	case 1:
		p.Facility, p.Facilities = &p.Facilities[0], nil
	}
	u.setup.mu.Lock()
	u.setup.pending = p
	u.setup.mu.Unlock()
	if p.Facility != nil {
		if err := u.checkRegistry(ctx, p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// facilityChoices lists facility-level (lowest level) org units under the user's org units.
func (u *UI) facilityChoices(ctx context.Context, auth central.Auth, roots []orgUnit) ([]orgUnit, error) {
	var levels struct {
		OrganisationUnitLevels []struct {
			Level int `json:"level"`
		} `json:"organisationUnitLevels"`
	}
	if err := u.H.Central.JSON(ctx, "GET", central.Q("/api/organisationUnitLevels", "fields", "level", "order", "level:desc",
		"pageSize", "1"), nil, auth, &levels); err != nil {
		return nil, err
	}
	if len(levels.OrganisationUnitLevels) == 0 {
		return nil, errors.New("no org unit levels on central")
	}
	maxLevel := levels.OrganisationUnitLevels[0].Level
	var out []orgUnit
	for _, root := range roots {
		if root.Level == maxLevel {
			out = append(out, root)
			continue
		}
		var resp struct {
			OrganisationUnits []orgUnit `json:"organisationUnits"`
		}
		if err := u.H.Central.JSON(ctx, "GET", central.Q("/api/organisationUnits", "filter", "path:like:"+root.ID,
			"filter", fmt.Sprintf("level:eq:%d", maxLevel), "fields", "id,name,code,level,path", "order", "name:asc",
			"paging", "false"), nil, auth, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.OrganisationUnits...)
	}
	return out, nil
}

// checkRegistry finds other active hubs for the facility (spec 6.2 step 7).
func (u *UI) checkRegistry(ctx context.Context, p *pendingSetup) error {
	var resp struct {
		Entries []registryEntry `json:"entries"`
	}
	err := u.H.Central.JSON(ctx, "GET", central.Q("/api/dataStore/facility-hub-registry", "fields", ".",
		"filter", "facility:eq:"+p.Facility.ID, "paging", "false"), nil, central.Auth{Username: p.Username, Password: p.Password}, &resp)
	if err != nil && central.Status(err) != 404 {
		return fmt.Errorf("reading hub registry: %w", err)
	}
	p.Others = nil
	for _, e := range resp.Entries {
		if e.Key != u.H.HubID() && fmt.Sprint(e.Value["status"]) != "retired" {
			p.Others = append(p.Others, e)
		}
	}
	return nil
}

// completeSetup runs spec 6.2 steps 7-9.
func (u *UI) completeSetup(ctx context.Context, p *pendingSetup) (*core.Session, error) {
	h := u.H
	basic := central.Auth{Username: p.Username, Password: p.Password}
	dk, err := hubcrypto.NewKey()
	if err != nil {
		return nil, err
	}
	if err := h.Store.SetDK(dk); err != nil {
		return nil, err
	}
	h.Store.SetMeta(core.MetaCentralURL, p.CentralURL)

	// Hub-level token (spec 6.2 step 8, 11.2). POC: the setup user's PAT.
	tokExp := time.Now().Add(time.Duration(h.Cfg.PATExpiryDays) * 24 * time.Hour)
	tokUID, tok, err := h.CreatePAT(ctx, basic, tokExp)
	if err != nil {
		return nil, fmt.Errorf("creating hub token: %w", err)
	}
	hubAuth := central.Auth{PAT: tok}
	cat, err := h.FetchCatalog(ctx, hubAuth, p.Facility.ID)
	if err != nil {
		return nil, fmt.Errorf("reading programs: %w", err)
	}
	var aoc struct {
		CategoryOptionCombos []idOnly `json:"categoryOptionCombos"`
	}
	h.Central.JSON(ctx, "GET", central.Q("/api/categoryOptionCombos", "filter", "name:eq:default", "fields", "id"), nil, hubAuth, &aoc)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	rt := &core.Runtime{Facility: p.Facility.ID, FacilityName: p.Facility.Name, FacilityCode: p.Facility.Code,
		FacilityPath: p.Facility.Path,
		Catalog:      cat, HubToken: tok, HubTokenUID: tokUID,
		HubTokenExp: tokExp.UTC().Format(time.RFC3339), SetupBy: p.Username, SetupAt: store.Now(), PrivateKey: priv}
	if len(aoc.CategoryOptionCombos) > 0 {
		rt.DefaultAOC = aoc.CategoryOptionCombos[0].ID
	}
	if err := h.SaveRuntime(rt); err != nil {
		return nil, err
	}
	h.Store.SetMeta(core.MetaPublicKey, base64.StdEncoding.EncodeToString(pub))

	// A replacement is allowed at any time (docs/DEPLOYMENT.md): patient data is not at
	// stake, since a new hub gets data only through each user's own login. It is
	// recorded on both entries, so a mistaken one can be found and undone.
	var replaces []string
	for _, o := range p.Others {
		o.Value["status"] = "retired"
		o.Value["retiredAt"] = store.Now()
		o.Value["retiredBy"] = h.HubID()
		o.Value["retiredByUser"] = p.Username
		replaces = append(replaces, o.Key)
		if err := h.Central.JSON(ctx, "PUT", "/api/dataStore/facility-hub-registry/"+url.PathEscape(o.Key), o.Value, hubAuth, nil); err != nil {
			return nil, fmt.Errorf("retiring hub %s: %w", o.Key, err)
		}
	}
	reg := map[string]any{"hubId": h.HubID(), "facility": p.Facility.ID, "facilityName": p.Facility.Name,
		"setupBy": p.Username, "setupAt": rt.SetupAt, "hubVersion": h.Version, "publicKey": base64.StdEncoding.EncodeToString(pub),
		"status": "active"}
	if len(replaces) > 0 {
		reg["replaces"] = replaces
	}
	if err := h.Central.JSON(ctx, "PUT", "/api/dataStore/facility-hub-registry/"+h.HubID(), reg, hubAuth, nil); err != nil {
		return nil, fmt.Errorf("registering hub: %w", err)
	}
	h.Store.SetMeta(core.MetaMode, core.ModeActive)
	slog.Info("hub set up", "facility", p.Facility.ID, "programs", len(cat.Programs), "by", p.Username)

	sess, f := u.Auth.LoginBasic(ctx, "setup", p.Username, p.Password)
	if f != nil {
		return nil, fmt.Errorf("registering setup user: HTTP %d", f.Status)
	}
	h.TriggerSync() // first full pull (spec 6.2 step 9)
	return sess, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
