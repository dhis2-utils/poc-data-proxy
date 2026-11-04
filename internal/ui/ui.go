// Package ui serves the hub's own pages and API (spec 12): /hub/status, the setup flow
// (spec 6.2) and the admin page. No patient data is shown beyond UIDs, except to
// the signed-in user their own refused changes (refusals.go).
package ui

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dhis2/facility-hub/internal/devkey"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"time"

	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/netsetup"
	"github.com/dhis2/facility-hub/internal/store"
	hubsync "github.com/dhis2/facility-hub/internal/sync"
	"github.com/dhis2/facility-hub/internal/tracker"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/setup.js
var setupJS []byte

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"short": func(s string) string {
		if len(s) > 19 {
			return s[:19]
		}
		return s
	},
	"json": func(v any) string { b, _ := json.Marshal(v); return string(b) },
	"gb":   func(b uint64) string { return fmt.Sprintf("%.0f GB", float64(b)/(1<<30)) },
}).ParseFS(templateFS, "templates/*.html"))

type UI struct {
	H      *core.Hub
	Auth   *auth.Auth
	Worker *hubsync.Worker
	// Reset wipes the store and returns to setup mode (set by main).
	Reset func(by string, discard bool) error
	// Restart restarts the hub process (nil where nothing restarts it, as in tests).
	Restart func()
	// ChooseUSB keeps the hub's copy on the removable drive at a root (Windows,
	// usb_dir: auto), or on the stick plugged in (stick.Stick.Choose).
	ChooseUSB func(root string) error
	// Net manages the network on a Pi (nil elsewhere): the pages' network section.
	Net   *netsetup.Manager
	setup setupFlow
}

func (u *UI) Register(mux *http.ServeMux) {
	mux.HandleFunc("/hub/status", u.handleStatus)
	mux.HandleFunc("/hub/setup", u.handleSetup)
	mux.HandleFunc("/hub/setup.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Write(setupJS)
	})
	mux.HandleFunc("/hub/login", u.handleLogin)
	mux.HandleFunc("/hub/logout", u.handleLogout)
	mux.HandleFunc("/hub/ui", u.admin(u.handleAdmin))
	mux.HandleFunc("/hub/ui/sync", u.admin(u.handleSyncNow))
	mux.HandleFunc("/hub/ui/uplink", u.admin(u.handleUplink))
	mux.HandleFunc("/hub/ui/proxy", u.admin(u.handleProxy))
	mux.HandleFunc("/hub/ui/usb", u.admin(u.handleUSB))
	mux.HandleFunc("/hub/network/join", u.handleNetwork(false))
	mux.HandleFunc("/hub/network/facility", u.handleNetwork(true))
	mux.HandleFunc("/hub/ui/wifi-password", u.admin(u.handleWifiPassword))
	mux.HandleFunc("/hub/ui/reset", u.admin(u.handleReset))
	mux.HandleFunc("/hub/ui/takeover", u.admin(u.handleTakeOver))
	mux.HandleFunc("/hub/ui/move", u.admin(u.handleMove))
	mux.HandleFunc("/hub/ui/endpoints", u.admin(u.handleEndpoints))
	// The hub's authority, for installing on devices (public; also on plain HTTP).
	mux.HandleFunc("/hub/ca.crt", func(w http.ResponseWriter, r *http.Request) { serveAuthority(w, r, u.H.Cfg.DataDir) })
	mux.HandleFunc("/hub/", func(w http.ResponseWriter, r *http.Request) {
		if u.H.Mode() == core.ModeSetup {
			http.Redirect(w, r, "/hub/setup", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
	})
}

func (u *UI) render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := tmpl.ExecuteTemplate(w, name+".html", data); err != nil {
		slog.Error("template", "name", name, "err", err)
	}
}

