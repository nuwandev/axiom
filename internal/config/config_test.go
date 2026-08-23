package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFixture creates a file at dir/name with the given mode, owned by the
// current test process (which satisfies checkScriptSecurity's "owned by the
// Axiom service account" branch without requiring root).
func writeFixture(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatalf("writing fixture %s: %v", p, err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatalf("chmod fixture %s: %v", p, err)
	}
	return p
}

// testCertPEM/testKeyPEM are a single self-signed certificate/key pair,
// generated once, reused everywhere config_test.go needs "some valid PEM
// certificate/key material" — config.Load validates that files are
// well-formed X.509/PEM, not that ca_file/cert_file/key_file form a
// consistent chain (that deeper check happens at TLS-listener setup).
var testCertPEM, testKeyPEM = generateTestCertPEM()

func generateTestCertPEM() (certPEM, keyPEM string) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "axiom-config-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	priv := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return cert, priv
}

func baseYAML(dir, scriptPath, caFile, certFile, keyFile string) string {
	return `
agent:
  id: test-agent
  name: Test Agent
  listen:
    address: 0.0.0.0
    port: 8443

security:
  mtls:
    ca_file: ` + caFile + `
    cert_file: ` + certFile + `
    key_file: ` + keyFile + `

actions:
  backend.deploy:
    command: ` + scriptPath + `
    timeout: 5m
    concurrency: exclusive
    parameters:
      image_tag:
        type: string
        pattern: '^[a-zA-Z0-9._-]{1,128}$'
        required: true

authorization:
  identities:
    ci-jenkins:
      actions:
        - backend.deploy
`
}

func TestLoad_Valid(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\necho hi\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	yamlPath := writeFixture(t, dir, "config.yaml", baseYAML(dir, script, ca, cert, key), 0o640)

	cfg, err := Load(yamlPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AgentID != "test-agent" {
		t.Errorf("AgentID = %q", cfg.AgentID)
	}
	action, ok := cfg.Actions["backend.deploy"]
	if !ok {
		t.Fatalf("expected backend.deploy action")
	}
	if action.Concurrency != ConcurrencyExclusive {
		t.Errorf("Concurrency = %q", action.Concurrency)
	}
	id, ok := cfg.Identities["ci-jenkins"]
	if !ok || !id.IsAllowed("backend.deploy") {
		t.Errorf("expected ci-jenkins allowed for backend.deploy")
	}
	if id.IsAllowed("frontend.deploy") {
		t.Errorf("expected ci-jenkins NOT allowed for frontend.deploy (default-deny)")
	}
}

func TestLoad_RejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	content := baseYAML(dir, script, ca, cert, key) + "\nnot_a_real_field: true\n"
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)

	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for unknown top-level field")
	}
}

func TestLoad_RejectsRelativeScriptPath(t *testing.T) {
	dir := t.TempDir()
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	content := baseYAML(dir, "relative-deploy.sh", ca, cert, key)
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)

	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for relative action command path")
	}
}

func TestLoad_RejectsWorldWritableScript(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o757)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	yamlPath := writeFixture(t, dir, "config.yaml", baseYAML(dir, script, ca, cert, key), 0o640)

	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for world-writable action script")
	}
}

func TestLoad_RejectsUnknownActionInIdentity(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	content := `
agent:
  id: test-agent
  name: Test Agent
  listen:
    address: 0.0.0.0
    port: 8443
security:
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  backend.deploy:
    command: ` + script + `
    timeout: 5m
authorization:
  identities:
    ci-jenkins:
      actions:
        - nonexistent.action
`
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)
	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for identity referencing unknown action")
	}
}

func TestLoad_RejectsMissingTimeout(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	content := `
agent:
  id: test-agent
  name: Test Agent
  listen:
    address: 0.0.0.0
    port: 8443
security:
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  backend.deploy:
    command: ` + script + `
authorization:
  identities: {}
`
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)
	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for missing timeout")
	}
}

func TestLoad_RejectsInvalidActionName(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	content := `
agent:
  id: test-agent
  name: Test Agent
  listen:
    address: 0.0.0.0
    port: 8443
security:
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  "bad name!":
    command: ` + script + `
    timeout: 5m
authorization:
  identities: {}
`
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)
	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for invalid action name")
	}
}

func TestLoad_RejectsInvalidParameterName(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	content := `
agent:
  id: test-agent
  name: Test Agent
  listen:
    address: 0.0.0.0
    port: 8443
security:
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  backend.deploy:
    command: ` + script + `
    timeout: 5m
    parameters:
      "1bad-name":
        type: string
authorization:
  identities: {}
`
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)
	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for invalid parameter name")
	}
}

func TestLoad_RejectsTimeoutOutOfBounds(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	content := `
agent:
  id: test-agent
  name: Test Agent
  listen:
    address: 0.0.0.0
    port: 8443
security:
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  backend.deploy:
    command: ` + script + `
    timeout: 48h
authorization:
  identities: {}
`
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)
	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for timeout exceeding the maximum bound")
	}
}

func TestLoad_RejectsInvalidCAFile(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", "not a certificate", 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	yamlPath := writeFixture(t, dir, "config.yaml", baseYAML(dir, script, ca, cert, key), 0o640)
	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for invalid CA file content")
	}
}

func TestLoad_RejectsGroupWritableActionDirectory(t *testing.T) {
	dir := t.TempDir()
	actionsDir := filepath.Join(dir, "actions")
	if err := os.Mkdir(actionsDir, 0o775); err != nil { // group-writable, no sticky bit
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(actionsDir, 0o775); err != nil { // Mkdir's mode is subject to umask; force it
		t.Fatalf("chmod: %v", err)
	}
	script := writeFixture(t, actionsDir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	yamlPath := writeFixture(t, dir, "config.yaml", baseYAML(dir, script, ca, cert, key), 0o640)
	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for a group-writable (non-sticky) action script directory")
	}
}

func TestLoad_RejectsTooManyParameters(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)

	var params string
	for i := 0; i < MaxParametersPerAction+1; i++ {
		params += "      p" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ":\n        type: string\n"
	}
	content := `
agent:
  id: test-agent
  name: Test Agent
  listen:
    address: 0.0.0.0
    port: 8443
security:
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  backend.deploy:
    command: ` + script + `
    timeout: 5m
    parameters:
` + params + `
authorization:
  identities: {}
`
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)
	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for exceeding the max parameters per action")
	}
}

func TestParameter_CheckValue(t *testing.T) {
	p := Parameter{Type: ParameterTypeString, Pattern: `^[a-z0-9-]+$`}
	if err := p.Validate("image_tag"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := p.CheckValue("uat-20260823-a81f32c"); err != nil {
		t.Errorf("expected valid value to pass: %v", err)
	}
	if err := p.CheckValue("; rm -rf /"); err == nil {
		t.Errorf("expected shell-metacharacter value to be rejected")
	}
}
