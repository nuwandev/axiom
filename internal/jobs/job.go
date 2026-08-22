// Package jobs implements the async job state machine and the per-action
// concurrency policy (e.g. "exclusive") enforced locally on this agent.
package jobs

import (
	"sync"
	"time"
)

// Status is a job's lifecycle state.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Job is one execution of a configured action. All fields are accessed
// through the Manager's locking, never mutated directly by callers.
type Job struct {
	ID          string
	Action      string
	RequestedBy string
	Parameters  map[string]string

	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time

	mu          sync.Mutex
	status      Status
	exitCode    *int
	errMsg      string
	stdout      []byte
	stderr      []byte
	stdoutTrunc bool
	stderrTrunc bool

	cancel func()
}

// Snapshot is an immutable, race-safe read of a job's current state.
type Snapshot struct {
	ID          string
	Action      string
	RequestedBy string
	Status      Status
	ExitCode    *int
	Error       string
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
	DurationMs  int64
}

// Snapshot returns a race-safe copy of the job's current state.
func (j *Job) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()

	s := Snapshot{
		ID:          j.ID,
		Action:      j.Action,
		RequestedBy: j.RequestedBy,
		Status:      j.status,
		ExitCode:    j.exitCode,
		Error:       j.errMsg,
		CreatedAt:   j.CreatedAt,
		StartedAt:   j.StartedAt,
		FinishedAt:  j.FinishedAt,
	}
	if !s.StartedAt.IsZero() {
		end := s.FinishedAt
		if end.IsZero() {
			end = time.Now()
		}
		s.DurationMs = end.Sub(s.StartedAt).Milliseconds()
	}
	return s
}

func (j *Job) setRunning() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status = StatusRunning
	j.StartedAt = time.Now()
}

func (j *Job) setFinished(status Status, exitCode *int, errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status = status
	j.exitCode = exitCode
	j.errMsg = errMsg
	j.FinishedAt = time.Now()
}

// Cancel requests cooperative cancellation of a running job (propagated via
// context to the executor, which kills the process tree).
func (j *Job) Cancel() {
	j.mu.Lock()
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
