package api

import (
	"context"
	"net/http"

	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/auth"
)

type contextKey int

const identityContextKey contextKey = iota

// withIdentity performs client-certificate verification for every request.
// It is the single place that decides "authenticated or not": /health may
// be configured to tolerate no identity, but every other route always
// requires one, and a certificate that fails chain verification is always
// rejected regardless of path.
func (s *Server) withIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anonymousHealth := r.URL.Path == "/health" && s.cfg.Load().HealthAllowAnonymous

		if r.TLS == nil {
			if anonymousHealth {
				next.ServeHTTP(w, r)
				return
			}
			s.rejectUnauthenticated(w, r, "no TLS connection state")
			return
		}

		id, err := s.tls.Verify(r.TLS.PeerCertificates)
		if err != nil {
			if anonymousHealth {
				next.ServeHTTP(w, r)
				return
			}
			s.rejectUnauthenticated(w, r, err.Error())
			return
		}

		ctx := context.WithValue(r.Context(), identityContextKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) rejectUnauthenticated(w http.ResponseWriter, r *http.Request, reason string) {
	s.writeRejection(audit.Record{
		Event:  audit.EventRejected,
		Action: r.URL.Path,
		Reason: "unauthenticated: " + reason,
	})
	writeError(w, http.StatusUnauthorized, "unauthenticated")
}

func identityFromContext(ctx context.Context) *auth.Identity {
	id, _ := ctx.Value(identityContextKey).(*auth.Identity)
	return id
}
