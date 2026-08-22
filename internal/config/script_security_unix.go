//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// checkScriptSecurity enforces that a configured action script cannot be
// tampered with by an untrusted local user: it must exist, be a regular
// file, be executable, be owned by root (or the account that owns the
// running Axiom process, so integration tests running as a non-root
// service account still enforce "owned by the expected administrator"),
// and not be writable by its group or by other users.
func checkScriptSecurity(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("not accessible: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%q: unable to determine file owner", path)
	}
	return evaluateScriptSecurity(path, info.IsDir(), info.Mode(), stat.Uid, uint32(os.Geteuid()))
}

// evaluateScriptSecurity is the pure decision logic, separated so it can be
// unit tested without requiring root-owned fixture files on disk.
func evaluateScriptSecurity(path string, isDir bool, mode os.FileMode, ownerUID, expectedUID uint32) error {
	if isDir {
		return fmt.Errorf("%q is a directory, expected an executable file", path)
	}
	if mode&0o111 == 0 {
		return fmt.Errorf("%q is not executable", path)
	}
	if mode&0o022 != 0 {
		return fmt.Errorf("%q is group- or world-writable (mode %s); scripts must not be modifiable by untrusted users", path, mode.Perm())
	}
	if ownerUID != 0 && ownerUID != expectedUID {
		return fmt.Errorf("%q is not owned by root or the Axiom service account (owner uid %d); action scripts must be owned by the administrator account", path, ownerUID)
	}
	return nil
}
