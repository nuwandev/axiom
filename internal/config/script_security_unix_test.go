//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
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

	if err := checkScriptSecurity(symlinkPath, true); err == nil {
		t.Fatalf("expected checkScriptSecurity to reject a symlink, got nil error")
	}
}

// Production default: a script owned by the account Axiom runs as is refused,
// because that account (or a compromised action running as it) could rewrite
// it. Root ownership is the supported layout.
func TestCheckScriptSecurity_ServiceOwnedRejectedByDefault(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a root-owned file is always acceptable; run as a non-root user")
	}
	script := writeFixture(t, t.TempDir(), "a.sh", "#!/bin/sh\n", 0o750)

	err := checkScriptSecurity(script, false)
	if err == nil || !strings.Contains(err.Error(), "service account") {
		t.Fatalf("checkScriptSecurity(service-owned, strict) = %v, want a service-account ownership error", err)
	}
	if err := checkScriptSecurity(script, true); err != nil {
		t.Fatalf("checkScriptSecurity with the development opt-in = %v, want nil", err)
	}
}

func TestRequireSecureParentDir_ServiceOwnedDirectoryRejectedByDefault(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("run as a non-root user")
	}
	dir := t.TempDir() // owned by the test user, not sticky
	target := filepath.Join(dir, "x")

	if err := requireSecureParentDir(target, false); err == nil {
		t.Fatal("a service-owned parent directory was accepted in strict mode")
	}
	if err := requireSecureParentDir(target, true); err != nil {
		t.Fatalf("with the development opt-in: %v", err)
	}
}

func TestRequireTrustedFile(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		mode    os.FileMode
		private bool
		wantErr string // substring; empty = accepted (with the dev opt-in)
	}{
		{"private key 0600", 0o600, true, ""},
		{"private key 0640 (documented root:axiom layout)", 0o640, true, ""},
		{"private key 0644 readable by everyone", 0o644, true, "accessible by other users"},
		{"private key 0604 other-readable", 0o604, true, "accessible by other users"},
		{"private key 0660 group-writable", 0o660, true, "writable"},
		{"certificate 0644", 0o644, false, ""},
		{"certificate 0664 group-writable", 0o664, false, "writable"},
		{"certificate 0646 world-writable", 0o646, false, "writable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeFixture(t, dir, strings.ReplaceAll(tc.name, " ", "_"), "x", tc.mode)
			err := requireTrustedFile(p, tc.private, true)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}

	t.Run("symlink rejected", func(t *testing.T) {
		real := writeFixture(t, dir, "real.key", "x", 0o600)
		link := filepath.Join(dir, "link.key")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if err := requireTrustedFile(link, true, true); err == nil {
			t.Fatal("a symlinked key was accepted")
		}
	})

	t.Run("service-owned rejected in strict mode", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("run as a non-root user")
		}
		p := writeFixture(t, dir, "owned.crt", "x", 0o644)
		if err := requireTrustedFile(p, false, false); err == nil {
			t.Fatal("a service-owned file was accepted in strict mode")
		}
	})
}
