//go:build windows

package config

import "github.com/nuwandev/axiom/internal/winsec"

// checkScriptSecurity enforces, on Windows, that a configured action script
// is a real PowerShell file that no unauthorized local principal can modify
// or redirect: it must be a .ps1 file (not a reparse point, not a
// directory), its path must contain no shell-dangerous characters, it must
// be owned by Administrators / SYSTEM / TrustedInstaller, and its DACL must
// grant write/modify/delete/take-ownership to none of the other principals
// on the box — the Axiom service account included, so the agent can run its
// capabilities but never rewrite them.
func checkScriptSecurity(path string) error {
	return winsec.ValidateCapabilityScript(path)
}

// requireSecureParentDir walks the directories above target and requires
// each to be trusted-owned with no untrusted write access, stopping at an
// ancestor whose DACL has inheritance disabled (SE_DACL_PROTECTED). That
// protected directory — normally %ProgramData%\Axiom, established by the
// installer — is the trust boundary; the walk deliberately does not climb
// to the volume root, because the default %ProgramData% ACL lets any user
// create files and would always fail.
func requireSecureParentDir(target string) error {
	return winsec.RequireProtectedAncestry(target)
}

// rejectSymlink refuses a path whose final component is a reparse point
// (symbolic link or directory junction), matching the unix behaviour: a
// reparse point's real target is not covered by the ancestor-directory
// walk, so configured paths must be real files.
func rejectSymlink(path string) error {
	return winsec.RejectReparsePoint(path)
}
