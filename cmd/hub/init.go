package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/dhis2/facility-hub/internal/config"
	"github.com/dhis2/facility-hub/internal/hubtls"
)

// initResult is what the Windows installer needs from `hub init`.
type initResult struct {
	Config    string // hub.yaml
	CA        string // the authority to add to the PC's trusted roots
	HTTPSPort int
	HTTPPort  int
}

// initData prepares a data folder for a hub on a PC (the Windows installer): hub.yaml
// with free ports and nothing country-specific, and the hub's certificate authority, so
// the installer can add it to the PC's trusted roots. An existing hub.yaml and
// authority are kept: a reinstall finds the same hub.
func initData(dir string, perUser bool, free func(int) bool) (initResult, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return initResult{}, err
	}
	r := initResult{Config: filepath.Join(dir, "hub.yaml"), CA: filepath.Join(dir, "ca.crt")}
	if _, err := os.Stat(r.Config); errors.Is(err, os.ErrNotExist) {
		r.HTTPSPort, r.HTTPPort = config.ChoosePorts(free)
		install := "for the whole PC (a Windows service)"
		if perUser {
			install = "for one Windows user (runs while they are logged in)"
		}
		text := fmt.Sprintf(`# The hub on this PC, installed %s. Written once by "hub init"; nothing
# country-specific: central's address is given at set-up, on this PC.
listen: ":%d"
http_listen: ":%d"
data_dir: %s
usb_dir: auto
mdns: true
setup_local_only: true
`, install, r.HTTPSPort, r.HTTPPort, dir)
		if err := os.WriteFile(r.Config, []byte(text), 0o600); err != nil {
			return r, err
		}
	} else if err != nil {
		return r, err
	}
	c, err := config.Load(r.Config)
	if err != nil {
		return r, err
	}
	r.HTTPSPort, r.HTTPPort = portOf(c.Listen), portOf(c.HTTPListen)
	if _, err := hubtls.Load(dir, c.HubHosts); err != nil {
		return r, err
	}
	return r, nil
}

func portOf(listen string) int {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(port)
	return p
}

// hubInit is `hub init -data DIR [-per-user]`: prints the settings file, the
// authority and the ports, one per line, for the installer.
func hubInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dir := fs.String("data", "", "the hub's data folder")
	perUser := fs.Bool("per-user", false, "installed for one Windows user")
	fs.Parse(args)
	if *dir == "" {
		return errors.New("need -data")
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	r, err := initData(abs, *perUser, config.PortFree)
	if err != nil {
		return err
	}
	fmt.Printf("config=%s\nca=%s\nhttps_port=%d\nhttp_port=%d\n", r.Config, r.CA, r.HTTPSPort, r.HTTPPort)
	return nil
}
