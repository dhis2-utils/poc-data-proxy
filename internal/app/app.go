// Package app wires the hub together: store, core, handlers, sync worker and upgrader.
// cmd/hub and the end-to-end tests both build the hub through New.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dhis2/facility-hub/internal/aggregateapi"
	"github.com/dhis2/facility-hub/internal/auth"
	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/core"
	"github.com/dhis2/facility-hub/internal/proxy"
	"github.com/dhis2/facility-hub/internal/stick"
	"github.com/dhis2/facility-hub/internal/store"
	hubsync "github.com/dhis2/facility-hub/internal/sync"
	"github.com/dhis2/facility-hub/internal/trackerapi"
	"github.com/dhis2/facility-hub/internal/ui"
	"github.com/dhis2/facility-hub/internal/upgrade"
)

type App struct {
	Hub       *core.Hub
	Handler   http.Handler
	Worker    *hubsync.Worker
	Upgrader  *upgrade.Upgrader
	Stick     *stick.Stick
	StorePath string
}

// EnsureIdentity gives a fresh store a hub ID and puts it in setup mode (spec 6.2 step 1).
func EnsureIdentity(s *store.Store) {
	if s.Meta(core.MetaHubID) == "" {
		s.SetMeta(core.MetaHubID, "hub-"+core.NewUID())
	}
	if s.Meta(core.MetaMode) == "" {
		s.SetMeta(core.MetaMode, core.ModeSetup)
	}
}

// New opens the store in cfg.DataDir and builds the hub. shutdown is called by the
// upgrader before it re-execs (it must stop serving; New's caller owns the server).
func New(cfg config.Config, version string, healthFail bool, shutdown func()) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	storePath := filepath.Join(cfg.DataDir, "hub.db")
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
	EnsureIdentity(st)
	h := core.New(cfg, version, st)
	h.Monitor.OnChangeInOrder(h.NoteConnectivity)
	if cfg.LogRequests {
		h.ReqLog.Open(cfg.DataDir)
	}
	a := &auth.Auth{H: h}
	px := &proxy.Proxy{H: h, Auth: a}
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
	u.Reset = func(by string, discard bool) error { return Reset(h, worker, by, discard) }
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
	return &App{Hub: h, Handler: logRequests(cfg, recoverer(root)), Worker: worker, Upgrader: up, Stick: stick.New(h),
		StorePath: storePath}, nil
}

// Start runs the connectivity monitor and sync worker until ctx ends.
func (a *App) Start(ctx context.Context) {
	go a.Hub.Monitor.Run(ctx, a.Hub.Central.BaseURL)
	go a.Worker.Run(ctx)
	go a.Stick.Run(ctx)
}

func (a *App) Close() error { return a.Hub.Store.Close() }

// Reset pushes the outbox, wipes all data and returns to setup mode (spec 6.2). No
// sync cycle may run meanwhile: a pull committing after the wipe would leave rows
// sealed under the old data key. Any staff member may reset (no setup group), so
// changes central refused are reported first and wiped only with discard: the
// clients were told they were saved, and central holds them only masked.
func Reset(h *core.Hub, w *hubsync.Worker, by string, discard bool) error {
	return w.Exclusive(func() error { return reset(h, w, by, discard) })
}

func reset(h *core.Hub, w *hubsync.Worker, by string, discard bool) error {
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
		reg := map[string]any{"hubId": h.HubID(), "facility": rt.Facility, "status": "retired", "retiredAt": store.Now(),
			"retiredBy": "reset", "retiredByUser": by}
		if err := h.Central.JSON(ctx, "PUT", "/api/dataStore/facility-hub-registry/"+h.HubID(), reg, h.HubAuth(), nil); err != nil {
			slog.Warn("reset: registry update", "err", err)
		}
		if rt.HubTokenUID != "" {
			h.Central.JSON(ctx, "DELETE", "/api/apiToken/"+rt.HubTokenUID, nil, h.HubAuth(), nil)
		}
	}
	h.Lock()
	if err := h.Store.Wipe(); err != nil {
		return err
	}
	EnsureIdentity(h.Store)
	if err := ui.EnsureSetupToken(h); err != nil {
		slog.Error("reset: setup token", "err", err)
	}
	slog.Info("hub reset; back in setup mode", "hubId", h.HubID())
	return nil
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

func logRequests(cfg config.Config, next http.Handler) http.Handler {
	if !cfg.LogRequests {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "path", core.Template(r.URL.Path), "query", core.SafeQuery(r.URL.Query()), "client", core.ClientKind(r),
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
