// Command axiom runs the Axiom agent: an authenticated HTTPS API that lets
// trusted CI/CD systems trigger predefined, server-local actions.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"axiom/internal/api"
	"axiom/internal/audit"
	"axiom/internal/config"
	"axiom/internal/jobs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "axiom:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "/etc/axiom/config.yaml", "path to agent configuration file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	auditLogger, err := audit.Open(cfg.AuditLogPath, cfg.AgentID)
	if err != nil {
		return fmt.Errorf("opening audit log: %w", err)
	}
	defer auditLogger.Close()

	jobMgr := jobs.NewManager(cfg, auditLogger, logger)

	server, err := api.NewServer(cfg, jobMgr, auditLogger, logger)
	if err != nil {
		return fmt.Errorf("building server: %w", err)
	}

	tlsConfig, err := server.TLSConfig()
	if err != nil {
		return fmt.Errorf("building TLS config: %w", err)
	}

	addr := net.JoinHostPort(cfg.ListenAddress, fmt.Sprintf("%d", cfg.ListenPort))
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.Handler(),
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := tls.Listen("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("axiom listening", "agent", cfg.AgentID, "address", addr)
		serveErr <- httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server error: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
	}

	return nil
}
