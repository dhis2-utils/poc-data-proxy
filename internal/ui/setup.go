package ui

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
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
	"github.com/dhis2/facility-hub/internal/hubaccount"
	"github.com/dhis2/facility-hub/internal/hubcrypto"
	"github.com/dhis2/facility-hub/internal/stick"
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
	server  *checkedServer
}

// checkedServer is central's address as the set-up's first step checked it: https
// with a trusted certificate, the right setup code, and the server's own name shown.
// The login step uses this address, never one posted with the login.
type checkedServer struct {
	Nonce, URL, Title, Host string
	Expires                 time.Time
}

func (s *setupFlow) checked(nonce string) *checkedServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server == nil || s.server.Nonce != nonce || time.Now().After(s.server.Expires) {
		return nil
	}
	return s.server
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
	if h.Cfg.SetupLocalOnly && !h.FromThisMachine(r.RemoteAddr) {
		// A hub on a PC: being at the PC proves presence at the facility.
		http.Error(w, "Set up this hub on the PC it runs on: open https://localhost there.", http.StatusForbidden)
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
	case "restore":
		// A new disk restored from the USB stick (docs/RECOVERY.md, C2): only with the
		// password of a staff member of that hub, checked against the copy's key slots.
		src, user, id := auth.Source(r), r.FormValue("username"), r.FormValue("hub")
		if f := u.Auth.Throttled(src, user); f != nil {
			err = errors.New("too many failed logins; try again later")
			break
		}
		err = stick.Stage(h.Cfg.USBDir, h.Cfg.DataDir, id, user, r.FormValue("password"))
		u.Auth.Count(src, user, err != nil)
		if err != nil {
			break
		}
		slog.Warn("restore from the USB stick staged; restarting", "hub", id, "by", user)
		if u.Restart != nil {
			go func() { time.Sleep(time.Second); u.Restart() }()
		}
		u.render(w, "setup", map[string]any{"Step": "restarting", "RestoredID": id, "HubID": h.HubID()})
		return
	case "server":
		// Central's address and the setup code, checked before anyone logs in.
		src := auth.Source(r)
		if f := u.Auth.Throttled(src, "setup-code"); f != nil {
			err = errors.New("too many wrong setup codes; try again later")
		} else if needToken(h) {
			err = checkSetupToken(h, r.FormValue("setup_token"))
			u.Auth.Count(src, "setup-code", err != nil)
		}
		var sv *checkedServer
		if err == nil {
			sv, err = u.checkServer(ctx, r.FormValue("central_url"))
		}
		if err != nil {
			data := u.setupForm(nil)
			data["Error"] = err.Error()
			u.render(w, "setup", data)
			return
		}
		u.render(w, "setup", map[string]any{"Step": "login", "Server": sv, "HubID": h.HubID()})
		return
	case "login":
		src, user := auth.Source(r), r.FormValue("username")
		if f := u.Auth.Throttled(src, user); f != nil {
			err = errors.New("too many failed logins; try again later")
			break
		}
		centralURL := r.FormValue("central_url")
		if h.SetupMode() != core.SetupPinned {
			// Only an address the server step checked, with its setup code.
			sv := u.setup.checked(r.FormValue("server"))
			if sv == nil {
				err = errors.New("start with central's address and the setup code")
				break
			}
			centralURL = sv.URL
		}
		p, err = u.setupLogin(ctx, centralURL, user, r.FormValue("password"))
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
	default:
		err = errors.New("start again")
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
		if err != nil && h.Mode() == core.ModeActive {
			// Set up; only the set-up user's session on the hub failed: they log in.
			slog.Warn("set up, but the set-up user is not logged in on the hub", "err", err)
		} else if err != nil {
			slog.Error("setup failed", "err", err)
			u.render(w, "setup", map[string]any{"Error": "Setup failed: " + err.Error(), "CentralURL": p.CentralURL, "HubID": h.HubID(), "Step": ""})
			return
		}
		u.setup.mu.Lock()
		u.setup.pending = nil
		u.setup.mu.Unlock()
		os.Remove(setupTokenPath(h)) // used; a reset makes a new one
		if sess != nil {
			core.SetSessionCookie(w, sess, h.Cfg.TLS, h.Sessions.Max)
		}
		u.render(w, "setup_done", map[string]any{"Facility": p.Facility, "HubID": h.HubID()})
	}
}

