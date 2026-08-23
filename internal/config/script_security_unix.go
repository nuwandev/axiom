//go:build unix

package config

import (
	"fmt"
	"os"
	"path/filepath"
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

// requireSecureParentDir walks every ancestor directory of target, from its
// immediate parent up to (and including) the filesystem root, and requires
// each to be owned by root or the Axiom service account and not
// group/world-writable.
//
// A file with perfectly safe ownership and permissions can still be
// tampered with by an untrusted local user if any directory in its path is
// writable by that user: they can simply delete-and-replace the file
// through the directory (rename/unlink operate on the containing
// directory's permissions, not the target file's). This closes that gap
// for action scripts, the audit log directory, and mTLS material.
func requireSecureParentDir(target string) error {
	expectedUID := uint32(os.Geteuid())
	dir := filepath.Dir(target)

	for {
		// os.Stat (not Lstat) follows a symlinked directory to its real
		// target, so a system path that is itself a symlink (e.g. RHEL's
		// usr-merge /bin -> /usr/bin) is judged on the real directory's
		// ownership/permissions rather than rejected outright.
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				// The directory may not exist yet (e.g. the audit log
				// directory, created by the installer/first run) — nothing
				// to enforce until it exists; the audit package's own open
				// call will fail closed if it's genuinely missing at
				// startup.
				break
			}
			return fmt.Errorf("checking directory %q: %w", dir, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("%q: unable to determine directory owner", dir)
		}
		// A group/world-writable directory normally lets any such user
		// delete-and-replace files they don't own, EXCEPT when the sticky
		// bit is set (e.g. /tmp, mode 1777): sticky restricts unlink/rename
		// to the file's owner, the directory's owner, or root, regardless
		// of the write bits. So sticky + writable is not a hole; only
		// writable-without-sticky is.
		sticky := info.Mode()&os.ModeSticky != 0
		if info.Mode()&0o022 != 0 && !sticky {
			return fmt.Errorf("directory %q is group- or world-writable without the sticky bit (mode %s); this would let an untrusted local user replace %q", dir, info.Mode().Perm(), target)
		}
		if !sticky && stat.Uid != 0 && stat.Uid != expectedUID {
			return fmt.Errorf("directory %q is not owned by root or the Axiom service account (owner uid %d)", dir, stat.Uid)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached the filesystem root
		}
		dir = parent
	}
	return nil
}
