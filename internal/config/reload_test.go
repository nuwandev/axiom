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
