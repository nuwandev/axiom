//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/nuwandev/axiom/internal/config"
)

// Win32 exit codes the service reports to the SCM so a failed start is
// legible in `sc query` / the Services console without needing file access.
const (
	exitOK          = 0
	exitConfigError = 13 // ERROR_INVALID_DATA — configuration failed to load
	exitServeError  = 1  // ERROR_INVALID_FUNCTION — the agent stopped unexpectedly
)

// runAsService runs the agent under the Service Control Manager. config.Load
// deliberately happens inside the handler (after StartPending is reported),
// not here, so a bad configuration surfaces as a clean Stopped state with a
// specific exit code and an event-log entry rather than an SCM timeout.
func runAsService(configPath string) error {
	logPath := filepath.Join(filepath.Dir(configPath), "logs", "axiom.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		reportServiceError(fmt.Sprintf("opening service log %q: %v (is the layout installed?)", logPath, err))
		return fmt.Errorf("opening service log: %w", err)
	}
	defer logFile.Close()

	h := &serviceHandler{
		configPath: configPath,
		logger:     slog.New(slog.NewJSONHandler(logFile, nil)),
	}
	if err := svc.Run(serviceName, h); err != nil {
		reportServiceError(fmt.Sprintf("service dispatcher failed: %v", err))
		return fmt.Errorf("service run: %w", err)
	}
	return h.exitErr
}

type serviceHandler struct {
	configPath string
	logger     *slog.Logger
	exitErr    error
}

func (h *serviceHandler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	s <- svc.Status{State: svc.StartPending, WaitHint: 15000}

	cfg, err := config.Load(h.configPath)
	if err != nil {
		h.exitErr = err
		h.logger.Error("configuration failed to load; service will not start",
			"config", h.configPath, "err", err)
		reportServiceError(fmt.Sprintf("configuration %q failed to load: %v", h.configPath, err))
		return false, exitConfigError
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- serve(ctx, cfg, h.logger) }()

	s <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// WaitHint must comfortably exceed serve()'s own 30s HTTP
				// drain so SCM does not misread a graceful shutdown as a hang.
				s <- svc.Status{State: svc.StopPending, WaitHint: 35000}
				cancel()
				if err := <-done; err != nil {
					// The stop was requested and honoured; a drain-timeout
					// error is worth logging but not a failure exit code
					// (which could prompt SCM recovery on a routine stop).
					h.logger.Warn("graceful shutdown did not complete cleanly", "err", err)
					h.exitErr = err
				}
				return false, exitOK
			default:
				h.logger.Warn("unexpected service control request", "cmd", uint32(c.Cmd))
			}
		case err := <-done:
			h.exitErr = err
			if err != nil {
				h.logger.Error("serve exited unexpectedly", "err", err)
				reportServiceError(fmt.Sprintf("serve exited: %v", err))
				return false, exitServeError
			}
			return false, exitOK
		}
	}
}

// reportServiceError writes a best-effort entry to the Windows Application
// event log (and stderr) so a failed "sc start" is diagnosable without file
// access. The event source is registered by installWindowsService; if it is
// not registered yet, eventlog.Open falls back to a generic source.
func reportServiceError(msg string) {
	fmt.Fprintln(os.Stderr, "axiom:", msg)
	el, err := eventlog.Open(serviceName)
	if err != nil {
		return
	}
	defer el.Close()
	_ = el.Error(1, msg)
}

func installWindowsService(configPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating executable: %w", err)
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return fmt.Errorf("resolving config path: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to service manager (run elevated): %w", err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return fmt.Errorf("service %q is already installed", serviceName)
	}

	s, err := m.CreateService(serviceName, exe, mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		DisplayName:      "Axiom automation agent",
		Description:      "Authenticated mTLS API that triggers predefined, server-local PowerShell capability scripts. No arbitrary command execution.",
		ServiceStartName: `NT SERVICE\` + serviceName, // virtual service account, no password
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, "-config", abs)
	if err != nil {
		return fmt.Errorf("creating service: %w", err)
	}
	defer s.Close()

	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	}, 86400); err != nil {
		return fmt.Errorf("setting recovery actions: %w", err)
	}

	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		// Non-fatal: the service still runs; only structured event-log
		// categorisation is missing.
		fmt.Fprintf(os.Stderr, "axiom: warning: registering event log source: %v\n", err)
	}

	fmt.Printf("service %q installed (account: NT SERVICE\\%s, config: %s)\n", serviceName, serviceName, abs)
	return nil
}

func uninstallWindowsService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to service manager (run elevated): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer s.Close()

	if status, err := s.Query(); err == nil && status.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stopping service: %w", err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if status, err := s.Query(); err == nil && status.State == svc.Stopped {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("deleting service: %w", err)
	}
	_ = eventlog.Remove(serviceName)
	fmt.Printf("service %q removed\n", serviceName)
	return nil
}
