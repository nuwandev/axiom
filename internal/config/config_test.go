package config

import (
	"os"
	"path/filepath"
	"testing"
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
	ca := writeFixture(t, dir, "ca.crt", "ca", 0o640)
	cert := writeFixture(t, dir, "server.crt", "cert", 0o640)
	key := writeFixture(t, dir, "server.key", "key", 0o600)

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
	ca := writeFixture(t, dir, "ca.crt", "ca", 0o640)
	cert := writeFixture(t, dir, "server.crt", "cert", 0o640)
	key := writeFixture(t, dir, "server.key", "key", 0o600)

	content := baseYAML(dir, script, ca, cert, key) + "\nnot_a_real_field: true\n"
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)

	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for unknown top-level field")
	}
}

func TestLoad_RejectsRelativeScriptPath(t *testing.T) {
	dir := t.TempDir()
	ca := writeFixture(t, dir, "ca.crt", "ca", 0o640)
	cert := writeFixture(t, dir, "server.crt", "cert", 0o640)
	key := writeFixture(t, dir, "server.key", "key", 0o600)

	content := baseYAML(dir, "relative-deploy.sh", ca, cert, key)
	yamlPath := writeFixture(t, dir, "config.yaml", content, 0o640)

	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for relative action command path")
	}
}

func TestLoad_RejectsWorldWritableScript(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o757)
	ca := writeFixture(t, dir, "ca.crt", "ca", 0o640)
	cert := writeFixture(t, dir, "server.crt", "cert", 0o640)
	key := writeFixture(t, dir, "server.key", "key", 0o600)

	yamlPath := writeFixture(t, dir, "config.yaml", baseYAML(dir, script, ca, cert, key), 0o640)

	if _, err := Load(yamlPath); err == nil {
		t.Fatalf("expected error for world-writable action script")
	}
}

func TestLoad_RejectsUnknownActionInIdentity(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", "ca", 0o640)
	cert := writeFixture(t, dir, "server.crt", "cert", 0o640)
	key := writeFixture(t, dir, "server.key", "key", 0o600)

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
	ca := writeFixture(t, dir, "ca.crt", "ca", 0o640)
	cert := writeFixture(t, dir, "server.crt", "cert", 0o640)
	key := writeFixture(t, dir, "server.key", "key", 0o600)

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
