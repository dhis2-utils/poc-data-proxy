// Command hub is the facility hub: a cache and relay between DHIS2 Capture clients on
// a facility LAN and the central DHIS2 server. See SPEC-facility-hub-poc.md.
//
//	hub [serve] -config hub.yaml      run the hub
//	hub migrate -db path              run store migrations on a copy (used by upgrades; data key on HUB_DK_FD)
//	hub keygen -out prefix            create a release signing key pair
//	hub release -key k -dir d -version v linux/arm64=path ...   publish a signed release
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/dhis2/facility-hub/internal/devkey"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // central's time zone, also on images without zone files

	"github.com/dhis2/facility-hub/internal/app"
	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/hubtls"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/ui"
	"github.com/dhis2/facility-hub/internal/upgrade"
)

// Set with -ldflags "-X main.version=0.2.0 -X main.healthFail=true". startFail makes
// serve exit at every start (test builds: a release that cannot start, adversarial G2).
var (
	version    = "0.0.0-dev" // builds set it: VERSION, or VERSION-ci.<run> on CI
	healthFail = "false"
	startFail  = "false"
)

// maxHeaderBytes caps a request's line and headers (review R-06).
const maxHeaderBytes = 64 << 10

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "migrate":
		err = migrate(args)
	case "keygen":
		err = keygen(args)
	case "init":
		err = hubInit(args)
	case "service":
		err = service(args)
	case "release":
		err = release(args)
	case "version":
		if len(args) > 0 {
			fmt.Println(versionAgainst(version, args[0])) // the Windows installer's question
		} else {
			fmt.Println(version)
		}
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hub:", err)
		os.Exit(1)
	}
}

// versionAgainst says whether this hub's version is "newer", "same" or "older" than
// other, by release precedence (upgrade.CompareVersions).
func versionAgainst(own, other string) string {
	return [...]string{"older", "same", "newer"}[upgrade.CompareVersions(own, other)+1]
}

func migrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	db := fs.String("db", "", "store file")
	fs.Parse(args)
	s, err := store.Open(*db)
	if err != nil {
		return err
	}
	defer s.Close()
	// With the data key (handed over by an upgrade), migrate the data file too.
	if dk := upgrade.HandedOverDK(); dk != nil {
		return s.SetDK(dk)
	}
	return nil
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/hub/hub.yaml", "image defaults file (spec 6.1)")
	fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveCtx(ctx, *cfgPath)
}

// serveCtx runs the hub until ctx ends (a signal, or the Windows service manager).
func serveCtx(ctx context.Context, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	logToFile(cfg.DataDir)
	for _, k := range cfg.Unknown {
		slog.Warn("hub.yaml: unknown key, ignored (a typo?)", "at", k)
	}
	// A swapped-in release that keeps exiting before its health check is rolled back.
	if err := upgrade.CountStart(cfg.DataDir, filepath.Join(cfg.DataDir, "hub.db"), version); err != nil {
		return err
	}
	if startFail == "true" {
		return errors.New("this release cannot start (test build)")
	}
	setMemoryLimit()

	var srv *http.Server
	shutdown := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if srv != nil {
			srv.Shutdown(sctx)
		}
	}
	upgrade.HandoverDir = cfg.DataDir // the Windows upgrade's key handover
	a, err := app.New(cfg, version, healthFail == "true", shutdown)
	if err != nil {
		return err
	}
	a.SetRestart(restartFunc(shutdown, a.Close, os.Executable, upgrade.Reexec, os.Exit))
	defer a.Close()
	if dk := upgrade.HandedOverDK(); dk != nil {
		if err := a.Hub.Unlock(dk); err != nil {
			slog.Error("unlock with handed-over key", "err", err)
		}
	}
	// No WriteTimeout: app downloads over weak facility Wi-Fi can take minutes.
	// MaxHeaderBytes: Go's default of 1 MB per request lets anyone hold a lot of
	// memory with long paths; real clients send a few kB.
	srv = &http.Server{Addr: cfg.Listen, Handler: a.Handler, ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout: 10 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: maxHeaderBytes}
	ln, err := listen(cfg.Listen, 15*time.Second)
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	scheme := "http"
	// The start page on plain HTTP (docs/DEPLOYMENT.md, "Reaching the hub securely").
	startPage := func() error {
		if cfg.HTTPListen == "" {
			return nil
		}
		page := &ui.Start{DataDir: cfg.DataDir, HTTPSPort: port, Allowed: a.Hub.HostAllowed, Reached: a.Hub.NoteReach}
		if a.Net != nil {
			page.Captive = a.Net.Captive
		}
		start := &http.Server{Handler: page, MaxHeaderBytes: maxHeaderBytes,
			ReadHeaderTimeout: 30 * time.Second, ReadTimeout: time.Minute, IdleTimeout: 2 * time.Minute}
		hln, err := listen(cfg.HTTPListen, 15*time.Second)
		if err != nil {
			return err
		}
		go func() { <-ctx.Done(); start.Close() }()
		go func() {
			if err := start.Serve(hln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("start page", "err", err)
			}
		}()
		return nil
	}
	if cfg.TLS {
		scheme = "https"
		certs, err := hubtls.Load(cfg.DataDir, cfg.HubHosts)
		if errors.Is(err, devkey.ErrNoKey) {
			// A disk without its TPM: its keys cannot be opened here. The start page says
			// so, and nothing else is served (devkey.Problem).
			slog.Error("not serving: " + devkey.Problem())
			ln.Close()
			if err := startPage(); err != nil {
				return err
			}
			return retryLater(ctx, devkeyRetry)
		}
		if err != nil {
			return err
		}
		srv.TLSConfig, srv.Handler = certs.Config(), hubtls.HSTS(a.Handler)
		go certs.Run(ctx.Done())
		if err := startPage(); err != nil {
			return err
		}
	}
	a.Start(ctx)
	go func() {
		<-ctx.Done()
		shutdown()
	}()
	go a.Upgrader.AfterBoot(ctx, fmt.Sprintf("%s://127.0.0.1:%s/hub/status", scheme, port))
	slog.Info("hub listening", "addr", ln.Addr().String(), "tls", cfg.TLS, "version", version, "hubId", a.Hub.HubID(),
		"mode", a.Hub.Mode(), "central", a.Hub.Central.BaseURL())
	if cfg.TLS {
		err = srv.ServeTLS(ln, "", "")
	} else {
		err = srv.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		<-ctx.Done()
		return nil
	}
	return err
}
