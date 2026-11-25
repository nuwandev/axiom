// Package api implements the agent's HTTP surface: POST /v1/actions/{action},
// GET /v1/jobs/{job_id}, GET /v1/jobs/{job_id}/logs, and GET /health.
package api

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/auth"
	"github.com/nuwandev/axiom/internal/config"
	"github.com/nuwandev/axiom/internal/jobs"
)

// Version is the agent's build version, surfaced on /health.
var Version = "dev"

// Server holds everything the HTTP handlers need.
type Server struct {
	// cfg is replaced as a whole by Reload. Handlers must load it once per
	// request and use that snapshot, so one request never sees two configs.
	cfg      atomic.Pointer[config.Config]
	reloadMu sync.Mutex
	tls      *auth.TLSState
	jobs     *jobs.Manager
	audit    *audit.Logger
	logger   *slog.Logger
}

// NewServer builds a Server and loads its TLS material. The CA pool lives in
// the same holder the listener uses, so per-request client-certificate
// verification and the handshake can never disagree about which CA is current.
func NewServer(cfg *config.Config, jobMgr *jobs.Manager, auditLogger *audit.Logger, logger *slog.Logger) (*Server, error) {
	tlsState, err := auth.NewTLSState(cfg.CertFile, cfg.KeyFile, cfg.CAFile, cfg.HealthAllowAnonymous)
	if err != nil {
		return nil, err
	}

	s := &Server{
		tls:    tlsState,
		jobs:   jobMgr,
		audit:  auditLogger,
		logger: logger,
	}
	s.cfg.Store(cfg)
	return s, nil
}

// Reload applies next to the running server. Only identities and the
// certificate files are reloadable; if anything else differs, nothing is
// applied and the error names what needs a restart. The new certificate
// material is loaded and validated before anything is swapped, so any error
// leaves the server exactly as it was.
func (s *Server) Reload(next *config.Config) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	current := s.cfg.Load()
	if changed := current.NonReloadableChanges(next); len(changed) > 0 {
		return fmt.Errorf("configuration changes need a restart, nothing was reloaded: %s", strings.Join(changed, ", "))
	}

	material, err := auth.LoadMaterial(next.CertFile, next.KeyFile, next.CAFile)
	if err != nil {
		return err
	}
	s.tls.Replace(material)
	s.cfg.Store(next)
	return nil
}

// Handler returns the fully wired HTTP handler (routes + auth middleware).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/actions/{action}", s.handleTriggerAction)
	mux.HandleFunc("GET /v1/jobs/{job_id}", s.handleGetJob)
	mux.HandleFunc("GET /v1/jobs/{job_id}/logs", s.handleGetJobLogs)
	mux.HandleFunc("GET /health", s.handleHealth)
	return s.withIdentity(mux)
}

// TLSConfig builds the server's mTLS listener configuration.
func (s *Server) TLSConfig() (*tls.Config, error) {
	return s.tls.Config(), nil
}
