// Package app wires the hub together: store, core, handlers, sync worker and upgrader.
// cmd/hub and the end-to-end tests both build the hub through New.
package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/dhis2/facility-hub/internal/devkey"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregateapi"
	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/hubtls"
	"github.com/dhis2/facility-hub/internal/mdns"
	"github.com/dhis2/facility-hub/internal/netsetup"
	"github.com/dhis2/facility-hub/internal/proxy"
	"github.com/dhis2/facility-hub/internal/stick"
	"github.com/dhis2/facility-hub/internal/store"
	hubsync "github.com/dhis2/facility-hub/internal/sync"
	"github.com/dhis2/facility-hub/internal/trackerapi"
	"github.com/dhis2/facility-hub/internal/ui"
	"github.com/dhis2/facility-hub/internal/upgrade"
)

type App struct {
	Hub      *core.Hub
	Proxy    *proxy.Proxy
	Handler  http.Handler
	Worker   *hubsync.Worker
	Upgrader *upgrade.Upgrader
	Stick    *stick.Stick
	// Net manages the network (Pi image, network: networkmanager); nil otherwise.
	Net *netsetup.Manager
	// MDNS answers dhis2.local (mdns: true, Windows); nil otherwise.
	MDNS      *mdns.Responder
	StorePath string
	ui        *ui.UI
}

// EnsureIdentity gives a fresh store a hub ID and puts it in setup mode (spec 6.2 step 1).
// A key file that cannot be read is not a fresh one: a new ID there would make a set-up
// hub another hub, in setup mode (review R-45).
func EnsureIdentity(s *store.Store) error {
	for key, fresh := range map[string]func() string{
		core.MetaHubID: func() string { return "hub-" + core.NewUID() },
		core.MetaMode:  func() string { return core.ModeSetup },
	} {
		v, err := s.MetaErr(key)
		if err != nil {
			return fmt.Errorf("reading the hub's %s: %w", key, err)
		}
		if v == "" {
			if err := s.SetMeta(key, fresh()); err != nil {
				return err
			}
		}
	}
	return nil
}

