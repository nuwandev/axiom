package jobs

import (
	"container/list"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/config"
	"github.com/nuwandev/axiom/internal/executor"
)

// Sentinel errors returned by Manager.Trigger. The API layer maps these to
// HTTP status codes.
var (
	ErrUnknownAction    = fmt.Errorf("unknown action")
	ErrUnknownParameter = fmt.Errorf("unknown parameter")
	ErrInvalidParameter = fmt.Errorf("invalid parameter")
	ErrMissingParameter = fmt.Errorf("missing required parameter")
	ErrActionBusy       = fmt.Errorf("action is already running (exclusive concurrency)")
	// ErrTooManyJobs means the agent-wide cap on queued+running jobs
	// (jobs.max_concurrent) has been reached. No job is created.
	ErrTooManyJobs = fmt.Errorf("too many jobs are running; try again later")
	// ErrShuttingDown means the agent is stopping and accepts no new jobs.
	ErrShuttingDown = fmt.Errorf("agent is shutting down")
)

// DefaultMaxConcurrent is the agent-wide cap on queued+running jobs when the
// caller doesn't set cfg.MaxConcurrentJobs (e.g. config built directly in
// tests).
const DefaultMaxConcurrent = 16

// interruptedReason is recorded on jobs stopped because the agent is
// shutting down.
const interruptedReason = "interrupted: agent shutting down"

// MaxOutputBytesDefault is used by callers (tests, mainly) that don't wire a
// config-derived limit through NewManager's cfg argument.
const MaxOutputBytesDefault = 2 * 1024 * 1024

// DefaultMaxJobHistory bounds in-memory job retention when the caller
// doesn't set cfg.MaxJobHistory (e.g. config built directly in tests).
const DefaultMaxJobHistory = 1000

// Manager owns the in-memory job store, per-action exclusive-run locks, and
// wires job execution to the executor and audit packages.
//
// The job store is memory-only and bounded: it exists so a caller can poll
// recent job status/logs, not as a durable execution record. It is dropped
// on process restart, and once more than maxHistory jobs have been created
// the oldest *terminal* job is evicted to make room for a new one — a
// currently running job is never evicted. The audit log (internal/audit) is
// the durable, restart-surviving record of what ran.
type Manager struct {
	cfg    *config.Config
	audit  *audit.Logger
	logger *slog.Logger

	maxOutputBytes int
	maxHistory     int
	maxConcurrent  int

	mu       sync.Mutex
	active   int            // queued+running jobs holding a slot; guarded by mu
	stopping bool           // set by Shutdown; guarded by mu
	running  sync.WaitGroup // one count per held slot
	jobs     map[string]*Job
	byInsert *list.List // list.Element.Value is a jobID (string), oldest at Front
	elemOf   map[string]*list.Element

	exclusiveMu sync.Mutex
	exclusive   map[string]*sync.Mutex
}

// NewManager builds a job manager bound to cfg's actions.
func NewManager(cfg *config.Config, auditLogger *audit.Logger, logger *slog.Logger) *Manager {
	maxOutputBytes := cfg.MaxOutputBytes
	if maxOutputBytes == 0 {
		maxOutputBytes = MaxOutputBytesDefault
	}
	maxHistory := cfg.MaxJobHistory
	if maxHistory == 0 {
		maxHistory = DefaultMaxJobHistory
	}
	maxConcurrent := cfg.MaxConcurrentJobs
	if maxConcurrent == 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	return &Manager{
		cfg:            cfg,
		audit:          auditLogger,
		logger:         logger,
		maxOutputBytes: maxOutputBytes,
		maxHistory:     maxHistory,
		maxConcurrent:  maxConcurrent,
		jobs:           make(map[string]*Job),
		byInsert:       list.New(),
		elemOf:         make(map[string]*list.Element),
		exclusive:      make(map[string]*sync.Mutex),
	}
}

