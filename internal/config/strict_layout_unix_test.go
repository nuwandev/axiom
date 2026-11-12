//go:build unix

package config

import (
	"os"
	"testing"
)

// TestLoad_InstalledLayout loads a config from a real root-owned layout (as
// the RPM/installer creates it) with the production defaults: no
// allow_service_owned_files. It is skipped unless AXIOM_LAYOUT_CONFIG points
// at such a config, because it needs root-owned fixtures and a non-root test
// user; scripts/rhel-test-fs-security.sh style hosts run it this way:
//
//	AXIOM_LAYOUT_CONFIG=/etc/axiom/config.yaml go test -run TestLoad_InstalledLayout ./internal/config
func TestLoad_InstalledLayout(t *testing.T) {
	path := os.Getenv("AXIOM_LAYOUT_CONFIG")
	if path == "" {
		t.Skip("AXIOM_LAYOUT_CONFIG not set")
	}
	if os.Geteuid() == 0 {
		t.Skip("run as the unprivileged service account")
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load(%s) with production defaults: %v", path, err)
	}
}
