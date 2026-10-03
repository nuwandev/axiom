//go:build windows

package executor

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"
)

// withPolicyKey creates a throwaway HKCU key (no elevation, no real policy
// touched), applies set to it, and returns its path.
func withPolicyKey(t *testing.T, set func(k registry.Key)) string {
	t.Helper()
	path := fmt.Sprintf(`Software\AxiomTest\gpo-%d`, time.Now().UnixNano())
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatalf("creating test key: %v", err)
	}
	set(k)
	k.Close()
	t.Cleanup(func() {
		registry.DeleteKey(registry.CURRENT_USER, path)
		registry.DeleteKey(registry.CURRENT_USER, `Software\AxiomTest`)
	})
	return path
}

func TestGPOExecutionPolicy(t *testing.T) {
	tests := []struct {
		name string
		set  func(k registry.Key)
		want string
	}{
		{"no values set", func(k registry.Key) {}, ""},
		{"AllSigned", func(k registry.Key) { k.SetStringValue("ExecutionPolicy", "AllSigned") }, "AllSigned"},
		{"RemoteSigned", func(k registry.Key) { k.SetStringValue("ExecutionPolicy", "RemoteSigned") }, "RemoteSigned"},
		{"scripts disabled blocks everything", func(k registry.Key) { k.SetDWordValue("EnableScripts", 0) }, "Restricted"},
		{"scripts disabled wins over a permissive policy value", func(k registry.Key) {
			k.SetDWordValue("EnableScripts", 0)
			k.SetStringValue("ExecutionPolicy", "RemoteSigned")
		}, "Restricted"},
		{"scripts enabled uses the policy value", func(k registry.Key) {
			k.SetDWordValue("EnableScripts", 1)
			k.SetStringValue("ExecutionPolicy", "AllSigned")
		}, "AllSigned"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := withPolicyKey(t, tc.set)
			got, err := gpoExecutionPolicy(registry.CURRENT_USER, path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGPOExecutionPolicy_MissingKeyIsNoPolicy(t *testing.T) {
	got, err := gpoExecutionPolicy(registry.CURRENT_USER, `Software\AxiomTest\does-not-exist-`+fmt.Sprint(time.Now().UnixNano()))
	if err != nil || got != "" {
		t.Fatalf("missing key: got (%q, %v), want (\"\", nil)", got, err)
	}
}