// Get returns a race-safe snapshot of a job's current state.
func (m *Manager) Get(jobID string) (Snapshot, bool) {
	m.mu.Lock()
	j, ok := m.jobs[jobID]
	m.mu.Unlock()
	if !ok {
		return Snapshot{}, false
	}
	return j.Snapshot(), true
}

// Logs returns the captured stdout/stderr for a job, if it has run.
func (m *Manager) Logs(jobID string) (stdout, stderr []byte, stdoutTruncated, stderrTruncated bool, found bool) {
	m.mu.Lock()
	j, ok := m.jobs[jobID]
	m.mu.Unlock()
	if !ok {
		return nil, nil, false, false, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stdout, j.stderr, j.stdoutTrunc, j.stderrTrunc, true
}

// recordJob inserts job into the store and, if that pushes the store over
// maxHistory, evicts the oldest job currently in a terminal state. If every
// stored job is still running/queued (all below maxHistory concurrently
// active — an operational anomaly, not expected in practice) the store is
// allowed to exceed maxHistory rather than evict live work.
func (m *Manager) recordJob(job *Job) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.jobs[job.ID] = job
	m.elemOf[job.ID] = m.byInsert.PushBack(job.ID)

	if len(m.jobs) <= m.maxHistory {
		return
	}
	for e := m.byInsert.Front(); e != nil; e = e.Next() {
		id := e.Value.(string)
		j, ok := m.jobs[id]
		if !ok {
			continue
		}
		if !j.Snapshot().Status.terminal() {
			continue
		}
		delete(m.jobs, id)
		delete(m.elemOf, id)
		m.byInsert.Remove(e)
		return
	}
}

// validateParameters checks requested params against the action's declared
// schema: unknown names are rejected, required-but-missing names are
// rejected, and every provided value must match its declared pattern.
func validateParameters(action *config.Action, requested map[string]string) error {
	for name := range requested {
		if _, ok := action.Parameters[name]; !ok {
			return fmt.Errorf("%w: %q is not declared for this action", ErrUnknownParameter, name)
		}
	}
	for name, p := range action.Parameters {
		value, provided := requested[name]
		if !provided {
			if p.Required {
				return fmt.Errorf("%w: %q", ErrMissingParameter, name)
			}
			continue
		}
		if err := p.CheckValue(value); err != nil {
			return fmt.Errorf("%w: %q: %v", ErrInvalidParameter, name, err)
		}
	}
	return nil
}

// buildEnv constructs the child process environment: a fixed,
// platform-specific base (see baseEnv) plus the job/action identifiers and
// the declared, validated parameters — never arbitrary request data, and
// never the Axiom process's own inherited environment.
func buildEnv(jobID, actionName string, params map[string]string) []string {
	env := append(baseEnv(),
		"AXIOM_JOB_ID="+jobID,
		"AXIOM_ACTION="+actionName,
	)
	for name, value := range params {
		envName := "AXIOM_PARAM_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		env = append(env, envName+"="+value)
	}
	return env
}

// acquireSlot reserves one of the agent-wide job slots. Every successful
// call must be paired with exactly one releaseSlot.
func (m *Manager) acquireSlot() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return ErrShuttingDown
	}
	if m.active >= m.maxConcurrent {
		return ErrTooManyJobs
	}
	m.active++
	m.running.Add(1)
	return nil
}

func (m *Manager) releaseSlot() {
	m.mu.Lock()
	m.active--
	m.mu.Unlock()
	m.running.Done()
}

