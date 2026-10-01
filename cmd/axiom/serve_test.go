package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/nuwandev/axiom/internal/config"
)

// serve_test.go exercises the shared serve(ctx) lifecycle on both platforms.
// It builds *config.Config directly so it does not depend on config.Load's
// platform-specific file-security checks.

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving port: %v", err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func writeCerts(t *testing.T, dir string) (caFile, certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "serve-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	caFile = filepath.Join(dir, "ca.crt")
	certFile = filepath.Join(dir, "server.crt")
	keyFile = filepath.Join(dir, "server.key")
	for f, b := range map[string][]byte{caFile: certPEM, certFile: certPEM, keyFile: keyPEM} {
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return caFile, certFile, keyFile
}

func testConfig(t *testing.T) *config.Config {
	dir := t.TempDir()
	ca, cert, key := writeCerts(t, dir)
	return &config.Config{
		AgentID:              "serve-test",
		AgentName:            "Serve Test",
		ListenAddress:        "127.0.0.1",
		ListenPort:           freePort(t),
		CAFile:               ca,
		CertFile:             cert,
		KeyFile:              key,
		HealthAllowAnonymous: true,
		AuditLogPath:         filepath.Join(dir, "audit.log"),
		MaxOutputBytes:       config.DefaultOutputBytes,
		MaxJobHistory:        config.DefaultMaxJobHistory,
		Actions:              map[string]*config.Action{},
		Identities:           map[string]*config.Identity{},
	}
}

func TestServe_StartsAndShutsDownOnContextCancel(t *testing.T) {
	cfg := testConfig(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serve(ctx, cfg, logger) }()

	// Drive a real mTLS request end-to-end: this proves the platform's TLS
	// listener + router + handler stack actually serves (the Windows-relevant
	// integration check — the auth/authz semantics themselves are
	// platform-neutral and covered by internal/api's Linux integration suite).
	caPEM, _ := os.ReadFile(cfg.CAFile)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	hc := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		Timeout:   2 * time.Second,
	}
	url := "https://" + net.JoinHostPort(cfg.ListenAddress, strconv.Itoa(cfg.ListenPort)) + "/health"

	var lastErr error
	served := false
	for i := 0; i < 100; i++ {
		resp, err := hc.Get(url)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("/health status = %d, body %s", resp.StatusCode, b)
			}
			served = true
			break
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	if !served {
		cancel()
		<-errc
		t.Fatalf("serve did not answer an mTLS /health request: %v", lastErr)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serve returned error on clean shutdown: %v", err)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("serve did not return after context cancellation")
	}
}

func TestServe_FailsClosedOnBadCertPath(t *testing.T) {
	cfg := testConfig(t)
	cfg.CAFile = filepath.Join(t.TempDir(), "does-not-exist.crt")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := serve(context.Background(), cfg, logger)
	if err == nil {
		t.Fatal("expected serve to fail when the CA file is missing")
	}
}
