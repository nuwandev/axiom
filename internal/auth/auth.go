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
		// TLS 1.2 minimum: modern Jenkins/JVM and Go HTTP clients all
		// support 1.3, but 1.2 is kept as a floor for compatibility with
		// slightly older internal clients. Only forward-secret AEAD cipher
		// suites are offered for a 1.2 handshake; a 1.3 handshake ignores
		// CipherSuites entirely and Go's fixed, already-strong 1.3 suite
		// set is used instead — TLS 1.3 ciphers are intentionally not
		// configurable by design of the standard library.
		MinVersion:   tls.VersionTLS12,
		CipherSuites: strongTLS12CipherSuites,
		CurvePreferences: []tls.CurveID{
			tls.X25519,
			tls.CurveP256,
		},
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequestClientCert,
	}, nil
}

// strongTLS12CipherSuites restricts a negotiated TLS 1.2 connection to
// ECDHE (forward secret) key exchange with AEAD (AES-GCM/ChaCha20-Poly1305)
// bulk ciphers — no CBC-mode, no RSA key exchange, no RC4/3DES.
var strongTLS12CipherSuites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
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
