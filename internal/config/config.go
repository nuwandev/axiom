// Package config loads and strictly validates the Axiom agent configuration.
//
// Load fails closed: any structural problem, missing/insecure file, invalid
// reference, or out-of-range value aborts startup rather than running with a
// partially valid configuration. gopkg.in/yaml.v3 already rejects duplicate
// mapping keys during parsing, so duplicate action names and duplicate
// identity names are caught before this package's own validation runs.
package config

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Concurrency policies for an action.
const (
	ConcurrencyShared    = "shared"
	ConcurrencyExclusive = "exclusive"
)

// Bounds used to reject obviously-wrong configuration values at load time.
const (
	MinActionTimeout = time.Second
	MaxActionTimeout = 24 * time.Hour

	MinOutputBytesLimit = 64 * 1024
	MaxOutputBytesLimit = 10 * 1024 * 1024
	DefaultOutputBytes  = 2 * 1024 * 1024

	MaxParametersPerAction = 32
	MaxParameterPatternLen = 512

	MaxJobHistoryMin     = 10
	MaxJobHistoryMax     = 100000
	DefaultMaxJobHistory = 1000
)

// actionNamePattern bounds action names to a safe, predictable charset:
// they appear in URL paths, audit log fields, and as map keys, and must
// never be interpreted as anything but an opaque identifier.
var actionNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// parameterNamePattern bounds parameter names to valid Go/shell
// identifier-like tokens, since each name is upper-cased and used to build
// an environment variable name for the child process.
var parameterNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)

// identityNamePattern bounds identity names (matched against a client
// certificate's Common Name) to a safe charset for the same reasons as
// action names — they are logged and used as map keys.
var identityNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ParameterType is the accepted type of a declared action parameter.
// Only "string" is supported in v1.
type ParameterType string

const ParameterTypeString ParameterType = "string"

// Parameter declares one accepted parameter for an action.
type Parameter struct {
	Type     ParameterType `yaml:"type"`
	Pattern  string        `yaml:"pattern"`
	Required bool          `yaml:"required"`

	compiled *regexp.Regexp
}

// Validate compiles the pattern (if any) and checks the type is supported.
//
// Go's regexp package (RE2) guarantees linear-time matching with no
// catastrophic-backtracking/ReDoS failure mode, unlike backtracking regex
// engines — so no additional complexity limit is needed beyond a sane
// upper bound on the pattern's own length, which exists purely to reject
// obviously-wrong configuration rather than for ReDoS defense.
func (p *Parameter) Validate(name string) error {
	if !parameterNamePattern.MatchString(name) {
		return fmt.Errorf("parameter name %q is invalid (must match %s)", name, parameterNamePattern.String())
	}
	if p.Type == "" {
		p.Type = ParameterTypeString
	}
	if p.Type != ParameterTypeString {
		return fmt.Errorf("parameter %q: unsupported type %q (only %q is supported)", name, p.Type, ParameterTypeString)
	}
	if len(p.Pattern) > MaxParameterPatternLen {
		return fmt.Errorf("parameter %q: pattern exceeds %d bytes", name, MaxParameterPatternLen)
	}
	if p.Pattern != "" {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return fmt.Errorf("parameter %q: invalid pattern: %w", name, err)
		}
		p.compiled = re
	}
	return nil
}

// CheckValue validates a candidate value against this parameter's rules.
func (p *Parameter) CheckValue(value string) error {
	if p.compiled != nil && !p.compiled.MatchString(value) {
		return fmt.Errorf("value does not match required pattern")
	}
	return nil
}

// Action is a single named, executable action exposed by the agent.
type Action struct {
	Name        string               `yaml:"-"`
	Command     string               `yaml:"command"`
	Timeout     time.Duration        `yaml:"timeout"`
	Concurrency string               `yaml:"concurrency"`
	Parameters  map[string]Parameter `yaml:"parameters"`
}

// Identity is a client identity's allowlisted actions.
type Identity struct {
	Actions []string `yaml:"actions"`

	allowed map[string]struct{}
}

// IsAllowed reports whether this identity may trigger the given action.
// Default-deny: an identity with no entry, or an action absent from its
// list, is denied.
func (i *Identity) IsAllowed(action string) bool {
	if i == nil {
		return false
	}
	_, ok := i.allowed[action]
	return ok
}

