// Package executor runs configured capability scripts as child processes with
// enforced timeouts and cancellation, whole-process-tree containment so a
// script's descendants can be killed as a unit and cannot outlive an
// unexpected death of the Axiom process itself, and bounded output capture.
//
// The two platform-specific concerns are isolated behind small internal
// seams: buildCommand turns a Spec into the concrete *exec.Cmd (a direct
// invocation on unix; a fixed powershell.exe -NoProfile -NonInteractive
// -ExecutionPolicy Bypass -File <script> invocation on Windows, see
// launch_windows.go's buildCommand for why Bypass is safe here), and
// processContainment isolates the
// child tree (a process group + PR_SET_PDEATHSIG on unix; a Job Object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE on Windows). Spec itself carries only a
// script path, an environment, a timeout, and an output cap — there is no
// field through which a caller could supply an executable, an argument, or
// PowerShell source.
package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// Spec describes one process execution request.
type Spec struct {
	// Command is the absolute path to the approved capability script to run.
	// On unix it is invoked directly (never through a shell). On Windows it
	// is passed as the sole -File argument to a fixed powershell.exe
	// invocation. Either way it is the only variable element of the child
	// process's argument vector; there is no Spec field for extra arguments,
	// an alternate executable, or inline script source.
	Command string
	// Env is the complete environment passed to the child process. Callers
	// are responsible for constructing this from a validated, known set of
	// entries — this package does not filter or interpret it.
	Env []string
	// Timeout is the maximum wall-clock duration the process may run
	// before its entire process tree is killed and the job is marked failed
	// due to timeout.
	Timeout time.Duration
	// MaxOutputBytes caps how many bytes of stdout and of stderr are
	// retained, independently. Output beyond the cap is discarded (the
	// process itself is not throttled or blocked).
	MaxOutputBytes int
}

// TerminationGracePeriod is how long a timed-out or cancelled process tree
// is given to exit after the first (cooperative) stop request before Run
// escalates to a forced kill. This is a fixed internal constant, not
// user-configurable: it exists purely to give a well-behaved script a
// bounded chance to clean up (e.g. a partially applied change), not as a
// tunable product surface.
//
// It only takes effect where the platform has a cooperative stop signal.
// On unix that is SIGTERM-then-SIGKILL. On Windows a service has no console
// and therefore no way to deliver a Ctrl-Break equivalent, so the first
// stop request already terminates the Job Object; the grace window is then
// never actually waited on (Wait returns immediately).
const TerminationGracePeriod = 5 * time.Second

// processContainment isolates a spawned child process and every descendant
// it creates, so the whole tree can be stopped as a unit on timeout or
// cancellation and can never outlive an uncontrolled death of the Axiom
// process itself. One instance is used per Run call.
type processContainment interface {
	// prepare sets the platform SysProcAttr on cmd before it is started.
	prepare(cmd *exec.Cmd)
	// started is called once, immediately after a successful cmd.Start().
	// A non-nil error means containment could not be established and the
	// child must be killed rather than allowed to run uncontained.
	started(cmd *exec.Cmd) error
	// terminate requests a cooperative stop of the whole tree (unix:
	// SIGTERM to the process group; Windows: TerminateJobObject, which is
	// not cooperative but is the only tree-wide stop available to a
	// service).
	terminate(cmd *exec.Cmd)
	// kill forcibly kills the whole tree.
	kill(cmd *exec.Cmd)
	// release drops any OS resources held by the containment. It is always
	// called, exactly once, before Run returns.
	release()
}

// Result is the outcome of a completed (or killed) execution.
type Result struct {
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	TimedOut        bool
	Cancelled       bool
	StartedAt       time.Time
	FinishedAt      time.Time
}

// Duration is the wall-clock execution time.
func (r *Result) Duration() time.Duration {
	return r.FinishedAt.Sub(r.StartedAt)
}

// Run starts spec.Command and waits for it to finish, for ctx to be
// cancelled, or for spec.Timeout to elapse — whichever happens first. On
// timeout or cancellation the complete process tree is killed (not just the
// direct child) before Run returns.
func Run(ctx context.Context, spec Spec) (*Result, error) {
	if spec.MaxOutputBytes <= 0 {
		return nil, fmt.Errorf("executor: MaxOutputBytes must be positive")
	}

	cmd := buildCommand(spec)
	cmd.Env = spec.Env

	cont := newContainment()
	cont.prepare(cmd)
	defer cont.release()

	stdout := newCappedBuffer(spec.MaxOutputBytes)
	stderr := newCappedBuffer(spec.MaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	result := &Result{StartedAt: time.Now()}

	if err := cmd.Start(); err != nil {
		result.FinishedAt = time.Now()
		return nil, fmt.Errorf("starting process: %w", err)
	}

	if err := cont.started(cmd); err != nil {
		// Containment could not be established: kill the child and fail
		// closed rather than run a script whose descendants we cannot
		// reliably terminate.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		result.FinishedAt = time.Now()
		return nil, fmt.Errorf("establishing process containment: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()

	var runErr error
	select {
	case runErr = <-waitErr:
		// Process finished on its own before the deadline/cancellation.
	case <-timeoutCtx.Done():
		if ctx.Err() != nil {
			result.Cancelled = true
		} else {
			result.TimedOut = true
		}

		// Request a cooperative stop of the whole tree first, so a
		// well-behaved script gets a bounded chance to clean up (e.g. an
		// in-progress docker/compose operation) instead of always being
		// force-killed mid-step. A script that ignores the request, or a
		// tree still alive once TerminationGracePeriod elapses, is then
		// force-killed — this is a bound, not an indefinite grace period,
		// so a hung/ignoring script still cannot outlive the timeout by
		// more than TerminationGracePeriod. On Windows terminate already
		// kills the Job Object, so waitErr fires immediately and the grace
		// timer is never reached.
		cont.terminate(cmd)
		select {
		case <-waitErr:
			// Exited in response to the cooperative stop (or was already
			// force-killed, on Windows).
		case <-time.After(TerminationGracePeriod):
			cont.kill(cmd)
			<-waitErr // reap regardless; ignore the exit error from a killed process
		}
	}

	result.FinishedAt = time.Now()
	result.Stdout, result.StdoutTruncated = stdout.bytes()
	result.Stderr, result.StderrTruncated = stderr.bytes()

	if result.TimedOut || result.Cancelled {
		result.ExitCode = -1
		return result, nil
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("waiting for process: %w", runErr)
	}

	result.ExitCode = cmd.ProcessState.ExitCode()
	return result, nil
}

// cappedBuffer is an io.Writer that retains at most limit bytes and reports
// whether it discarded any beyond that.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		return len(p), nil // report full consumption; the process is not throttled
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.truncated = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

func (c *cappedBuffer) bytes() ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, c.buf.Len())
	copy(out, c.buf.Bytes())
	return out, c.truncated
}
