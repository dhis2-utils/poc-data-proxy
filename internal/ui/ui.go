// Package ui serves the hub's own pages and API (spec 12): /hub/status, the setup flow
// (spec 6.2) and the admin page. No patient data is shown beyond UIDs.
package ui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/store"
	hubsync "github.com/dhis2/facility-hub/internal/sync"
	"github.com/dhis2/facility-hub/internal/tracker"
)

//go:embed templates/*.html
var templateFS embed.FS

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"short": func(s string) string {
		if len(s) > 19 {
			return s[:19]
		}
		return s
	},
	"json": func(v any) string { b, _ := json.Marshal(v); return string(b) },
}).ParseFS(templateFS, "templates/*.html"))

type UI struct {
	H      *core.Hub
	Auth   *auth.Auth
	Worker *hubsync.Worker
	// Reset wipes the store and returns to setup mode (set by main).
	Reset func() error
	setup setupFlow
}

func (u *UI) Register(mux *http.ServeMux) {
	mux.HandleFunc("/hub/status", u.handleStatus)
	mux.HandleFunc("/hub/setup", u.handleSetup)
	mux.HandleFunc("/hub/login", u.handleLogin)
	mux.HandleFunc("/hub/logout", u.handleLogout)
	mux.HandleFunc("/hub/ui", u.admin(u.handleAdmin))
	mux.HandleFunc("/hub/ui/sync", u.admin(u.handleSyncNow))
	mux.HandleFunc("/hub/ui/uplink", u.admin(u.handleUplink))
	mux.HandleFunc("/hub/ui/reset", u.admin(u.handleReset))
	mux.HandleFunc("/hub/ui/endpoints", u.admin(u.handleEndpoints))
	// The hub's CA certificate, for installing on devices (public; no key behind it).
	mux.HandleFunc("/hub/ca.crt", func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join(u.H.Cfg.DataDir, "ca.crt"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Write(b)
	})
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

// admin requires a hub session of a member of setup_user_group (spec 12).
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
		if !u.H.Access(sess.UserID).InGroup(u.H.Cfg.SetupUserGroup) {
			w.WriteHeader(http.StatusForbidden)
			u.render(w, "login", map[string]any{"Error": "Only members of the hub setup user group can open this page."})
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
	if o := r.Header.Get("Origin"); o != "" {
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
		u.render(w, "login", nil)
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
			PATExpires: us.PATExpires, Credential: cred, NeedsLogin: us.NeedsLogin, InFacility: h.Access(us.ID).InFacility(h.Facility())})
	}
	type issueRow struct {
		*store.Issue
		User    string
		Message string
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
		irows = append(irows, issueRow{Issue: is, User: names[is.UserID], Message: tracker.Redact(msg)})
	}
	rt := h.Runtime()
	var programs []string
	for _, id := range h.Catalog().ProgramIDs() {
		programs = append(programs, id+" "+h.Catalog().Programs[id].Name)
	}
	upgrade := h.Store.Meta(core.MetaUpgrade)
	u.render(w, "admin", map[string]any{
		"Status": StatusBody(h), "Heartbeat": hubsync.Status(h), "Runtime": rt, "Programs": programs,
		"Outbox": outbox, "Issues": irows, "Users": rows, "Names": names, "Objects": h.Index.Len(),
		"Forced": h.Monitor.ForcedOffline(), "Upgrade": upgrade, "Central": h.Central.BaseURL(),
		"ServerVersion": h.ServerVersion(), "Msg": r.URL.Query().Get("msg"),
	})
}

func (u *UI) handleSyncNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/hub/ui", http.StatusFound)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := u.Worker.Cycle(ctx); err != nil {
			slog.Warn("sync now", "err", err)
		}
	}()
	http.Redirect(w, r, "/hub/ui?msg=Sync+started", http.StatusFound)
}

// handleUplink lets a demo or test "cut the uplink" without touching the network.
func (u *UI) handleUplink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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

func (u *UI) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.FormValue("confirm") != "RESET" {
		http.Redirect(w, r, "/hub/ui?msg=Type+RESET+to+confirm", http.StatusFound)
		return
	}
	if err := u.Reset(); err != nil {
		http.Redirect(w, r, "/hub/ui?msg="+template.URLQueryEscaper("Reset refused: "+err.Error()), http.StatusFound)
		return
	}
	core.ClearSessionCookie(w, u.H.Cfg.TLS)
	http.Redirect(w, r, "/hub/setup", http.StatusFound)
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