// StatusBody is GET /hub/status (spec 12).
func StatusBody(h *core.Hub) map[string]any {
	mode := "offline"
	if h.Online() {
		mode = "online"
	}
	lastPush, lastPull := h.LastPushPull()
	out := map[string]any{"mode": mode, "hubId": h.HubID(), "lastPush": nullable(lastPush), "lastPull": nullable(lastPull),
		"pending": h.Store.CountObjects(store.SyncPending), "rejected": h.Store.CountObjects(store.SyncRejected),
		"version": h.Version, "locked": !h.Store.Unlocked(), "setup": h.Mode() == core.ModeSetup}
	if start, running := h.Stats.Cycle(); start != "" {
		out["syncStarted"], out["syncing"] = start, running
	}
	if h.Retired() {
		out["retired"] = true
		out["message"] = "this hub was replaced by another hub for the facility; it only sends its remaining changes"
		if h.Revoked() {
			out["message"] = "this hub's tokens were revoked on central; it serves nothing until it is reset"
		}
	}
	if p := devkey.Problem(); p != "" {
		out["message"] = p
	}
	if n := h.StuckOutbox(); n > 0 {
		out["stuckOutbox"] = n
		out["message"] = fmt.Sprintf("%d queued change(s) wait for their author to log in online; ask them to, or an admin to review", n)
	}
	if h.ServerVersion() != "" && h.API() == nil {
		out["needsUpgrade"] = true
		out["message"] = "hub needs upgrade: DHIS2 " + h.ServerVersion() + " is not supported by this hub release"
	}
	return out
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (u *UI) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	core.WriteJSON(w, 200, StatusBody(u.H))
}

// --- admin auth --------------------------------------------------------------------

// admin requires a hub session of a user assigned to the facility (the facility in
// their capture scope): the staff who use the hub run it, with no setup group.
func (u *UI) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u.H.Mode() == core.ModeSetup {
			http.Redirect(w, r, "/hub/setup", http.StatusFound)
			return
		}
		sess := u.H.Sessions.FromCookie(r)
		if sess == nil || sess.UserID == 0 {
			http.Redirect(w, r, "/hub/login", http.StatusFound)
			return
		}
		if !u.H.Access(sess.UserID).InFacility(u.H.FacilityPath()) {
			w.WriteHeader(http.StatusForbidden)
			u.render(w, "login", map[string]any{"Error": "Only staff assigned to this facility can open this page."})
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// sameOrigin refuses cross-site form posts to the setup and admin pages. Browsers send
// Origin on form posts, and Sec-Fetch-Site on every request; a post with neither is
// refused (REVIEW-2 F9).
func sameOrigin(r *http.Request) bool {
	// Hub pages send Referrer-Policy: no-referrer, under which browsers post forms with
	// "Origin: null"; their Sec-Fetch-Site then tells (every browser login and setup was
	// refused before this was noticed by hand).
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		return o == "http://"+r.Host || o == "https://"+r.Host
	}
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "same-origin" || site == "none"
}

func (u *UI) handleLogin(w http.ResponseWriter, r *http.Request) {
	if u.H.Mode() == core.ModeSetup {
		http.Redirect(w, r, "/hub/setup", http.StatusFound)
		return
	}
	if r.Method != http.MethodPost {
		var data map[string]any
		if u.H.FromThisMachine(r.RemoteAddr) {
			data = map[string]any{"Addresses": u.H.DeviceAddresses()}
		}
		u.render(w, "login", data)
		return
	}
	r.ParseForm()
	req := auth.LoginRequest{Username: r.FormValue("username"), Password: r.FormValue("password")}
	raw, _ := json.Marshal(req)
	sess, _, f := u.Auth.LoginWeb(r.Context(), auth.Source(r), req, raw)
	if f != nil {
		w.WriteHeader(f.Status)
		u.render(w, "login", map[string]any{"Error": "Login failed (HTTP " + fmt.Sprint(f.Status) + ")"})
		return
	}
	core.SetSessionCookie(w, sess, u.H.Cfg.TLS, u.H.Sessions.Max)
	http.Redirect(w, r, "/hub/ui", http.StatusFound)
}

func (u *UI) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s := u.H.Sessions.FromCookie(r); s != nil {
		u.H.Sessions.Delete(s.Token)
		u.H.Sessions.DropBasic(s.Username)
	}
	core.ClearSessionCookie(w, u.H.Cfg.TLS)
	http.Redirect(w, r, "/hub/login", http.StatusFound)
}

// --- admin page ---------------------------------------------------------------------

type userRow struct {
	Username, LastLogin, AccessRefreshed, PATExpires string
	Credential                                       string
	NeedsLogin, InFacility                           bool
}