// New opens the store in cfg.DataDir and builds the hub. shutdown is called by the
// upgrader before it re-execs (it must stop serving; New's caller owns the server).
func New(cfg config.Config, version string, healthFail bool, shutdown func()) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	// The device key first: what follows reads and writes the secrets it seals.
	if err := devkey.Start(cfg.DataDir, cfg.TPM); err != nil {
		return nil, err
	}
	storePath := filepath.Join(cfg.DataDir, "hub.db")
	// An upgrade cut short is finished or undone before the store is opened.
	if err := upgrade.Recover(cfg.DataDir, storePath, version); err != nil {
		return nil, err
	}
	// A restore from the USB stick chosen on the setup page goes in place now
	// (docs/RECOVERY.md, C2).
	if err := stick.Finish(cfg.DataDir, storePath); err != nil {
		slog.Error("restoring from the USB stick", "err", err)
	}
	st, err := store.Open(storePath)
	if err != nil {
		return nil, err
	}
	st.NewDataFile = stick.RestorePending(cfg.DataDir) // a restored hub's cache is pulled again
	if err := EnsureIdentity(st); err != nil {
		st.Close()
		return nil, err
	}
	h := core.New(cfg, version, st)
	if upgrade.Unconfirmed(cfg.DataDir, version) {
		h.FreezeLocalWrites() // until AfterBoot's health check passes
	}
	h.Monitor.OnChange(func(online, forced bool) {
		if !forced { // a simulated cut is no outage
			h.NoteConnectivity(online)
		}
	})
	if cfg.LogRequests {
		h.ReqLog.Open(cfg.DataDir)
	}
	a := &auth.Auth{H: h}
	px := &proxy.Proxy{H: h, Auth: a}
	a.AfterWebLogin = px.ReplayAfterLogin
	tr := &trackerapi.API{H: h, Auth: a, Proxy: px}
	ag := &aggregateapi.API{H: h, Auth: a, Proxy: px}
	worker := &hubsync.Worker{H: h}
	up := &upgrade.Upgrader{H: h, StorePath: storePath, HealthFail: healthFail,
		Shutdown: func() {
			if shutdown != nil {
				shutdown()
			}
			st.Close()
		},
		Drain: func(ctx context.Context) (int, error) {
			err := worker.Push(ctx)
			return st.CountOutbox(store.OutboxPending), err
		}}
	worker.AfterCycle = up.Check
	u := &ui.UI{H: h, Auth: a, Worker: worker}
	if err := ui.EnsureSetupToken(h); err != nil {
		return nil, fmt.Errorf("setup token: %w", err)
	}
	u.Restart = shutdown

	// The hub's own pages use a ServeMux. Everything else is dispatched by hand: a
	// ServeMux answers 301/307 for slash or path-cleaning mismatches, and the Android
	// SDK adopts any redirect's Location as its new server URL (FINDINGS.md).
	hubMux := http.NewServeMux()
	u.Register(hubMux)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.HostAllowed(r.Host) {
			http.Error(w, "unknown host name: open the hub by its address (hub_hosts)", http.StatusMisdirectedRequest)
			return
		}
		h.NoteReach(r.Host, r.RemoteAddr)
		if !cleanPath(r.URL.Path) {
			// "//h/api/x.json" or "/a/../api/x": the cache and the replay would read
			// such a path differently (review R-16). Central behind Tomcat answers 400
			// too; a redirect is no answer for Android (above).
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		// Apps call versioned paths (/api/42/tracker/...). Route them like the
		// unversioned ones; central accepts either.
		p := r.URL.Path
		if m := apiVersion.FindString(p); m != "" {
			canon := "/api/" + p[len(m):]
			if canon == "/api/auth/login" || canon == "/api/tracker" || strings.HasPrefix(canon, "/api/tracker/") ||
				(strings.HasPrefix(canon, "/api/trackedEntityAttributes/") && isGeneratePath(canon)) || canon == "/api/ping" ||
				aggregateapi.IsAggregatePath(canon) {
				r.URL.Path, r.URL.RawPath, p = canon, "", canon
			}
		}
		switch {
		case p == "/hub" || strings.HasPrefix(p, "/hub/"):
			hdr := w.Header()
			hdr.Set("X-Content-Type-Options", "nosniff")
			hdr.Set("X-Frame-Options", "DENY")
			hdr.Set("Referrer-Policy", "no-referrer")
			hdr.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; form-action 'self'")
			hubMux.ServeHTTP(w, r)
		case p == "/api/auth/login":
			a.HandleLogin(w, r)
		case p == "/dhis-web-commons-security/logout.action":
			a.HandleLogout(w, r)
		case p == "/api/tracker" || strings.HasPrefix(p, "/api/tracker/"):
			tr.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/trackedEntityAttributes/") && isGeneratePath(p):
			tr.ServeGenerated(w, r)
		case aggregateapi.IsAggregatePath(p):
			ag.ServeHTTP(w, r)
		case h.Mode() == core.ModeSetup && (p == "/" || p == "/index.html"):
			http.Redirect(w, r, "/hub/setup", http.StatusFound)
		default:
			px.ServeHTTP(w, r)
		}
	})
	a2 := &App{Hub: h, Proxy: px, Handler: logRequests(cfg, recoverer(countMisses(h, root))), Worker: worker, Upgrader: up, Stick: stick.New(h),
		StorePath: storePath, ui: u}
	u.ChooseUSB = a2.Stick.Choose
	u.Reset = a2.Reset
	if cfg.MDNS {
		a2.MDNS = &mdns.Responder{Name: hubtls.Name, Nets: mdns.InterfaceNets}
	}
	if cfg.Network == "networkmanager" {
		a2.SetNet(&netsetup.Manager{R: netsetup.ExecRunner{}, DataDir: cfg.DataDir, HubID: h.HubID(), CaptiveDir: cfg.CaptiveDir, CaptiveConf: cfg.CaptiveConf})
		h.SystemZone = netsetup.SetTimeZone
	}
	return a2, nil
}

// SetRestart sets how the hub restarts itself after staging a restore from the USB
// stick (main: replace the process). Without it, as in tests, it only stops serving.
func (a *App) SetRestart(fn func()) { a.ui.Restart = fn }

// SetNet hands the hub's network to m: the pages' network section and hub.txt on the
// USB stick.
func (a *App) SetNet(m *netsetup.Manager) {
	a.Net, a.ui.Net = m, m
	a.Stick.Info = func() string { return hubInfo(a.Hub.HubID(), m.Status()) }
}

// hubInfo is facility-hub/hub.txt: what anyone holding the box needs to reach the hub.
// The facility Wi-Fi's password is in it when the hub made it, not when an admin chose
// it (until 2026-10-27).
func hubInfo(id string, st netsetup.Status) string {
	addrs := "none"
	if len(st.Addresses) > 0 {
		addrs = strings.Join(st.Addresses, ", ")
	}
	s := "Facility hub " + id + "\nOpen: http://dhis2.local\nAddresses: " + addrs +
		"\nSetup Wi-Fi: " + st.SetupSSID + "\nSetup Wi-Fi password: " + st.SetupPassword + "\n"
	if st.Mode == "facility" {
		s += "Facility Wi-Fi: " + st.Name + "\n"
		if st.FacilityPassword != "" {
			s += "Facility Wi-Fi password: " + st.FacilityPassword + "\n"
		}
	}
	return s
}

// Start runs the connectivity monitor and sync worker until ctx ends.
func (a *App) Start(ctx context.Context) {
	if a.Net != nil {
		go a.Net.Run(ctx)
	}
	if a.MDNS != nil {
		go func() {
			if err := a.MDNS.Run(ctx); err != nil {
				slog.Error("mdns: cannot answer dhis2.local; devices use the PC's name or address", "err", err)
			}
		}()
	}
	go a.Hub.Monitor.Run(ctx, a.Hub.Central.BaseURL)
	go a.Worker.Run(ctx)
	go a.Stick.Run(ctx)
}

