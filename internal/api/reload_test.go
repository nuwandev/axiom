//go:build unix

package api

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nuwandev/axiom/internal/config"
)

func (e *testEnv) triggerStatus(client *http.Client) int {
	e.t.Helper()
	resp, err := client.Post(e.server.URL+"/v1/actions/sample.action", "application/json", nil)
	if err != nil {
		e.t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// editConfig rewrites the config file and loads it through the real
// config.Load path, exactly as a SIGHUP would.
func (e *testEnv) editConfig(old, replacement string) *config.Config {
	e.t.Helper()
	data, err := os.ReadFile(e.configPath)
	if err != nil {
		e.t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, old) {
		e.t.Fatalf("config has no %q to replace", old)
	}
	text = strings.Replace(text, old, replacement, 1)
	if err := os.WriteFile(e.configPath, []byte(text), 0o640); err != nil {
		e.t.Fatal(err)
	}
	next, err := config.Load(e.configPath)
	if err != nil {
		e.t.Fatalf("config.Load: %v", err)
	}
	return next
}

func TestReload_NewIdentityTakesEffectWithoutRestart(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-new", x509.ExtKeyUsageClientAuth, 20)
	client := env.clientFor(certPEM, keyPEM)

	if got := env.triggerStatus(client); got != http.StatusForbidden {
		t.Fatalf("before reload: status = %d, want 403", got)
	}

	next := env.editConfig("    ci-jenkins:", "    ci-new:\n      actions:\n        - sample.action\n    ci-jenkins:")
	if err := env.srv.Reload(next); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if got := env.triggerStatus(client); got != http.StatusAccepted {
		t.Fatalf("after reload: status = %d, want 202", got)
	}
}

func TestReload_RemovedIdentityLosesAccess(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 21)
	client := env.clientFor(certPEM, keyPEM)

	if got := env.triggerStatus(client); got != http.StatusAccepted {
		t.Fatalf("before reload: status = %d, want 202", got)
	}

	next := env.editConfig("    ci-jenkins:\n", "    other:\n")
	if err := env.srv.Reload(next); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if got := env.triggerStatus(client); got != http.StatusForbidden {
		t.Fatalf("after reload: status = %d, want 403", got)
	}
}

func TestReload_RefusesRestartOnlyChangeAndAppliesNothing(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-new", x509.ExtKeyUsageClientAuth, 22)
	client := env.clientFor(certPEM, keyPEM)

	// The identity addition is reloadable, the port change is not: the whole
	// reload must be refused, so the new identity must still be denied.
	next := env.editConfig("port: 18443", "port: 18444")
	next.Identities["ci-new"] = &config.Identity{Actions: []string{"sample.action"}}

	err := env.srv.Reload(next)
	if err == nil || !strings.Contains(err.Error(), "ListenPort") {
		t.Fatalf("Reload error = %v, want one naming ListenPort", err)
	}
	if got := env.triggerStatus(client); got != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a refused reload must change nothing", got)
	}
}

func TestReload_BadCertificateKeepsPreviousState(t *testing.T) {
	env := newTestEnv(t, nil)
	certPEM, keyPEM := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 23)
	client := env.clientFor(certPEM, keyPEM)

	next := *env.cfg
	next.CertFile = env.dir + "/does-not-exist.crt"
	if err := env.srv.Reload(&next); err == nil {
		t.Fatal("Reload with a missing certificate succeeded")
	}
	if got := env.triggerStatus(client); got != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: a failed reload must keep serving", got)
	}
}

func TestReload_CARotationAppliesToExistingAndNewConnections(t *testing.T) {
	env := newTestEnv(t, nil)
	oldCert, oldKey := env.ca.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 24)
	oldClient := env.clientFor(oldCert, oldKey)
	if got := env.triggerStatus(oldClient); got != http.StatusAccepted {
		t.Fatalf("before rotation: status = %d, want 202", got)
	}

	newCA := newTestCA(t)
	newCAFile := writeFile(t, env.dir, "ca-new.crt", newCA.certPEM, 0o644)
	next := *env.cfg
	next.CAFile = newCAFile
	if err := env.srv.Reload(&next); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// The old client's connection is still open, so this request is stopped
	// by the per-request check rather than by a new handshake.
	if got := env.triggerStatus(oldClient); got != http.StatusUnauthorized {
		t.Fatalf("existing connection after rotation: status = %d, want 401", got)
	}

	// A fresh connection with a certificate from the old CA must not get in.
	fresh := env.clientFor(oldCert, oldKey)
	fresh.Transport.(*http.Transport).DisableKeepAlives = true
	if resp, err := fresh.Post(env.server.URL+"/v1/actions/sample.action", "application/json", nil); err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusAccepted {
			t.Fatal("old-CA client still accepted on a new connection")
		}
	}

	// A client from the new CA is accepted.
	newCert, newKey := newCA.issue(t, "ci-jenkins", x509.ExtKeyUsageClientAuth, 25)
	pair, err := tls.X509KeyPair(newCert, newKey)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(env.ca.certPEM)
	newClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{pair}}},
		Timeout:   5 * time.Second,
	}
	if got := env.triggerStatus(newClient); got != http.StatusAccepted {
		t.Fatalf("new-CA client: status = %d, want 202", got)
	}
}
