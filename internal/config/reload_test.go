package config

import (
	"reflect"
	"testing"
	"time"
)

func baseConfig() *Config {
	return &Config{
		AgentID:       "a",
		ListenAddress: "127.0.0.1",
		ListenPort:    8443,
		CAFile:        "/ca",
		CertFile:      "/crt",
		KeyFile:       "/key",
		Actions: map[string]*Action{
			"x": {Command: "/bin/x", Timeout: time.Second, Concurrency: ConcurrencyShared,
				Parameters: map[string]Parameter{"p": {Type: ParameterTypeString, Pattern: "[a-z]+", Required: true}}},
		},
		Identities: map[string]*Identity{"i": {Actions: []string{"x"}}},
	}
}

func TestNonReloadableChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   []string
	}{
		{"nothing changed", func(*Config) {}, nil},
		{"identities and certificate paths are reloadable", func(c *Config) {
			c.Identities = map[string]*Identity{"j": {Actions: []string{"x"}}}
			c.CAFile, c.CertFile, c.KeyFile = "/ca2", "/crt2", "/key2"
		}, nil},
		{"port", func(c *Config) { c.ListenPort = 9 }, []string{"ListenPort"}},
		{"limit", func(c *Config) { c.MaxTriggersPerMinute = 5 }, []string{"MaxTriggersPerMinute"}},
		{"action command", func(c *Config) { c.Actions["x"].Command = "/bin/y" }, []string{"Actions"}},
		{"action parameter pattern", func(c *Config) {
			c.Actions["x"].Parameters = map[string]Parameter{"p": {Type: ParameterTypeString, Pattern: ".*", Required: true}}
		}, []string{"Actions"}},
		{"action added", func(c *Config) { c.Actions["y"] = &Action{Command: "/bin/y"} }, []string{"Actions"}},
		{"several", func(c *Config) { c.ListenPort = 9; c.AgentID = "b" }, []string{"AgentID", "ListenPort"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := baseConfig()
			tc.mutate(next)
			got := baseConfig().NonReloadableChanges(next)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIdentityChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"identical", func(*Config) {}, "no identity changes"},
		{"added", func(c *Config) { c.Identities["b"] = &Identity{Actions: []string{"x"}} }, "added: b"},
		{"removed", func(c *Config) { c.Identities = map[string]*Identity{} }, "removed: i"},
		{"changed", func(c *Config) { c.Identities["i"] = &Identity{Actions: []string{"x", "y"}} }, "changed: i"},
		{"all three, sorted", func(c *Config) {
			c.Identities = map[string]*Identity{"z": {Actions: []string{"x"}}, "a": {Actions: []string{"x"}}}
		}, "added: a, z; removed: i"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next := baseConfig()
			tc.mutate(next)
			if got := baseConfig().IdentityChanges(next); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Reordering or repeating an identity's actions grants and removes nothing, so
// it must not be reported as a change.
func TestIdentityChanges_IgnoresActionOrderAndDuplicates(t *testing.T) {
	a, b := baseConfig(), baseConfig()
	a.Identities["i"] = &Identity{Actions: []string{"x", "y"}}
	b.Identities["i"] = &Identity{Actions: []string{"y", "x", "y"}}
	if got := a.IdentityChanges(b); got != "no identity changes" {
		t.Fatalf("got %q", got)
	}
}