func (a *App) Close() error {
	a.Proxy.StopReplays()
	a.Hub.ReqLog.Close()
	return a.Hub.Store.Close()
}

// Reset pushes the outbox, wipes all data and returns to setup mode (spec 6.2). No
// sync cycle may run meanwhile: a pull committing after the wipe would leave rows
// sealed under the old data key. Any staff member may reset (no setup group), so
// changes central refused are reported first and wiped only with discard: the
// clients were told they were saved, and central holds them only masked.
// The hub's tokens on central, its users' included, and its folder on the USB stick
// go with it (review R-25).
func (a *App) Reset(by string, discard bool) error {
	return a.Worker.Exclusive(func() error { return reset(a.Hub, a.Worker, a.Stick, by, discard) })
}

func reset(h *core.Hub, w *hubsync.Worker, stk *stick.Stick, by string, discard bool) error {
	if h.Revoked() {
		// Its tokens are gone: nothing it holds can reach central any more.
		if n := h.Store.CountOutbox(store.OutboxPending) + h.Store.CountOutbox(store.OutboxRejected) +
			h.Store.CountOutbox(store.OutboxPartial); n > 0 && !discard {
			return fmt.Errorf("this hub's tokens were revoked: its %d unsent changes cannot be sent unless its registry entry on central is set back to active; type RESET AND DISCARD to reset it", n)
		}
		stk.Forget()
		return wipe(h)
	}
	if !h.Online() {
		return errors.New("the hub must be online to push its outbox before a reset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := w.Push(ctx); err != nil {
		return err
	}
	if n := h.Store.CountOutbox(store.OutboxPending); n > 0 {
		return fmt.Errorf("%d outbox entries are still pending", n)
	}
	if err := w.ReportIssues(ctx); err != nil {
		return fmt.Errorf("reporting refused changes to central: %w", err)
	}
	if is, _ := h.Store.Issues(true, 1); len(is) > 0 {
		return errors.New("refused changes are not yet reported to central")
	}
	if n := h.Store.CountOutbox(store.OutboxRejected) + h.Store.CountOutbox(store.OutboxPartial); n > 0 && !discard {
		return fmt.Errorf("%d changes central refused would be lost (central has them only with their values masked); type RESET AND DISCARD to reset anyway", n)
	}
	if rt := h.Runtime(); rt != nil {
		// Edit the entry, not replace it: the fields the Facility Hub app shows (its
		// tokens, for "Revoke all tokens") stay (G6, N23).
		if err := h.EditRegistry(ctx, h.HubAuth(), func(reg map[string]any) bool {
			reg["hubId"] = h.HubID()
			reg["facility"] = rt.Facility
			reg["status"] = "retired"
			reg["retiredAt"] = store.Now()
			reg["retiredBy"] = "reset"
			reg["retiredByUser"] = by
			return true
		}); err != nil {
			slog.Warn("reset: registry update", "err", err)
		}
	}
	h.DropTokens(ctx)
	stk.Forget()
	return wipe(h)
}

// wipe forgets all data and returns the hub to setup mode.
func wipe(h *core.Hub) error {
	h.Lock()
	if err := h.Store.Wipe(); err != nil {
		return err
	}
	if err := EnsureIdentity(h.Store); err != nil {
		return err
	}
	if err := ui.EnsureSetupToken(h); err != nil {
		slog.Error("reset: setup token", "err", err)
	}
	slog.Info("hub reset; back in setup mode", "hubId", h.HubID())
	return nil
}

// cleanPath reports whether p is what path.Clean makes of it, a trailing slash
// allowed.
func cleanPath(p string) bool {
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c == p
}

var apiVersion = regexp.MustCompile(`^/api/[0-9]{2}/`)

func isGeneratePath(p string) bool {
	p = strings.TrimSuffix(p, "/")
	return strings.HasSuffix(p, "/generate") || strings.HasSuffix(p, "/generateAndReserve") || strings.HasSuffix(p, "/requestValues")
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic", "path", r.URL.Path, "err", v)
				http.Error(w, "internal error", 500)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// countMisses counts the reads the hub answered "needs connection" (the header
// core.WebError sets), whichever part of the hub answered.
func countMisses(h *core.Hub, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.Method == http.MethodGet && w.Header().Get(core.NeedsConnectionHeader) != "" {
			h.ReqLog.Miss(r)
		}
	})
}

func logRequests(cfg config.Config, next http.Handler) http.Handler {
	if !cfg.LogRequests {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "path", core.Template(r.URL.Path), "params", core.ParamNames(r.URL.Query()), "client", core.ClientKind(r),
			"status", sw.status, "ms", time.Since(t).Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
