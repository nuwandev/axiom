//go:build windows

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"golang.org/x/sys/windows/svc"

	"github.com/nuwandev/axiom/internal/api"
	"github.com/nuwandev/axiom/internal/config"
)

const serviceName = "axiom"

func defaultConfigPath() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "Axiom", "config.yaml")
}

func run() error {
	configPath := flag.String("config", defaultConfigPath(), "path to agent configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	installService := flag.Bool("install-service", false, "register the Axiom Windows service and exit")
	uninstallService := flag.Bool("uninstall-service", false, "remove the Axiom Windows service and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("axiom %s (%s)\n", api.Version, commit)
		return nil
	}
	if *installService {
		return installWindowsService(*configPath)
	}
	if *uninstallService {
		return uninstallWindowsService()
	}

	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("determining service context: %w", err)
	}

	if isService {
		// Enter the SCM dispatcher first; config.Load happens inside the
		// service handler, after StartPending is reported, so a bad config
		// surfaces as a clean stop with a specific exit code and an event
		// log entry rather than an SCM "did not respond in time" error.
		return runAsService(*configPath)
	}

	// Console / development mode: identical lifecycle to the unix build.
	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return serve(ctx, cfg, logger)
}
