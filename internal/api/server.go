// Package api implements the agent's HTTP surface: POST /v1/actions/{action},
// GET /v1/jobs/{job_id}, GET /v1/jobs/{job_id}/logs, and GET /health.
package api

import (
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"net/http"
	"os"

	"axiom/internal/audit"
	"axiom/internal/auth"
	"axiom/internal/config"
	"axiom/internal/jobs"
)

// Version is the agent's build version, surfaced on /health.
var Version = "dev"

// Server holds everything the HTTP handlers need.
type Server struct {
	cfg    *config.Config
	jobs   *jobs.Manager
	audit  *audit.Logger
	logger *slog.Logger
	caPool *x509.CertPool
}

// NewServer builds a Server. caFile is read again here (independent of the
// TLS listener's own CA use) so client certificates can be chain-verified
// per request inside HTTP middleware.
func NewServer(cfg *config.Config, jobMgr *jobs.Manager, auditLogger *audit.Logger, logger *slog.Logger) (*Server, error) {
	caPEM, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)

	return &Server{
		cfg:    cfg,
		jobs:   jobMgr,
		audit:  auditLogger,
		logger: logger,
		caPool: pool,
	}, nil
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
	return auth.TLSConfig(s.cfg.CertFile, s.cfg.KeyFile, s.cfg.CAFile)
}
