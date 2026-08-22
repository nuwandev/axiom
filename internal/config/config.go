// Package config loads and strictly validates the Axiom agent configuration.
package config

import (
	"bytes"
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
func (p *Parameter) Validate(name string) error {
	if p.Type == "" {
		p.Type = ParameterTypeString
	}
	if p.Type != ParameterTypeString {
		return fmt.Errorf("parameter %q: unsupported type %q (only %q is supported)", name, p.Type, ParameterTypeString)
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

// rawConfig mirrors the on-disk YAML shape before post-load processing.
type rawConfig struct {
	Agent         agentConfig         `yaml:"agent"`
	Security      securityConfig      `yaml:"security"`
	Actions       map[string]*Action  `yaml:"actions"`
	Authorization authorizationConfig `yaml:"authorization"`
	Audit         auditConfig         `yaml:"audit"`
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
	}
	if cfg.AuditLogPath == "" {
		cfg.AuditLogPath = DefaultAuditLogPath
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
	for label, p := range map[string]string{"ca_file": c.CAFile, "cert_file": c.CertFile, "key_file": c.KeyFile} {
		if err := requireExistingFile(p); err != nil {
			return fmt.Errorf("security.mtls.%s: %w", label, err)
		}
	}

	if !filepath.IsAbs(c.AuditLogPath) {
		return fmt.Errorf("audit.path must be an absolute path, got %q", c.AuditLogPath)
	}

	if len(c.Actions) == 0 {
		return fmt.Errorf("actions: at least one action must be configured")
	}
	for name, a := range c.Actions {
		if name == "" {
			return fmt.Errorf("actions: action name must not be empty")
		}
		a.Name = name
		if err := validateAction(name, a); err != nil {
			return err
		}
	}

	for identityName, id := range c.Identities {
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
	if a.Timeout <= 0 {
		return fmt.Errorf("actions.%s.timeout must be a positive duration (e.g. \"10m\")", name)
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
	if err := checkScriptSecurity(command); err != nil {
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
