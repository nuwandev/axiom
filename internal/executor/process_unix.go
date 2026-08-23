//go:build unix

package executor

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup places the child in its own process group so that
// killProcessGroup/terminateProcessGroup can signal it and everything it
// spawned as a unit, and arranges for the kernel to SIGKILL the direct
// child if Axiom's own process ever disappears without a controlled
// shutdown (crash, `kill -9`, OOM).
//
// Pdeathsig only covers the direct child, not further descendants that
// child spawns — see the package doc for why that's an intentional,
// documented scope boundary rather than a gap this package tries to close
// on its own. Under this project's actual deployment target (systemd, see
// packaging/axiom.service), the whole descendant tree is additionally
// covered by systemd's own default KillMode=control-group, which kills
// every process in the service's cgroup — including ones that escaped this
// process group — whenever the unit stops or restarts for any reason.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

// terminateProcessGroup sends SIGTERM to the entire process group, giving a
// well-behaved script a chance to exit cleanly before killProcessGroup
// force-kills it.
func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative pid targets the whole process group (see setpgid(2), kill(2)).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// killProcessGroup sends SIGKILL to the entire process group, ensuring a
// timed-out or cancelled action cannot leave orphaned descendants running.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
