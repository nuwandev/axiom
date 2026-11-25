// Package auth builds the agent's mTLS server configuration and extracts a
// verified client identity from a TLS connection.
package auth

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// Material is the part of the mTLS setup that can be replaced while the agent
// runs: the server certificate and the CA pool client certificates are
// verified against.
type Material struct {
	cert   tls.Certificate
	caPool *x509.CertPool
	// crl is nil when no revocation list is configured.
	crl *revocationList
}

// LoadMaterial reads and validates the server certificate/key pair, the
// internal CA bundle and, when crlFile is not empty, the revocation list.
// Nothing is returned on any error, so a caller keeps whatever it already had.
func LoadMaterial(certFile, keyFile, caFile, crlFile string) (*Material, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading server certificate/key: %w", err)
	}

	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}
	cas := parseCertificates(caPEM)
	if len(cas) == 0 {
		return nil, fmt.Errorf("no valid certificates found in CA file %q", caFile)
	}
	pool := x509.NewCertPool()
	for _, ca := range cas {
		pool.AddCert(ca)
	}

	m := &Material{cert: cert, caPool: pool}
	if crlFile != "" {
		if m.crl, err = loadRevocationList(crlFile, cas, time.Now()); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// TLSState builds the agent's mTLS listener configuration and lets its
// certificate and CA be replaced atomically while connections are being
// accepted.
//
// By default the handshake itself requires and verifies a client
// certificate, so an unauthenticated peer never reaches the HTTP layer.
// Only when allowAnonymousClients is true (the operator opted in to an
// anonymous /health) is ClientAuth relaxed to tls.RequestClientCert, so one
// listener can serve /health without a certificate; the HTTP middleware (see
// Identity/Verify below) then still requires a verified identity on every
// other route. Verify runs per request in both modes.
type TLSState struct {
	allowAnonymousClients bool
	material              atomic.Pointer[Material]
}

// NewTLSState loads the initial material.
func NewTLSState(certFile, keyFile, caFile, crlFile string, allowAnonymousClients bool) (*TLSState, error) {
	m, err := LoadMaterial(certFile, keyFile, caFile, crlFile)
	if err != nil {
		return nil, err
	}
	s := &TLSState{allowAnonymousClients: allowAnonymousClients}
	s.material.Store(m)
	return s, nil
}

// Replace makes m the material used by every later handshake. Connections
// already established keep the server certificate they negotiated, but their
// client certificate is re-verified against the current CA on every request
// (see Verify), so a removed CA takes effect for them immediately.
func (s *TLSState) Replace(m *Material) { s.material.Store(m) }

// Verify checks a client certificate chain against the CA and revocation list
// currently in force and returns the identity it proves. It runs on every
// request, so a revocation or a rotated CA applies to open connections too.
func (s *TLSState) Verify(peerCerts []*x509.Certificate) (*Identity, error) {
	m := s.material.Load()
	id, err := Verify(peerCerts, m.caPool)
	if err != nil {
		return nil, err
	}
	if err := m.crl.check(peerCerts[0], time.Now()); err != nil {
		return nil, err
	}
	return id, nil
}

// Config returns the listener's tls.Config. Each handshake gets a copy that
// carries the material current at that moment.
func (s *TLSState) Config() *tls.Config {
	clientAuth := tls.RequireAndVerifyClientCert
	if s.allowAnonymousClients {
		clientAuth = tls.RequestClientCert
	}

	base := &tls.Config{
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
		ClientAuth: clientAuth,
	}

	withMaterial := func(m *Material) *tls.Config {
		c := base.Clone()
		c.Certificates = []tls.Certificate{m.cert}
		c.ClientCAs = m.caPool
		return c
	}

	cfg := withMaterial(s.material.Load())
	cfg.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return withMaterial(s.material.Load()), nil
	}
	return cfg
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
// trusted root — an expired or otherwise untrusted certificate is always
// rejected regardless of which route was requested. Revocation is not checked
// here; TLSState.Verify adds it.
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
