// Command hub is the facility hub: a cache and relay between DHIS2 Capture clients on
// a facility LAN and the central DHIS2 server. See SPEC-facility-hub-poc.md.
//
//	hub [serve] -config hub.yaml      run the hub
//	hub migrate -db path              run store migrations on a file (used by upgrades)
//	hub keygen -out prefix            create a release signing key pair
//	hub release -key k -dir d -version v linux/arm64=path ...   publish a signed release
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dhis2/facility-hub/internal/app"
	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/store"
	"github.com/dhis2/facility-hub/internal/upgrade"
)

// Set with -ldflags "-X main.version=0.2.0 -X main.healthFail=true".
var (
	version    = "0.1.0"
	healthFail = "false"
)

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
	case "release":
		err = release(args)
	case "version":
		fmt.Println(version)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hub:", err)
		os.Exit(1)
	}
}

func migrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	db := fs.String("db", "", "store file")
	fs.Parse(args)
	s, err := store.Open(*db)
	if err != nil {
		return err
	}
	return s.Close()
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/hub/hub.yaml", "image defaults file (spec 6.1)")
	fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var srv *http.Server
	shutdown := func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if srv != nil {
			srv.Shutdown(sctx)
		}
	}
	a, err := app.New(cfg, version, healthFail == "true", shutdown)
	if err != nil {
		return err
	}
	defer a.Close()
	if dk := upgrade.HandedOverDK(); dk != nil {
		if err := a.Hub.Unlock(dk); err != nil {
			slog.Error("unlock with handed-over key", "err", err)
		}
	}
	srv = &http.Server{Addr: cfg.Listen, Handler: a.Handler, ReadHeaderTimeout: 30 * time.Second}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	scheme := "http"
	if cfg.TLS {
		scheme = "https"
		if srv.TLSConfig, err = selfSignedTLS(cfg.DataDir); err != nil {
			return err
		}
	}
	a.Start(ctx)
	go func() {
		<-ctx.Done()
		shutdown()
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
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
