// Command axiom runs the Axiom agent: an authenticated HTTPS API that lets
// trusted CI/CD systems trigger predefined, server-local actions.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/nuwandev/axiom/internal/api"
	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/config"
	"github.com/nuwandev/axiom/internal/executor"
	"github.com/nuwandev/axiom/internal/jobs"
)

// commit is set via -ldflags at release build time (see scripts/build-release.sh).
var commit = "unknown"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "axiom:", err)
		os.Exit(1)
	}
}

// serve builds the agent and runs it until ctx is cancelled, then shuts the
// HTTP listener down gracefully. It is the single application lifecycle
// shared by every entrypoint: the unix binary drives ctx from POSIX
// signals, and the Windows service adapter drives it from the Service
// Control Manager. Neither duplicates any of the startup or shutdown logic
// below.
func serve(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	if err := executor.CheckPlatformPrerequisites(); err != nil {
		return fmt.Errorf("platform prerequisites: %w", err)
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
		Addr:      addr,
		Handler:   server.Handler(),
		TLSConfig: tlsConfig,
		// Bounds on a single connection's lifecycle. WriteTimeout is safe
		// to keep short: every response Axiom sends is small JSON written
		// immediately (POST /v1/actions/{action} returns 202 before the
		// action even starts running — action execution itself is
		// unaffected by this, since it continues in the background against
		// its own per-action config.Action.Timeout regardless of what
		// happens to the HTTP connection that triggered it).
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 * 1024,
	}

	listener, err := tls.Listen("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

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
