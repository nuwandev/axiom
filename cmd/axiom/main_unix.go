//go:build unix

package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nuwandev/axiom/internal/api"
	"github.com/nuwandev/axiom/internal/config"
)

func run() error {
	configPath := flag.String("config", "/etc/axiom/config.yaml", "path to agent configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("axiom %s (%s)\n", api.Version, commit)
		return nil
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return serve(ctx, cfg, logger)
}
