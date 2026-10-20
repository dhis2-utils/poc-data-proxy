package main

import (
	"errors"
	"flag"
	"io"
	"time"
)

// The Windows service (service_windows.go): `hub service run|install|uninstall`.
const (
	serviceName    = "DHIS2FacilityHub"
	serviceDisplay = "DHIS2 Facility Hub"
	serviceAccount = `NT SERVICE\DHIS2FacilityHub`
)

type serviceArgs struct {
	action, config, exe string
	noStart             bool // install only: the installer sets the data folder's rights first
}

func parseServiceArgs(args []string) (serviceArgs, error) {
	var a serviceArgs
	if len(args) == 0 {
		return a, errors.New("hub service run|install|stop|uninstall")
	}
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := fs.String("config", "", "hub.yaml")
	exe := fs.String("exe", "", "the hub.exe the service runs")
	noStart := fs.Bool("nostart", false, "install without starting")
	if err := fs.Parse(args[1:]); err != nil {
		return a, err
	}
	switch args[0] {
	case "run":
		if *cfg == "" {
			return a, errors.New("hub service run needs -config")
		}
	case "install":
		if *cfg == "" || *exe == "" {
			return a, errors.New("hub service install needs -config and -exe")
		}
	case "uninstall", "stop":
	default:
		return a, errors.New("hub service run|install|stop|uninstall")
	}
	return serviceArgs{action: args[0], config: *cfg, exe: *exe, noStart: *noStart}, nil
}

// waitReporting waits up to limit for the hub to stop, calling report with a rising
// checkpoint at once and then every interval: the Windows service manager takes a stop
// that reports no progress within its wait hint as hung.
func waitReporting(done <-chan error, limit, every time.Duration, report func(checkpoint uint32)) bool {
	t := time.NewTicker(every)
	defer t.Stop()
	end := time.After(limit)
	for cp := uint32(1); ; cp++ {
		report(cp)
		select {
		case <-done:
			return true
		case <-end:
			return false
		case <-t.C:
		}
	}
}
