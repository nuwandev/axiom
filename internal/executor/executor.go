// Package executor runs configured action scripts as child processes with
// enforced timeouts (full process-tree termination), bounded output
// capture, and cooperative cancellation.
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
	// Command is the absolute path to the script/binary to run. It is
	// invoked directly (never through a shell), so shell metacharacters in
	// arguments or environment values have no special meaning.
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

	cmd := exec.Command(spec.Command)
	cmd.Env = spec.Env
	configureProcessGroup(cmd)

	stdout := newCappedBuffer(spec.MaxOutputBytes)
	stderr := newCappedBuffer(spec.MaxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	result := &Result{StartedAt: time.Now()}

	if err := cmd.Start(); err != nil {
		result.FinishedAt = time.Now()
		return nil, fmt.Errorf("starting process: %w", err)
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
		killProcessGroup(cmd)
		<-waitErr // reap regardless; ignore the exit error from a killed process
		if ctx.Err() != nil {
			result.Cancelled = true
		} else {
			result.TimedOut = true
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
