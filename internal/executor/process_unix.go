//go:build unix

package executor

import (
	"os/exec"
	"syscall"
)

// unixContainment places the child in its own process group so the whole
// tree it spawns can be signalled as a unit, and arranges for the kernel to
// SIGKILL the direct child if Axiom's own process ever disappears without a
// controlled shutdown (crash, `kill -9`, OOM).
//
// Pdeathsig only covers the direct child, not further descendants that child
// spawns — see the package doc for why that's an intentional, documented
// scope boundary. Under this project's actual deployment target (systemd,
// see packaging/axiom.service), the whole descendant tree is additionally
// covered by systemd's own default KillMode=control-group.
type unixContainment struct{}

func newContainment() processContainment { return &unixContainment{} }

func (unixContainment) prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
}

func (unixContainment) started(*exec.Cmd) error { return nil }

// terminate sends SIGTERM to the entire process group, giving a well-behaved
// script a chance to exit cleanly before kill force-kills it.
func (unixContainment) terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// Negative pid targets the whole process group (see setpgid(2), kill(2)).
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// kill sends SIGKILL to the entire process group, ensuring a timed-out or
// cancelled action cannot leave orphaned descendants running.
func (unixContainment) kill(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

func (unixContainment) release() {}
