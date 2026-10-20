package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// service is `hub service run|install|uninstall` (docs/superpowers/specs/2026-10-20-
// windows-service-design.md): the Windows installer installs and removes the service;
// the service manager runs `hub service run`.
func service(args []string) error {
	a, err := parseServiceArgs(args)
	if err != nil {
		return err
	}
	switch a.action {
	case "run":
		return svc.Run(serviceName, &handler{config: a.config})
	case "install":
		return installService(a.exe, a.config, !a.noStart)
	case "stop":
		return stopService()
	default:
		return uninstallService()
	}
}

// handler answers the service manager: stop and shutdown end the hub as SIGTERM does.
type handler struct{ config string }

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveCtx(ctx, h.config) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				slog.Error("hub stopped", "err", err)
				return true, 1 // a failure: the recovery action restarts the service
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(25 * time.Second):
				}
				return false, 0
			}
		}
	}
}

func installService(exe, cfg string, start bool) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return fmt.Errorf("the service %s is already installed", serviceName)
	}
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:      serviceDisplay,
		Description:      "Keeps DHIS2 working at this facility while the internet is down.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
		ServiceStartName: serviceAccount, // a virtual account: no password to manage
		// The virtual account is the service's own SID: it needs one (unrestricted).
		SidType: windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, "service", "run", "-config", cfg)
	if err != nil {
		return err
	}
	defer s.Close()
	// Restart after a crash, and after an upgrade's planned exit (upgrade.ExitRestart).
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, 24*60*60); err != nil {
		return err
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return err
	}
	if !start {
		return nil
	}
	return s.Start()
}

// stopService stops the service and waits until it has (up to a minute): the installer
// replaces hub.exe next, which Windows refuses while it runs.
func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return nil // not installed
	}
	defer s.Close()
	return waitStopped(s)
}

func waitStopped(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State != svc.Stopped && st.State != svc.StopPending {
		if st, err = s.Control(svc.Stop); err != nil {
			return err
		}
	}
	for i := 0; i < 120 && st.State != svc.Stopped; i++ {
		time.Sleep(500 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return err
		}
	}
	if st.State != svc.Stopped {
		return fmt.Errorf("the service did not stop within a minute")
	}
	return nil
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return nil // not installed
	}
	defer s.Close()
	waitStopped(s)
	return s.Delete()
}