// checkServer checks central's address before anyone logs in: https with a certificate
// the system trusts (the hub's client verifies it), reachable, and its own name
// (loginConfig needs no login), shown with its host so staff can tell their server.
func (u *UI) checkServer(ctx context.Context, raw string) (*checkedServer, error) {
	h := u.H
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	pu, err := url.Parse(raw)
	if err != nil || pu.Scheme != "https" || pu.Host == "" || !h.CentralAllowed(raw) {
		if len(h.Cfg.CentralHosts) > 0 {
			return nil, fmt.Errorf("the central server must be an https URL on %s", strings.Join(h.Cfg.CentralHosts, ", "))
		}
		return nil, errors.New("enter central's https:// address (from the installation card)")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", raw+"/api/loginConfig", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := h.Central.HTTP.Do(req)
	if err != nil {
		var cerr *tls.CertificateVerificationError
		if errors.As(err, &cerr) {
			return nil, fmt.Errorf("%s does not have a certificate this hub trusts; check the address", pu.Host)
		}
		return nil, fmt.Errorf("cannot reach %s; the hub must be online to be set up", pu.Host)
	}
	defer resp.Body.Close()
	var lc struct {
		ApplicationTitle string `json:"applicationTitle"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&lc) != nil {
		return nil, fmt.Errorf("%s does not answer as a DHIS2 server", pu.Host)
	}
	sv := &checkedServer{Nonce: core.NewUID(), URL: raw, Title: cmp.Or(lc.ApplicationTitle, "DHIS2"), Host: pu.Host,
		Expires: time.Now().Add(10 * time.Minute)}
	u.setup.mu.Lock()
	u.setup.server = sv
	u.setup.mu.Unlock()
	return sv, nil
}

// setupForm is the data for the first setup page. The URL shown comes only from the
// image, never from an earlier submission, which a cross-site post could have made.
func (u *UI) setupForm(p *pendingSetup) map[string]any {
	h := u.H
	var restores []stick.Candidate
	if h.Cfg.USBDir != "" {
		restores = stick.Candidates(h.Cfg.USBDir)
	}
	return map[string]any{"CentralURL": h.Cfg.CentralURL, "Pinned": h.SetupMode() == core.SetupPinned,
		"NeedToken": needToken(h), "Hosts": strings.Join(h.Cfg.CentralHosts, ", "),
		"HubID": h.HubID(), "Pending": p, "Step": "", "Restores": restores, "Net": u.netView()}
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
	// The hub serves the org unit it is set up for: the user's only one, or the one
	// they choose among theirs, at whatever level. A ward or a community can be a place
	// of its own; there is no facility level to configure.
	switch len(me.OrganisationUnits) {
	case 0:
		return nil, errors.New("you are not assigned to any org unit")
	case 1:
		p.Facility = &me.OrganisationUnits[0]
	default:
		p.Facilities = slices.Clone(me.OrganisationUnits)
		slices.SortFunc(p.Facilities, func(a, b orgUnit) int { return strings.Compare(a.Name, b.Name) })
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
func (u *UI) completeSetup(ctx context.Context, p *pendingSetup) (_ *core.Session, err error) {
	h := u.H
	basic := central.Auth{Username: p.Username, Password: p.Password}

	// Hub-level token (spec 6.2 step 8, 11.2). POC: the setup user's PAT.
	tokExp := time.Now().Add(h.TokenLife())
	tokUID, tok, err := h.CreatePAT(ctx, basic, tokExp)
	if err != nil {
		return nil, fmt.Errorf("creating hub token: %w", err)
	}
	hubAuth := central.Auth{PAT: tok}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	setupAt := store.Now()
	boxPub, boxPriv, err := hubaccount.NewBoxKey()
	if err != nil {
		return nil, err
	}
	// accountRequested: the national team approves the hub in the Facility Hub app, which
	// gives it an account of its own, sent encrypted to boxKey (docs/DEPLOYMENT.md, "The
	// hub's own account").
	reg := map[string]any{"hubId": h.HubID(), "facility": p.Facility.ID, "facilityName": p.Facility.Name,
		"setupBy": p.Username, "setupAt": setupAt, "hubVersion": h.Version, "publicKey": base64.StdEncoding.EncodeToString(pub),
		"status": "active", "accountRequested": true, "boxKey": base64.StdEncoding.EncodeToString(boxPub)}
	// The registry entry first, before the hub commits anything: the hub's housekeeping
	// writes the facility-hub namespaces with this token, and countries protect them for
	// a "hub owners" group (docs/DEPLOYMENT.md). Someone outside it is told so, and the
	// hub stays as it was.
	if err := h.Central.JSON(ctx, "PUT", core.RegistryPath(h.HubID()), reg, hubAuth, nil); err != nil {
		h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+url.PathEscape(tokUID), nil, hubAuth, nil)
		if central.Status(err) == http.StatusForbidden {
			return nil, core.ErrNotHubOwner
		}
		return nil, fmt.Errorf("registering hub: %w", err)
	}
	h.ProtectRegistry(ctx, hubAuth)
	committed := false
	defer func() {
		// A set-up that fails from here, before the hub goes active, leaves nothing
		// behind on central; once active, the hub keeps both.
		if err != nil && !committed {
			h.Central.JSON(context.WithoutCancel(ctx), "DELETE", core.RegistryPath(h.HubID()), nil, hubAuth, nil)
			h.Central.JSON(context.WithoutCancel(ctx), "DELETE", "/api/apiToken/"+url.PathEscape(tokUID), nil, hubAuth, nil)
		}
	}()
	dk, err := hubcrypto.NewKey()
	if err != nil {
		return nil, err
	}
	if err := h.Store.SetDK(dk); err != nil {
		return nil, err
	}
	h.Store.SetMeta(core.MetaCentralURL, p.CentralURL)
	cat, err := h.FetchCatalog(ctx, hubAuth, p.Facility.ID)
	if err != nil {
		return nil, fmt.Errorf("reading programs: %w", err)
	}
	var aoc struct {
		CategoryOptionCombos []idOnly `json:"categoryOptionCombos"`
	}
	h.Central.JSON(ctx, "GET", central.Q("/api/categoryOptionCombos", "filter", "name:eq:default", "fields", "id"), nil, hubAuth, &aoc)
	rt := &core.Runtime{Facility: p.Facility.ID, FacilityName: p.Facility.Name, FacilityCode: p.Facility.Code,
		FacilityPath: p.Facility.Path,
		Catalog:      cat, HubToken: tok, HubTokenUID: tokUID,
		HubTokenExp: tokExp.UTC().Format(time.RFC3339), SetupBy: p.Username, SetupAt: setupAt, PrivateKey: priv, BoxKey: boxPriv}
	if len(aoc.CategoryOptionCombos) > 0 {
		rt.DefaultAOC = aoc.CategoryOptionCombos[0].ID
	}
	if err := h.SaveRuntime(rt); err != nil {
		return nil, err
	}
	h.Store.SetMeta(core.MetaPublicKey, base64.StdEncoding.EncodeToString(pub))

	// A replacement is allowed at any time (docs/DEPLOYMENT.md): patient data is not at
	// stake, since a new hub gets data only through each user's own login. It is
	// recorded on both entries, so a mistaken one can be found and undone. The others
	// are retired once this hub's entry is final and the hub is active: a set-up that
	// fails must not leave the facility with no active hub.
	var replaces []string
	for _, o := range p.Others {
		replaces = append(replaces, o.Key)
	}
	if len(replaces) > 0 {
		reg["replaces"] = replaces
	}
	if err := h.Central.JSON(ctx, "PUT", core.RegistryPath(h.HubID()), reg, hubAuth, nil); err != nil {
		return nil, fmt.Errorf("registering hub: %w", err)
	}
	h.Store.SetMeta(core.MetaMode, core.ModeActive)
	committed = true
	for _, o := range p.Others {
		o.Value["status"] = "retired"
		o.Value["retiredAt"] = store.Now()
		o.Value["retiredBy"] = h.HubID()
		o.Value["retiredByUser"] = p.Username
		if err := h.Central.JSON(ctx, "PUT", core.RegistryPath(o.Key), o.Value, hubAuth, nil); err != nil {
			slog.Warn("the hub this one replaces is not marked retired; mark it on central", "hub", o.Key, "err", err)
		}
	}
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
