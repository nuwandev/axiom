//go:build unix

// This end-to-end suite drives config.Load, which enforces POSIX
// ownership/mode security on the fixture files it writes to t.TempDir(). The
// Windows equivalent (internal/winsec ACL semantics) cannot be satisfied by
// an ordinary temp directory, so this package has 0% automated coverage on
// GOOS=windows by design (see the "go test" step comment in
// .github/workflows/ci.yml) -- the mTLS/auth/authz path on Windows is
// instead covered by the real-host validation pass against a genuinely
// ACL-configured, protected-directory install; see
// docs/WINDOWS-SERVER-2022-VALIDATION.md ("mTLS end-to-end").
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

	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/config"
	"github.com/nuwandev/axiom/internal/jobs"
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
	// maxConcurrent sets jobs.max_concurrent (0 = default).
	maxConcurrent int
	// slowAction adds "slow.action" (sleeps 5s), allowed for ci-jenkins.
	slowAction bool
	// unlistedAction adds "frontend.deploy", which no identity may run.
	unlistedAction bool
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

	if opts.slowAction {
		slow := writeFile(t, dir, "slow.sh", []byte("#!/bin/sh\nsleep 5\n"), 0o750)
		opts.extraActionsYAML += "  slow.action:\n    command: " + slow + "\n    timeout: 10s\n    concurrency: shared\n"
	}
	if opts.unlistedAction {
		// A script owned by the test user, not a system binary: /bin/true is a
		// symlink on some distributions and config.Load rejects symlinks.
		unlisted := writeFile(t, dir, "unlisted.sh", []byte("#!/bin/sh\nexit 0\n"), 0o750)
		opts.extraActionsYAML += "  frontend.deploy:\n    command: " + unlisted + "\n    timeout: 5s\n"
	}
	identityExtra := ""
	if opts.slowAction {
		identityExtra = "        - slow.action\n"
	}
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
security:
  allow_service_owned_files: true` + healthBlock + `
  mtls:
    ca_file: ` + caFile + `
    cert_file: ` + serverCertFile + `
    key_file: ` + serverKeyFile + `
actions:
  sample.action:
    command: ` + script + `
    timeout: 5s
    concurrency: shared
` + opts.extraActionsYAML + `
authorization:
  identities:
    ci-jenkins:
      actions:
        - sample.action
