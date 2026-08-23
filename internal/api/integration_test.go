package api

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"axiom/internal/audit"
	"axiom/internal/config"
	"axiom/internal/jobs"
)

// testEnv wires a real mTLS listener in front of a real Server, using
// freshly generated certificates, so authentication/authorization are
// exercised through the actual TLS handshake rather than mocked.
type testEnv struct {
	t          *testing.T
	server     *httptest.Server
	ca         *testCA
	serverCert string
	serverKey  string
	dir        string
}

// testEnvOptions lets individual tests add extra actions/identities/flags
// to the base config before it is parsed by the real config.Load path.
type testEnvOptions struct {
	extraActionsYAML string
	healthAnonymous  bool
}

func newTestEnv(t *testing.T, opts *testEnvOptions) *testEnv {
	t.Helper()
	if opts == nil {
		opts = &testEnvOptions{}
	}
	dir := t.TempDir()
	ca := newTestCA(t)
	caFile := writeFile(t, dir, "ca.crt", ca.certPEM, 0o644)

	serverCertPEM, serverKeyPEM := ca.issue(t, "axiom-server", x509.ExtKeyUsageServerAuth, 2)
	serverCertFile := writeFile(t, dir, "server.crt", serverCertPEM, 0o644)
	serverKeyFile := writeFile(t, dir, "server.key", serverKeyPEM, 0o600)

	script := writeFile(t, dir, "deploy.sh", []byte("#!/bin/sh\nexit 0\n"), 0o750)

	healthBlock := ""
	if opts.healthAnonymous {
		healthBlock = "\n  health:\n    allow_anonymous: true\n"
	}

	yamlContent := `
agent:
  id: test-agent
  name: Test Agent
  listen:
    address: 127.0.0.1
    port: 18443
security:` + healthBlock + `
  mtls:
    ca_file: ` + caFile + `
    cert_file: ` + serverCertFile + `
    key_file: ` + serverKeyFile + `
actions:
  backend.deploy:
    command: ` + script + `
    timeout: 5s
    concurrency: shared
` + opts.extraActionsYAML + `
authorization:
  identities:
    ci-jenkins:
      actions:
        - backend.deploy
`
	auditPath := filepath.Join(dir, "audit.log")
	yamlContent += "\naudit:\n  path: " + auditPath + "\n"

	configPath := writeFile(t, dir, "config.yaml", []byte(yamlContent), 0o640)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	auditLogger, err := audit.Open(cfg.AuditLogPath, cfg.AgentID)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { auditLogger.Close() })

	jobMgr := jobs.NewManager(cfg, auditLogger, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	srv, err := NewServer(cfg, jobMgr, auditLogger, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	tlsConfig, err := srv.TLSConfig()
	if err != nil {
		t.Fatalf("TLSConfig: %v", err)
	}

	hts := httptest.NewUnstartedServer(srv.Handler())
	hts.TLS = tlsConfig
	hts.StartTLS()
	t.Cleanup(hts.Close)

	return &testEnv{t: t, server: hts, ca: ca, dir: dir}
}

func (e *testEnv) clientFor(certPEM, keyPEM []byte) *http.Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(e.ca.certPEM)

	tlsCfg := &tls.Config{RootCAs: pool}
	if certPEM != nil {
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			e.t.Fatalf("loading client keypair: %v", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   5 * time.Second,
	}
}

func TestIntegration_AuthorizedIdentityCanTriggerAllowedAction(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 10)
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Post(env.server.URL+"/v1/actions/backend.deploy", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
}

func TestIntegration_UnauthorizedIdentityIsRejected(t *testing.T) {
	env := newTestEnv(t, nil)
	// A valid client certificate signed by the trusted CA, but for an
	// identity that has no allowlist entry at all (default-deny).
	certPEM, keyPEM := env.ca.issue(t, "unknown-identity", x509.ExtKeyUsageClientAuth, 11)
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Post(env.server.URL+"/v1/actions/backend.deploy", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestIntegration_DisallowedActionForIdentityIsRejected(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{extraActionsYAML: `  frontend.deploy:
    command: /bin/true
    timeout: 5s
`})
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 12)
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Post(env.server.URL+"/v1/actions/frontend.deploy", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (ci-jenkins is not allowlisted for frontend.deploy)", resp.StatusCode)
	}
}

func TestIntegration_UntrustedCertificateIsRejected(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := selfSigned(t, "ci-jenkins")
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Post(env.server.URL+"/v1/actions/backend.deploy", "application/json", nil)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 or a TLS-level rejection", resp.StatusCode)
		}
		return
	}
	// A TLS handshake failure (connection reset, bad certificate, etc.) is
	// also an acceptable rejection outcome here.
}

