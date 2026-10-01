//go:build windows

package executor

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsContainment confines a capability process and every descendant it
// spawns to a Job Object created with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE:
//
//   - terminate/kill call TerminateJobObject, which kills the entire tree at
//     once. A Windows service has no console, so there is no Ctrl-Break
//     equivalent to deliver first — the cooperative-stop phase that exists
//     on unix simply does not apply here, and Run's grace timer is never
//     reached because Wait returns as soon as the job is terminated.
//   - the job handle is held only by the Axiom process, so an uncontrolled
//     death of Axiom (crash, OOM, force-kill) closes the handle and the OS
//     tears the tree down automatically. This covers descendants that the
//     unix PR_SET_PDEATHSIG does not.
//
// The process is assigned to the job immediately after cmd.Start(). If the
// script finishes first (faster than the containment call), there is nothing
// to contain and started() returns without a job — Run's own Wait still
// reports the real exit code. But if the child is still running and cannot
// be assigned to the job (a job-object nesting conflict, an access-denied
// security descriptor), started() kills it and returns an error: Run fails
// the job closed rather than run a capability whose process tree it cannot
// terminate. The sub-millisecond window in which a still-running script
// could spawn a descendant before assignment is an accepted residual (see
// docs/windows-security.md and docs/THREAT-MODEL.md): powershell.exe
// executes no script line, and therefore spawns nothing, that quickly.
type windowsContainment struct {
	job windows.Handle
}

func newContainment() processContainment { return &windowsContainment{} }

func (w *windowsContainment) prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// A service runs in session 0 with no console; do not create or attach
	// one for the child.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

func (w *windowsContainment) started(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	pid := uint32(cmd.Process.Pid)

	h, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE,
		false, pid)
	if err != nil {
		// Can't open our own child by PID. If it has already exited there is
		// nothing to contain (Run's Wait still reports the real exit code);
		// otherwise this is a genuine failure and we must not run the
		// capability uncontained.
		if processExited(pid) {
			return nil
		}
		return fmt.Errorf("opening child process for containment: %w", err)
	}
	defer windows.CloseHandle(h)

	if handleSignaled(h) {
		return nil // already exited on its own
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("creating job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return fmt.Errorf("configuring job object: %w", err)
	}

	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.CloseHandle(job)
		// If the process merely raced to exit, there is nothing to contain.
		// If it is still alive, we failed to contain a running capability —
		// kill it and fail the job closed rather than run it uncontained
		// (a job-object nesting conflict or an access-denied SD would land
		// here).
		if handleSignaled(h) {
			return nil
		}
		_ = windows.TerminateProcess(h, 1)
		return fmt.Errorf("assigning child to job object (capability would run uncontained): %w", err)
	}

	w.job = job
	return nil
}

// handleSignaled reports whether a process handle is signaled — i.e. the
// process has exited. WaitForSingleObject with a zero timeout is the
// unambiguous primitive for this (unlike GetExitCodeProcess, which cannot
// distinguish a running process from one that exited with code 259).
func handleSignaled(h windows.Handle) bool {
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == windows.WAIT_OBJECT_0
}

func processExited(pid uint32) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return true // cannot even open it — treat as gone
	}
	defer windows.CloseHandle(h)
	return handleSignaled(h)
}

func (w *windowsContainment) terminate(*exec.Cmd) { w.forceKill() }
func (w *windowsContainment) kill(*exec.Cmd)      { w.forceKill() }

func (w *windowsContainment) forceKill() {
	if w.job != 0 {
		_ = windows.TerminateJobObject(w.job, 1)
	}
}

func (w *windowsContainment) release() {
	if w.job != 0 {
		// Closing the final handle triggers KILL_ON_JOB_CLOSE for anything
		// still running in the job — a backstop if forceKill was never
		// called (e.g. the script exited on its own).
		_ = windows.CloseHandle(w.job)
		w.job = 0
	}
}
