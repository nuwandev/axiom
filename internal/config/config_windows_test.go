//go:build windows

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Full config.Load coverage on Windows needs an NTFS-ACL-configured fixture
// tree (see docs/development.md) and runs on a real host. These tests cover
// the Windows-specific wiring that does not require elevation: the
// script-security check enforces the .ps1 requirement and rejects a
// user-writable script, and rejectSymlink flags reparse points.

func TestCheckScriptSecurity_Windows_RequiresPS1(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "deploy.bat")
	if err := os.WriteFile(bat, []byte("echo hi\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := checkScriptSecurity(bat)
	if err == nil || !strings.Contains(err.Error(), "ps1") {
		t.Fatalf("checkScriptSecurity(.bat) = %v, want a .ps1 error", err)
	}
}

func TestCheckScriptSecurity_Windows_RejectsUserWritableScript(t *testing.T) {
	dir := t.TempDir()
	ps1 := filepath.Join(dir, "deploy.ps1")
	if err := os.WriteFile(ps1, []byte("exit 0\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A temp file is owned by the current (non-trusted) user with an
	// inherited full-control ACE — exactly the insecure condition the check
	// must reject.
	if err := checkScriptSecurity(ps1); err == nil {
		t.Fatalf("checkScriptSecurity on a user-owned temp .ps1 = nil, want an error")
	}
}

func TestRejectSymlink_Windows_MissingPath(t *testing.T) {
	if err := rejectSymlink(filepath.Join(t.TempDir(), "nope.ps1")); err == nil {
		t.Fatalf("rejectSymlink on a missing path = nil, want an error")
	}
}