func (u *UI) handleAdmin(w http.ResponseWriter, r *http.Request) {
	h := u.H
	outbox, _ := h.Store.RecentOutbox(50)
	issues, _ := h.Store.Issues(false, 100)
	users, _ := h.Store.Users()
	names := map[int64]string{}
	var rows []userRow
	for _, us := range users {
		names[us.ID] = us.Username
		cred := "none"
		switch {
		case us.HasPAT:
			cred = "PAT"
		case us.HasPassword:
			cred = "password (PAT fallback)"
		}
		rows = append(rows, userRow{Username: us.Username, LastLogin: us.LastLoginAt, AccessRefreshed: us.AccessRefreshedAt,
			PATExpires: us.PATExpires, Credential: cred, NeedsLogin: us.NeedsLogin, InFacility: h.Access(us.ID).InFacility(h.FacilityPath())})
	}
	type issueRow struct {
		*store.Issue
		User    string
		Message string
		Object  string
	}
	var irows []issueRow
	for _, is := range issues {
		var e struct {
			Message string `json:"message"`
			Reports []struct {
				ErrorCode string `json:"errorCode"`
				Message   string `json:"message"`
			} `json:"errorReports"`
		}
		json.Unmarshal(is.Error, &e)
		msg := e.Message
		if len(e.Reports) > 0 {
			msg = e.Reports[0].ErrorCode + ": " + e.Reports[0].Message
		}
		row := issueRow{Issue: is, User: names[is.UserID], Message: tracker.Redact(msg), Object: is.ObjectUID}
		if is.Kind == store.OutboxAggregate {
			var x struct {
				Edit struct {
					Value *string `json:"value"`
				} `json:"edit"`
			}
			json.Unmarshal(is.Error, &x)
			row.Object = h.AggLabel(is.ObjectUID)
			if x.Edit.Value != nil {
				row.Object += ": " + *x.Edit.Value
			}
		}
		irows = append(irows, row)
	}
	rt := h.Runtime()
	var programs []string
	for _, id := range h.Catalog().ProgramIDs() {
		programs = append(programs, id+" "+h.Catalog().Programs[id].Name)
	}
	upgrade := h.Store.Meta(core.MetaUpgrade)
	var own []*ownRefusal
	if sess := h.Sessions.FromCookie(r); sess != nil && sess.UserID != 0 {
		own = u.ownRefusals(sess.UserID)
	}
	u.render(w, "admin", map[string]any{
		"Status": StatusBody(h), "Heartbeat": hubsync.Status(h), "Runtime": rt, "Programs": programs,
		"Outbox": outbox, "Issues": irows, "Users": rows, "Names": names, "Objects": h.Index.Len(),
		"Forced": h.Monitor.ForcedOffline(), "UplinkSwitch": h.Cfg.UplinkSwitch, "Upgrade": upgrade, "Central": h.Central.BaseURL(),
		"ServerVersion": h.ServerVersion(), "Msg": r.URL.Query().Get("msg"),
		"AggWarnings": h.AggWarnings(3 * 24 * time.Hour), "Orphans": h.Store.PendingWithoutUser(),
		"Offline": !h.Online(), "AccountCode": h.AccountCode(), "Housekeeping": h.Stats.HousekeepingError(),
		"Reach": reachText(h.Reach()), "Proxy": h.Proxy(), "Net": u.netView(true),
		"OwnRefusals": own,
	})
}

func (u *UI) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
		return
	}
	// The scheduler runs it: one cycle at a time, and without a time limit, since a
	// first pull of a large facility takes longer than any fixed one.
	u.H.TriggerSync()
	http.Redirect(w, r, "/hub/ui?msg=Sync+started", http.StatusFound)
}

// handleUplink lets a demo or test "cut the uplink" without touching the network.
// Off unless hub.yaml has uplink_switch: a hub that looks offline lets staff type in a
// new central address.
func (u *UI) handleUplink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !u.H.Cfg.UplinkSwitch {
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
		return
	}
	switch r.FormValue("state") {
	case "off":
		f := false
		u.H.Monitor.Force(&f)
	default:
		u.H.Monitor.Force(nil)
		u.H.Monitor.Probe(r.Context(), u.H.Central.BaseURL())
	}
	http.Redirect(w, r, "/hub/ui", http.StatusFound)
}

// handleUSB records the staff member's choice of stick for the hub's copy.
func (u *UI) handleUSB(w http.ResponseWriter, r *http.Request) {
	msg := "The hub keeps its copy on that stick"
	if r.Method != http.MethodPost || u.ChooseUSB == nil {
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
		return
	}
	if err := u.ChooseUSB(r.FormValue("root")); err != nil {
		msg = err.Error()
	}
	http.Redirect(w, r, "/hub/ui?msg="+url.QueryEscape(msg), http.StatusFound)
}

// handleProxy sets the web proxy for central and GitHub (sites that force one).
func (u *UI) handleProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
		return
	}
	msg := "Proxy saved"
	if err := u.H.SetProxy(r.FormValue("proxy")); err != nil {
		msg = err.Error()
	}
	http.Redirect(w, r, "/hub/ui?msg="+url.QueryEscape(msg), http.StatusFound)
}

