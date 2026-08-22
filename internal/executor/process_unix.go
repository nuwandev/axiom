//go:build unix

package executor

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup places the child in its own process group so that
// killProcessGroup can terminate it and everything it spawned as a unit.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the entire process group, ensuring a
// timed-out or cancelled action cannot leave orphaned descendants running.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative pid targets the whole process group (see setpgid(2), kill(2)).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