// Trigger validates and starts an action on behalf of identity. It returns
// the queued job immediately; execution happens asynchronously. A rejected
// trigger (busy, over the global cap, shutting down, audit unavailable)
// creates no job and leaves no lock or slot held.
func (m *Manager) Trigger(ctx context.Context, actionName, identity string, params map[string]string) (*Job, error) {
	action, ok := m.cfg.Actions[actionName]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAction, actionName)
	}
	if err := validateParameters(action, params); err != nil {
		return nil, err
	}

	var lock *sync.Mutex
	if action.Concurrency == config.ConcurrencyExclusive {
		lock = m.lockFor(actionName)
		if !lock.TryLock() {
			return nil, ErrActionBusy
		}
	}
	unlockLock := func() {
		if lock != nil {
			lock.Unlock()
		}
	}

	if err := m.acquireSlot(); err != nil {
		unlockLock()
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	job := &Job{
		ID:          NewID(),
		Action:      actionName,
		RequestedBy: identity,
		Parameters:  params,
		CreatedAt:   time.Now(),
		status:      StatusQueued,
		cancel:      cancel,
	}

	m.recordJob(job)

	if err := m.audit.Write(audit.Record{
		Event:      audit.EventAccepted,
		JobID:      job.ID,
		Identity:   identity,
		Action:     actionName,
		Parameters: params,
	}); err != nil {
		// The accept-audit write failed: we cannot guarantee traceability
		// for this job, so refuse to start it rather than run an
		// unaudited action.
		cancel()
		m.removeJob(job.ID)
		m.releaseSlot()
		unlockLock()
		return nil, fmt.Errorf("audit log unavailable, refusing to start job: %w", err)
	}

	go m.run(runCtx, cancel, job, action, lock)

	return job, nil
}

// Shutdown stops accepting new jobs, asks every running job to stop (the
// executor terminates the whole process group), and waits for them to be
// reaped and their final audit record written, until ctx expires. It returns
// an error if jobs were still running when ctx expired. A crash, SIGKILL or
// power loss skips this path entirely: those jobs then have no finished
// record (see docs/THREAT-MODEL.md).
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopping = true
	var live []*Job
	for _, j := range m.jobs {
		if !j.Snapshot().Status.terminal() {
			live = append(live, j)
		}
	}
	m.mu.Unlock()

	for _, j := range live {
		j.Cancel()
	}

	done := make(chan struct{})
	go func() {
		m.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("shutdown: jobs still running: %w", ctx.Err())
	}
}

func (m *Manager) isStopping() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopping
}

// BeginShutdown stops accepting new jobs. Call it as soon as a stop is
// requested, before the slower HTTP drain, so a job that dies from the
// stop's own signals is attributed to the shutdown. Shutdown (which also
// cancels and waits) may follow.
func (m *Manager) BeginShutdown() {
	m.mu.Lock()
	m.stopping = true
	m.mu.Unlock()
}

// finishWithoutRunning records a terminal state for a job whose script was
// never executed.
func (m *Manager) finishWithoutRunning(job *Job, status Status, reason string) {
	job.setFinished(status, nil, reason)
}

// writeFinished emits the finished audit record. A failure is logged (the
// job's outcome is already decided) and cannot be retried.
func (m *Manager) writeFinished(job *Job, status Status, reason string) {
	snap := job.Snapshot()
	if err := m.audit.Write(audit.Record{
		Event:      audit.EventFinished,
		JobID:      job.ID,
		Identity:   job.RequestedBy,
		Action:     job.Action,
		Result:     string(status),
		Reason:     reason,
		DurationMs: snap.DurationMs,
	}); err != nil {
		m.logger.Error("audit write failed for job completion", "job_id", job.ID, "error", err)
	}
}

func (m *Manager) removeJob(jobID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, jobID)
	if e, ok := m.elemOf[jobID]; ok {
		m.byInsert.Remove(e)
		delete(m.elemOf, jobID)
	}
}

func (m *Manager) lockFor(action string) *sync.Mutex {
	m.exclusiveMu.Lock()
	defer m.exclusiveMu.Unlock()
	l, ok := m.exclusive[action]
	if !ok {
		l = &sync.Mutex{}
		m.exclusive[action] = l
	}
	return l
}

