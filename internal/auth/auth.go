// Package auth builds the agent's mTLS server configuration and extracts a
// verified client identity from a TLS connection.
package auth

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// TLSConfig loads the server certificate/key and the internal CA pool used
// to verify client certificates, and returns a tls.Config for the agent's
// listener.
//
// ClientAuth is intentionally tls.RequestClientCert rather than
// tls.RequireAndVerifyClientCert: the server always asks for a client
// certificate, but whether one is required is enforced per-request by the
// HTTP middleware (see Identity/Verify below). This is what allows a single
// listener to serve an optionally-anonymous /health endpoint while every
// other route still requires a verified client identity.
func TLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading server certificate/key: %w", err)
	}

	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no valid certificates found in CA file %q", caFile)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequestClientCert,
	}, nil
}

// Identity is a verified client identity, derived from a client
// certificate's Common Name after full chain verification against the
// agent's internal CA.
type Identity struct {
	CommonName string
}

// Verify validates the presented client certificate chain against caPool
// and returns the resulting identity. It returns an error if no certificate
// was presented, or if the presented certificate does not chain to a
// trusted root — an expired, revoked-by-absence, or otherwise untrusted
// certificate is always rejected regardless of which route was requested.
func Verify(peerCerts []*x509.Certificate, caPool *x509.CertPool) (*Identity, error) {
	if len(peerCerts) == 0 {
		return nil, fmt.Errorf("no client certificate presented")
	}

	leaf := peerCerts[0]
	intermediates := x509.NewCertPool()
	for _, c := range peerCerts[1:] {
		intermediates.AddCert(c)
	}

	opts := x509.VerifyOptions{
		Roots:         caPool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := leaf.Verify(opts); err != nil {
		return nil, fmt.Errorf("client certificate verification failed: %w", err)
	}

	if leaf.Subject.CommonName == "" {
		return nil, fmt.Errorf("client certificate has no Common Name to use as identity")
	}

	return &Identity{CommonName: leaf.Subject.CommonName}, nil
}