func TestIntegration_NoCertificateIsRejectedForNonHealthRoute(t *testing.T) {
	env := newTestEnv(t, nil)
	client := env.clientFor(nil, nil)

	resp, err := client.Post(env.server.URL+"/v1/actions/backend.deploy", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestIntegration_HealthRequiresAuthByDefault(t *testing.T) {
	env := newTestEnv(t, nil)
	client := env.clientFor(nil, nil)

	resp, err := client.Get(env.server.URL + "/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (health must be authenticated by default)", resp.StatusCode)
	}
}

func TestIntegration_HealthAnonymousWhenExplicitlyAllowed(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{healthAnonymous: true})
	client := env.clientFor(nil, nil)

	resp, err := client.Get(env.server.URL + "/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("Status = %q", body.Status)
	}
}

func TestIntegration_NoArbitraryExecuteEndpoint(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 13)
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Post(env.server.URL+"/execute", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		t.Fatalf("status = %d: an arbitrary-execute-shaped endpoint must not succeed", resp.StatusCode)
	}
}

func TestIntegration_JobLifecycleAndLogs(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 14)
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Post(env.server.URL+"/v1/actions/backend.deploy", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	var triggerResp map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&triggerResp); err != nil {
		t.Fatalf("decoding trigger response: %v", err)
	}
	resp.Body.Close()
	jobID := triggerResp["job_id"]
	if jobID == "" {
		t.Fatalf("empty job_id in response")
	}

	deadline := time.Now().Add(5 * time.Second)
	var last jobResponse
	for time.Now().Before(deadline) {
		r, err := client.Get(env.server.URL + "/v1/jobs/" + jobID)
		if err != nil {
			t.Fatalf("GET job: %v", err)
		}
		_ = json.NewDecoder(r.Body).Decode(&last)
		r.Body.Close()
		if last.Status == "succeeded" || last.Status == "failed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if last.Status != "succeeded" {
		t.Fatalf("job status = %q, want succeeded", last.Status)
	}

	r, err := client.Get(env.server.URL + "/v1/jobs/" + jobID + "/logs")
	if err != nil {
		t.Fatalf("GET logs: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("logs status = %d", r.StatusCode)
	}
}

func TestIntegration_RejectsUnknownJSONFields(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 20)
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Post(env.server.URL+"/v1/actions/backend.deploy", "application/json",
		strings.NewReader(`{"parameters":{},"not_a_real_field":true}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown JSON field", resp.StatusCode)
	}
}

func TestIntegration_RejectsWrongContentType(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 21)
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Post(env.server.URL+"/v1/actions/backend.deploy", "text/plain",
		strings.NewReader(`{"parameters":{}}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 for a non-JSON Content-Type", resp.StatusCode)
	}
}

func TestIntegration_RejectsInvalidActionNameInPath(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 22)
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Post(env.server.URL+"/v1/actions/"+url.PathEscape("../etc/passwd"), "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 400 or 404 for a malformed action name", resp.StatusCode)
	}
}

func TestIntegration_RejectsMalformedJobID(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 23)
	client := env.clientFor(certPEM, keyPEM)

	resp, err := client.Get(env.server.URL + "/v1/jobs/not-a-real-job-id")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a malformed job_id", resp.StatusCode)
	}
}

func TestIntegration_RejectsTooManyParameters(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 24)
	client := env.clientFor(certPEM, keyPEM)

	var sb strings.Builder
	sb.WriteString(`{"parameters":{`)
	for i := 0; i < 100; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"p%d":"x"`, i)
	}
	sb.WriteString("}}")

	resp, err := client.Post(env.server.URL+"/v1/actions/backend.deploy", "application/json", strings.NewReader(sb.String()))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for exceeding the per-request parameter count cap", resp.StatusCode)
	}
}

func TestIntegration_WrongMethodIsRejected(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 25)
	client := env.clientFor(certPEM, keyPEM)

	req, err := http.NewRequest(http.MethodDelete, env.server.URL+"/v1/actions/backend.deploy", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}