func (m *Manager) run(ctx context.Context, cancel context.CancelFunc, job *Job, action *config.Action, lock *sync.Mutex) {
	// Whatever happens below, release the job's context, its global slot and
	// (via unlock) its exclusive lock exactly once. Registered first so it
	// runs last, after the final audit record is written.
	defer m.releaseSlot()
	defer cancel()

	// The exclusive lock protects overlapping script executions, not the
	// bookkeeping after execution finishes — release it as soon as the job
	// reaches a terminal state so a subsequent trigger isn't blocked behind
	// this job's audit write. A sync.Once guards against a double-unlock
	// if the deferred release below also fires.
	var unlockOnce sync.Once
	unlock := func() {
		if lock != nil {
			unlockOnce.Do(lock.Unlock)
		}
	}
	defer unlock()

	// A goroutine panic is NOT recoverable by any other goroutine — left
	// unhandled it takes down the entire agent process, killing every other
	// in-flight and future job. Recover here, record the job as failed with
	// the panic detail, and always emit the finished-audit record so the
	// failure is never silent.
	defer func() {
		if r := recover(); r != nil {
			job.setFinished(StatusFailed, nil, fmt.Sprintf("internal panic: %v", r))
			m.logger.Error("recovered panic in job execution", "job_id", job.ID, "action", job.Action, "panic", r, "stack", string(debug.Stack()))
			if err := m.audit.Write(audit.Record{
				Event:    audit.EventFinished,
				JobID:    job.ID,
				Identity: job.RequestedBy,
				Action:   job.Action,
				Result:   string(StatusFailed),
				Reason:   fmt.Sprintf("internal panic: %v", r),
			}); err != nil {
				m.logger.Error("audit write failed for panic-recovery job completion", "job_id", job.ID, "error", err)
			}
		}
	}()

	// Stopped before it ever started (agent shutting down): record it, do not
	// execute anything.
	if ctx.Err() != nil {
		m.finishWithoutRunning(job, StatusCancelled, interruptedReason)
		unlock()
		m.writeFinished(job, StatusCancelled, interruptedReason)
		return
	}

	// The started record must be written before the script runs. If it cannot
	// be, the action is not executed: an unaudited execution is worse than a
	// failed job.
	if err := m.audit.Write(audit.Record{
		Event:    audit.EventStarted,
		JobID:    job.ID,
		Identity: job.RequestedBy,
		Action:   job.Action,
	}); err != nil {
		m.logger.Error("audit write failed for job start; action not executed", "job_id", job.ID, "error", err)
		const reason = "audit log unavailable; action not started"
		m.finishWithoutRunning(job, StatusFailed, reason)
		unlock()
		m.writeFinished(job, StatusFailed, reason)
		return
	}
	job.setRunning()

	env := buildEnv(job.ID, job.Action, job.Parameters)
	result, execErr := executor.Run(ctx, executor.Spec{
		Command:        action.Command,
		Env:            env,
		Timeout:        action.Timeout,
		MaxOutputBytes: m.maxOutputBytes,
	})

	var status Status
	var exitCode *int
	var errMsg string

	switch {
	case execErr != nil:
		status = StatusFailed
		errMsg = execErr.Error()
	case result.Cancelled:
		status = StatusCancelled
		errMsg = interruptedReason
	case result.TimedOut:
		status = StatusFailed
		errMsg = fmt.Sprintf("action exceeded timeout of %s", action.Timeout)
	case result.ExitCode == 0:
		status = StatusSucceeded
		ec := result.ExitCode
		exitCode = &ec
	default:
		status = StatusFailed
		ec := result.ExitCode
		exitCode = &ec
		if result.Signal != "" {
			// Killed from outside Axiom's own timeout/cancel path, e.g. systemd
			// signalling the whole control group at the same moment it stops
			// Axiom. Say so in the audit record instead of a bare failure.
			errMsg = "terminated by signal: " + result.Signal
			if m.isStopping() {
				errMsg = interruptedReason + " (" + errMsg + ")"
			}
		}
	}

	if result != nil {
		job.mu.Lock()
		job.stdout, job.stdoutTrunc = result.Stdout, result.StdoutTruncated
		job.stderr, job.stderrTrunc = result.Stderr, result.StderrTruncated
		job.mu.Unlock()
	}

	job.setFinished(status, exitCode, errMsg)
	unlock()
	m.writeFinished(job, status, errMsg)
}
