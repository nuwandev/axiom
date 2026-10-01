//go:build unix

package executor

import "os/exec"

// buildCommand invokes the capability script directly, exactly as the
// executor has always done on unix: no shell, no wrapper, no extra
// arguments. Shell metacharacters in the environment therefore have no
// special meaning.
func buildCommand(spec Spec) *exec.Cmd {
	return exec.Command(spec.Command)
}

// CheckPlatformPrerequisites has nothing to verify on unix — the capability
// script is executed directly and its own integrity is validated by
// internal/config at load time.
func CheckPlatformPrerequisites() error { return nil }
