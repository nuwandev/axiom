//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEvaluateScriptSecurity(t *testing.T) {
	const me = 1000

	cases := []struct {
		name        string
		isDir       bool
		mode        os.FileMode
		ownerUID    uint32
		expectedUID uint32
		wantErr     bool
	}{
		{"ok root owned 0755", false, 0o755, 0, me, false},
		{"ok owned by service account 0750", false, 0o750, me, me, false},
		{"rejects directory", true, 0o755, 0, me, true},
		{"rejects non-executable", false, 0o644, 0, me, true},
		{"rejects group writable", false, 0o775, 0, me, true},
		{"rejects world writable", false, 0o757, 0, me, true},
		{"rejects wrong owner", false, 0o755, 1234, me, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateScriptSecurity("/opt/axiom/actions/x.sh", tc.isDir, tc.mode, tc.ownerUID, tc.expectedUID)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}

// TestCheckScriptSecurity_RejectsSymlink demonstrates the actual gap found
// during real-host validation: requireSecureParentDir walks the ancestors
// of the *configured* path, not of wherever a symlink at that path
// resolves to. A script placed in a directory with perfectly secure
// ownership/permissions, but which is actually a symlink to a file sitting
// in a directory the ancestor-walk never visits (e.g. one that's
// group-writable), would previously pass every check even though an
// untrusted user could still replace the real script through that other
// directory. The fix is to reject symlinks outright rather than resolve
// and re-walk the target.
func TestCheckScriptSecurity_RejectsSymlink(t *testing.T) {
	secureDir := t.TempDir() // 0700 by default, owned by the test process
	insecureDir := t.TempDir()
	if err := os.Chmod(insecureDir, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	realScript := filepath.Join(insecureDir, "real.sh")
	if err := os.WriteFile(realScript, []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil {
		t.Fatalf("writing real script: %v", err)
	}

	symlinkPath := filepath.Join(secureDir, "innocent-looking.sh")
	if err := os.Symlink(realScript, symlinkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := checkScriptSecurity(symlinkPath); err == nil {
		t.Fatalf("expected checkScriptSecurity to reject a symlink, got nil error")
	}
}