// reachText is the admin page's set-up check: do devices find the hub by name?
func reachText(byName, byIP time.Time) string {
	switch {
	case !byName.IsZero():
		return "Devices reach this hub by dhis2.local (last " + byName.Local().Format("2 Jan 15:04") + ")."
	case !byIP.IsZero():
		return "Devices reach this hub only by its IP address: ask for a DHCP reservation on the router, so the address stays the same."
	}
	return "No other device has opened this hub yet."
}

func (u *UI) handleReset(w http.ResponseWriter, r *http.Request) {
	confirm := r.FormValue("confirm")
	sess := u.H.Sessions.FromCookie(r)
	if r.Method != http.MethodPost || sess == nil || (confirm != "RESET" && confirm != "RESET AND DISCARD") {
		http.Redirect(w, r, "/hub/ui?msg=Type+RESET+to+confirm", http.StatusFound)
		return
	}
	if err := u.Reset(sess.Username, confirm == "RESET AND DISCARD"); err != nil {
		http.Redirect(w, r, "/hub/ui?msg="+template.URLQueryEscaper("Reset refused: "+err.Error()), http.StatusFound)
		return
	}
	core.ClearSessionCookie(w, u.H.Cfg.TLS)
	http.Redirect(w, r, "/hub/setup", http.StatusFound)
}

// handleTakeOver moves the hub's housekeeping to a token of the logged-in staff member,
// who confirms with their password (docs/DEPLOYMENT.md, "The hub's own account").
func (u *UI) handleTakeOver(w http.ResponseWriter, r *http.Request) {
	sess := u.H.Sessions.FromCookie(r)
	if r.Method != http.MethodPost || sess == nil {
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
		return
	}
	msg := "The hub now uses your account for its own housekeeping"
	// Throttled like logins: a session left open on a shared PC must not let anyone
	// guess its user's central password here.
	// Counted apart from logins: wrong take-over passwords do not lock the user out.
	src, key := auth.Source(r), "takeover:"+sess.Username
	if f := u.Auth.Throttled(src, key); f != nil {
		msg = "Not taken over: too many wrong passwords; try again later"
	} else {
		err := u.H.TakeOverHubToken(r.Context(), sess.Username, r.FormValue("password"))
		u.Auth.Count(src, key, errors.Is(err, core.ErrWrongPassword))
		if err != nil {
			msg = "Not taken over: " + err.Error()
		}
	}
	http.Redirect(w, r, "/hub/ui?msg="+template.URLQueryEscaper(msg), http.StatusFound)
}

// handleMove switches the hub to central's new address after an unplanned move, typed
// in by a staff member with the code the national team gave for it (core.TypedMove).
func (u *UI) handleMove(w http.ResponseWriter, r *http.Request) {
	sess := u.H.Sessions.FromCookie(r)
	if r.Method != http.MethodPost || sess == nil {
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
		return
	}
	msg := "The hub now uses central at its new address"
	// Wrong codes are throttled like logins, and counted apart from them.
	src, key := auth.Source(r), "move:"+sess.Username
	if f := u.Auth.Throttled(src, key); f != nil {
		msg = "Not moved: too many wrong codes; try again later"
	} else {
		err := u.H.TypedMove(r.Context(), r.FormValue("central_url"), r.FormValue("code"))
		u.Auth.Count(src, key, errors.Is(err, core.ErrWrongCode))
		if err != nil {
			msg = "Not moved: " + err.Error()
		}
	}
	http.Redirect(w, r, "/hub/ui?msg="+template.URLQueryEscaper(msg), http.StatusFound)
}

// handleEndpoints lists the endpoints and parameters clients called (spec 13.1).
func (u *UI) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	counts, unsupp := u.H.ReqLog.Snapshot()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintln(w, "# Endpoints called through the hub (method path ?params [client]) count")
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%d\n", k, counts[k])
	}
	misses, total := u.H.ReqLog.OfflineMisses(1000)
	fmt.Fprintf(w, "\n# Reads answered \"needs connection\" offline, since the hub started (%d)\n", total)
	for _, m := range misses {
		fmt.Fprintf(w, "%s\t%d\n", m["request"], m["count"])
	}
	fmt.Fprintln(w, "\n# Parameters not supported offline")
	ukeys := make([]string, 0, len(unsupp))
	for k := range unsupp {
		ukeys = append(ukeys, k)
	}
	slices.Sort(ukeys)
	for _, k := range ukeys {
		fmt.Fprintf(w, "%s\t%d\n", k, unsupp[k])
	}
}