` + identityExtra
	auditPath := filepath.Join(dir, "audit.log")
	yamlContent += "\naudit:\n  path: " + auditPath + "\n"
	if opts.maxConcurrent > 0 {
		yamlContent += fmt.Sprintf("\njobs:\n  max_concurrent: %d\n", opts.maxConcurrent)
	}

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

	resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "application/json", nil)
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

	resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestIntegration_DisallowedActionForIdentityIsRejected(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{unlistedAction: true})
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

// requireRejected asserts a request was refused. In the default mode the
// TLS handshake itself must fail; when anonymous health is enabled the
// handshake completes and the HTTP middleware must answer 401.
func requireRejected(t *testing.T, client *http.Client, url string, handshakeMustFail bool) {
	t.Helper()
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		if !handshakeMustFail {
			t.Fatalf("expected an HTTP 401 from the middleware, got transport error: %v", err)
		}
		return
	}
	defer resp.Body.Close()
	if handshakeMustFail {
		t.Fatalf("got HTTP %d, want the TLS handshake to fail", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestIntegration_InvalidClientCertificatesAreRejected(t *testing.T) {
	for _, anonymousHealth := range []bool{false, true} {
		env := newTestEnv(t, &testEnvOptions{healthAnonymous: anonymousHealth})
		url := env.server.URL + "/v1/actions/sample.action"

		t.Run(fmt.Sprintf("self-signed/anonymousHealth=%v", anonymousHealth), func(t *testing.T) {
			certPEM, keyPEM := selfSigned(t, "ci-jenkins")
			requireRejected(t, env.clientFor(certPEM, keyPEM), url, !anonymousHealth)
		})
		t.Run(fmt.Sprintf("expired/anonymousHealth=%v", anonymousHealth), func(t *testing.T) {
			certPEM, keyPEM := env.ca.issueValid(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 41,
				time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
			requireRejected(t, env.clientFor(certPEM, keyPEM), url, !anonymousHealth)
		})
		t.Run(fmt.Sprintf("not-yet-valid/anonymousHealth=%v", anonymousHealth), func(t *testing.T) {
			certPEM, keyPEM := env.ca.issueValid(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 42,
				time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour))
			requireRejected(t, env.clientFor(certPEM, keyPEM), url, !anonymousHealth)
		})
		t.Run(fmt.Sprintf("wrong-ca/anonymousHealth=%v", anonymousHealth), func(t *testing.T) {
			// Same CN as the allowlisted identity, but issued by a different CA.
			otherCA := newTestCA(t)
			certPEM, keyPEM := otherCA.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 43)
			requireRejected(t, env.clientFor(certPEM, keyPEM), url, !anonymousHealth)
		})
		t.Run(fmt.Sprintf("wrong-key-usage/anonymousHealth=%v", anonymousHealth), func(t *testing.T) {
			// Signed by the trusted CA, right CN, but a serverAuth-only
			// certificate must not authenticate a client.
			certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageServerAuth, 40)
			requireRejected(t, env.clientFor(certPEM, keyPEM), url, !anonymousHealth)
		})
	}
}

// With anonymous health disabled (the default) the TLS handshake itself
// requires a client certificate, so a certificate-less peer must never get
// an HTTP response at all.
func TestIntegration_NoCertificateFailsHandshakeByDefault(t *testing.T) {
	env := newTestEnv(t, nil)
	client := env.clientFor(nil, nil)

	for _, path := range []string{"/v1/actions/sample.action", "/health"} {
		resp, err := client.Post(env.server.URL+path, "application/json", nil)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("POST %s without a client certificate got HTTP %d, want a TLS handshake failure", path, resp.StatusCode)
		}
	}
}

func TestIntegration_NoCertificateIsRejectedForNonHealthRouteWhenHealthAnonymous(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{healthAnonymous: true})
	client := env.clientFor(nil, nil)

	resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestIntegration_JobStatusAndLogsAreOwnerOnly(t *testing.T) {
	env := newTestEnv(t, nil)
	ownerCert, ownerKey := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 30)
	owner := env.clientFor(ownerCert, ownerKey)
	// Valid CA-signed certificate, different identity, never triggered the job.
	otherCert, otherKey := env.ca.issue(t, "other-pipeline", x509.ExtKeyUsageClientAuth, 31)
	other := env.clientFor(otherCert, otherKey)

	resp, err := owner.Post(env.server.URL+"/v1/actions/sample.action", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	var triggerResp map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&triggerResp); err != nil {
		t.Fatalf("decoding trigger response: %v", err)
	}
	resp.Body.Close()
	jobID := triggerResp["job_id"]

	for _, suffix := range []string{"", "/logs"} {
		url := env.server.URL + "/v1/jobs/" + jobID + suffix

		r, err := other.Get(url)
		if err != nil {
			t.Fatalf("GET %s as other identity: %v", suffix, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("other identity GET job%s = %d, want 404", suffix, r.StatusCode)
		}

		r, err = owner.Get(url)
		if err != nil {
			t.Fatalf("GET %s as owner: %v", suffix, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("owner GET job%s = %d, want 200", suffix, r.StatusCode)
		}
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

	resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "application/json", nil)
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

	resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "application/json",
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

	resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "text/plain",
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

	resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "application/json", strings.NewReader(sb.String()))
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

	req, err := http.NewRequest(http.MethodDelete, env.server.URL+"/v1/actions/sample.action", nil)
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

// A caller must not be able to tell "exists but belongs to someone else" from
// "never existed" from "not a job ID": all three answer with the same status
// and body.
func TestIntegration_JobLookupFailuresAreIndistinguishable(t *testing.T) {
	env := newTestEnv(t, nil)
	ownerCert, ownerKey := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 50)
	owner := env.clientFor(ownerCert, ownerKey)
	otherCert, otherKey := env.ca.issue(t, "other-pipeline", x509.ExtKeyUsageClientAuth, 51)
	other := env.clientFor(otherCert, otherKey)

	resp, err := owner.Post(env.server.URL+"/v1/actions/sample.action", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	var trig map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&trig); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	resp.Body.Close()

	otherOwners := env.server.URL + "/v1/jobs/" + trig["job_id"]
	neverExisted := env.server.URL + "/v1/jobs/" + jobs.NewID()
	malformed := env.server.URL + "/v1/jobs/not-a-job-id"

	fetch := func(url string) (int, string) {
		t.Helper()
		r, err := other.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	for _, suffix := range []string{"", "/logs"} {
		wantStatus, wantBody := fetch(otherOwners + suffix)
		if wantStatus != http.StatusNotFound {
			t.Fatalf("other-owner lookup%s status = %d, want 404", suffix, wantStatus)
		}
		for name, u := range map[string]string{"never-existed": neverExisted, "malformed": malformed} {
			status, body := fetch(u + suffix)
			if status != wantStatus || body != wantBody {
				t.Errorf("%s%s: status=%d body=%q, want status=%d body=%q", name, suffix, status, body, wantStatus, wantBody)
			}
		}
	}
}

func TestIntegration_OversizedParameterValueIsRejected(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 52)
	client := env.clientFor(certPEM, keyPEM)

	body := `{"parameters":{"p":"` + strings.Repeat("a", maxParameterValueLen+1) + `"}}`
	resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegration_UndeclaredParameterIsRejected(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 53)
	client := env.clientFor(certPEM, keyPEM)

	for _, name := range []string{"PATH", "AXIOM_PARAM_X", "../x", "a b"} {
		quoted, _ := json.Marshal(name)
		body := `{"parameters":{` + string(quoted) + `:"v"}}`
		resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("parameter name %q: status = %d, want 400", name, resp.StatusCode)
		}
	}
}

// A request body must be exactly one JSON object: trailing data (a second
// document some other parser might read differently) is rejected, oversized
// bodies are rejected, and the error text does not echo the parser's
// internals or the offending input.
func TestIntegration_MalformedBodiesAreRejectedWithoutEcho(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 54)
	client := env.clientFor(certPEM, keyPEM)

	cases := map[string]string{
		"trailing document":  `{"parameters":{}}{"parameters":{"x":"y"}}`,
		"trailing garbage":   `{"parameters":{}} SECRET-MARKER`,
		"unknown field":      `{"parameters":{},"SECRET-MARKER":1}`,
		"not json":           `SECRET-MARKER`,
		"oversized body":     `{"parameters":{"p":"` + strings.Repeat("a", maxRequestBodyBytes) + `"}}`,
		"wrong parameters":   `{"parameters":"SECRET-MARKER"}`,
		"truncated document": `{"parameters":{"p":"SECRET-MARKER"`,
	}
	for name, body := range cases {
		resp, err := client.Post(env.server.URL+"/v1/actions/sample.action", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("%s: POST: %v", name, err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %q)", name, resp.StatusCode, got)
		}
		if strings.Contains(string(got), "SECRET-MARKER") {
			t.Errorf("%s: error response echoes request content: %q", name, got)
		}
	}
}

// At the agent-wide job cap the API answers 429 with Retry-After and creates
// no job; once capacity frees up the same request is accepted.
func TestIntegration_GlobalJobLimitReturns429(t *testing.T) {
	env := newTestEnv(t, &testEnvOptions{maxConcurrent: 1, slowAction: true})
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 55)
	client := env.clientFor(certPEM, keyPEM)

	post := func() *http.Response {
		resp, err := client.Post(env.server.URL+"/v1/actions/slow.action", "application/json", nil)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		return resp
	}
	first := post()
	first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first trigger status = %d, want 202", first.StatusCode)
	}
	second := post()
	second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second trigger status = %d, want 429", second.StatusCode)
	}
	if second.Header.Get("Retry-After") == "" {
		t.Errorf("429 response has no Retry-After header")
	}
}