type mtlsConfig struct {
	CAFile   string `yaml:"ca_file"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type healthConfig struct {
	AllowAnonymous bool `yaml:"allow_anonymous"`
}

type securityConfig struct {
	MTLS   mtlsConfig   `yaml:"mtls"`
	Health healthConfig `yaml:"health"`
}

type listenConfig struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

type agentConfig struct {
	ID     string       `yaml:"id"`
	Name   string       `yaml:"name"`
	Listen listenConfig `yaml:"listen"`
}

type authorizationConfig struct {
	Identities map[string]*Identity `yaml:"identities"`
}

type auditConfig struct {
	Path string `yaml:"path"`
}

type outputConfig struct {
	MaxBytes int `yaml:"max_bytes"`
}

type jobsConfig struct {
	MaxHistory int `yaml:"max_history"`
}

// rawConfig mirrors the on-disk YAML shape before post-load processing.
type rawConfig struct {
	Agent         agentConfig         `yaml:"agent"`
	Security      securityConfig      `yaml:"security"`
	Actions       map[string]*Action  `yaml:"actions"`
	Authorization authorizationConfig `yaml:"authorization"`
	Audit         auditConfig         `yaml:"audit"`
	Output        outputConfig        `yaml:"output"`
	Jobs          jobsConfig          `yaml:"jobs"`
}

// DefaultAuditLogPath is used when audit.path is not set in config.
const DefaultAuditLogPath = "/var/log/axiom/audit.log"

// Config is the fully validated, ready-to-use agent configuration.
type Config struct {
	AgentID       string
	AgentName     string
	ListenAddress string
	ListenPort    int

	CAFile   string
	CertFile string
	KeyFile  string

	HealthAllowAnonymous bool

	Actions map[string]*Action

	Identities map[string]*Identity

	AuditLogPath string

	MaxOutputBytes int
	MaxJobHistory  int
}

// Load reads, parses, and strictly validates the configuration at path.
// It fails fast on any structural, security, or referential problem rather
// than starting with a partially valid configuration.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg := &Config{
		AgentID:              raw.Agent.ID,
		AgentName:            raw.Agent.Name,
		ListenAddress:        raw.Agent.Listen.Address,
		ListenPort:           raw.Agent.Listen.Port,
		CAFile:               raw.Security.MTLS.CAFile,
		CertFile:             raw.Security.MTLS.CertFile,
		KeyFile:              raw.Security.MTLS.KeyFile,
		HealthAllowAnonymous: raw.Security.Health.AllowAnonymous,
		Actions:              raw.Actions,
		Identities:           raw.Authorization.Identities,
		AuditLogPath:         raw.Audit.Path,
		MaxOutputBytes:       raw.Output.MaxBytes,
		MaxJobHistory:        raw.Jobs.MaxHistory,
	}
	if cfg.AuditLogPath == "" {
		cfg.AuditLogPath = DefaultAuditLogPath
	}
	if cfg.MaxOutputBytes == 0 {
		cfg.MaxOutputBytes = DefaultOutputBytes
	}
	if cfg.MaxJobHistory == 0 {
		cfg.MaxJobHistory = DefaultMaxJobHistory
	}
	if cfg.Actions == nil {
		cfg.Actions = map[string]*Action{}
	}
	if cfg.Identities == nil {
		cfg.Identities = map[string]*Identity{}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.AgentID == "" {
		return fmt.Errorf("agent.id is required")
	}
	if c.AgentName == "" {
		return fmt.Errorf("agent.name is required")
	}
	if c.ListenAddress == "" {
		return fmt.Errorf("agent.listen.address is required")
	}
	if c.ListenPort <= 0 || c.ListenPort > 65535 {
		return fmt.Errorf("agent.listen.port must be between 1 and 65535, got %d", c.ListenPort)
	}

	if c.CAFile == "" || c.CertFile == "" || c.KeyFile == "" {
		return fmt.Errorf("security.mtls: ca_file, cert_file, and key_file are all required")
	}
	if err := requireValidCAFile(c.CAFile); err != nil {
		return fmt.Errorf("security.mtls.ca_file: %w", err)
	}
	if err := requireValidCertKeyPair(c.CertFile, c.KeyFile); err != nil {
		return fmt.Errorf("security.mtls: %w", err)
	}
	for label, p := range map[string]string{"ca_file": c.CAFile, "cert_file": c.CertFile, "key_file": c.KeyFile} {
		// A trust anchor (CA) or server key sitting in a directory an
		// untrusted local user can write to is a full compromise (swap the
		// CA to mint accepted client certs, or swap the server key to
		// impersonate the agent) even if the file itself is locked down.
		if err := requireSecureParentDir(p); err != nil {
			return fmt.Errorf("security.mtls.%s: %w", label, err)
		}
	}

	if !filepath.IsAbs(c.AuditLogPath) {
		return fmt.Errorf("audit.path must be an absolute path, got %q", c.AuditLogPath)
	}
	if err := requireSecureParentDir(c.AuditLogPath); err != nil {
		return fmt.Errorf("audit.path: %w", err)
	}

	if c.MaxOutputBytes < MinOutputBytesLimit || c.MaxOutputBytes > MaxOutputBytesLimit {
		return fmt.Errorf("output.max_bytes must be between %d and %d, got %d", MinOutputBytesLimit, MaxOutputBytesLimit, c.MaxOutputBytes)
	}
	if c.MaxJobHistory < MaxJobHistoryMin || c.MaxJobHistory > MaxJobHistoryMax {
		return fmt.Errorf("jobs.max_history must be between %d and %d, got %d", MaxJobHistoryMin, MaxJobHistoryMax, c.MaxJobHistory)
	}

	if len(c.Actions) == 0 {
		return fmt.Errorf("actions: at least one action must be configured")
	}
	for name, a := range c.Actions {
		if !actionNamePattern.MatchString(name) {
			return fmt.Errorf("actions: invalid action name %q (must match %s)", name, actionNamePattern.String())
		}
		a.Name = name
		if err := validateAction(name, a); err != nil {
			return err
		}
	}

	for identityName, id := range c.Identities {
		if !identityNamePattern.MatchString(identityName) {
			return fmt.Errorf("authorization.identities: invalid identity name %q (must match %s)", identityName, identityNamePattern.String())
		}
		if len(id.Actions) == 0 {
			return fmt.Errorf("authorization.identities.%s: must declare at least one action", identityName)
		}
		id.allowed = make(map[string]struct{}, len(id.Actions))
		for _, actionName := range id.Actions {
			if _, ok := c.Actions[actionName]; !ok {
				return fmt.Errorf("authorization.identities.%s: references unknown action %q", identityName, actionName)
			}
			id.allowed[actionName] = struct{}{}
		}
	}

	return nil
}

func validateAction(name string, a *Action) error {
	if a.Command == "" {
		return fmt.Errorf("actions.%s.command is required", name)
	}
	if a.Timeout < MinActionTimeout || a.Timeout > MaxActionTimeout {
		return fmt.Errorf("actions.%s.timeout must be between %s and %s, got %s", name, MinActionTimeout, MaxActionTimeout, a.Timeout)
	}
	switch a.Concurrency {
	case "":
		a.Concurrency = ConcurrencyShared
	case ConcurrencyShared, ConcurrencyExclusive:
	default:
		return fmt.Errorf("actions.%s.concurrency must be %q or %q, got %q", name, ConcurrencyShared, ConcurrencyExclusive, a.Concurrency)
	}

	if err := validateActionScript(name, a.Command); err != nil {
		return err
	}

	if len(a.Parameters) > MaxParametersPerAction {
		return fmt.Errorf("actions.%s.parameters: at most %d parameters are allowed, got %d", name, MaxParametersPerAction, len(a.Parameters))
	}
	for paramName, p := range a.Parameters {
		p := p
		if err := p.Validate(paramName); err != nil {
			return fmt.Errorf("actions.%s.parameters: %w", name, err)
		}
		a.Parameters[paramName] = p
	}

	return nil
}

func validateActionScript(actionName, command string) error {
	if !filepath.IsAbs(command) {
		return fmt.Errorf("actions.%s.command must be an absolute path, got %q", actionName, command)
	}
	if command != filepath.Clean(command) {
		return fmt.Errorf("actions.%s.command must be a clean path with no \".\"/\"..\" segments, got %q", actionName, command)
	}
	if err := checkScriptSecurity(command); err != nil {
		return fmt.Errorf("actions.%s.command: %w", actionName, err)
	}
	// A script with safe ownership/permissions sitting in a directory that
	// is itself group/world-writable can still be swapped out from under
	// Axiom by an untrusted local user (delete+replace via the directory,
	// not the file). Walk every ancestor directory up to (but not
	// including) the filesystem root and require the same ownership and
	// non-group/world-writable guarantee.
	if err := requireSecureParentDir(command); err != nil {
		return fmt.Errorf("actions.%s.command: %w", actionName, err)
	}
	return nil
}

func requireExistingFile(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("must be an absolute path, got %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("not accessible: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory, expected a file", path)
	}
	return nil
}

func requireValidCAFile(path string) error {
	if err := requireExistingFile(path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return fmt.Errorf("%q contains no valid PEM-encoded certificates", path)
	}
	return nil
}

func requireValidCertKeyPair(certPath, keyPath string) error {
	if err := requireExistingFile(certPath); err != nil {
		return fmt.Errorf("cert_file: %w", err)
	}
	if err := requireExistingFile(keyPath); err != nil {
		return fmt.Errorf("key_file: %w", err)
	}
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("reading cert_file %q: %w", certPath, err)
	}
	block, _ := pem.Decode(certData)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("cert_file %q does not contain a PEM certificate", certPath)
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return fmt.Errorf("cert_file %q: invalid certificate: %w", certPath, err)
	}
	// The key's own validity and its match against the certificate is
	// verified by tls.LoadX509KeyPair when the TLS listener is built at
	// startup (internal/auth.TLSConfig) — duplicating X.509/PKCS8 key
	// parsing here would just be two implementations of the same check.
	if err := requireExistingFile(keyPath); err != nil {
		return err
	}
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("reading key_file %q: %w", keyPath, err)
	}
	if kb, _ := pem.Decode(keyData); kb == nil {
		return fmt.Errorf("key_file %q does not contain PEM data", keyPath)
	}
	return nil
}
