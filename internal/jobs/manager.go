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

	"axiom/internal/audit"
	"axiom/internal/config"
	"axiom/internal/executor"
)

// Sentinel errors returned by Manager.Trigger. The API layer maps these to
// HTTP status codes.
var (
	ErrUnknownAction    = fmt.Errorf("unknown action")
	ErrUnknownParameter = fmt.Errorf("unknown parameter")
	ErrInvalidParameter = fmt.Errorf("invalid parameter")
	ErrMissingParameter = fmt.Errorf("missing required parameter")
	ErrActionBusy       = fmt.Errorf("action is already running (exclusive concurrency)")
)

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

	mu       sync.Mutex
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
	return &Manager{
		cfg:            cfg,
		audit:          auditLogger,
		logger:         logger,
		maxOutputBytes: maxOutputBytes,
		maxHistory:     maxHistory,
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

// buildEnv constructs the child process environment from only the
// declared, validated parameters — never from arbitrary request data.
func buildEnv(jobID, actionName string, params map[string]string) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"AXIOM_JOB_ID=" + jobID,
		"AXIOM_ACTION=" + actionName,
	}
	for name, value := range params {
		envName := "AXIOM_PARAM_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		env = append(env, envName+"="+value)
	}
	return env
}

// Trigger validates and starts an action on behalf of identity. It returns
// the queued job immediately; execution happens asynchronously.
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

	job := &Job{
		ID:          NewID(),
		Action:      actionName,
		RequestedBy: identity,
		Parameters:  params,
		CreatedAt:   time.Now(),
		status:      StatusQueued,
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
		if lock != nil {
			lock.Unlock()
		}
		m.removeJob(job.ID)
		return nil, fmt.Errorf("audit log unavailable, refusing to start job: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	job.cancel = cancel

	go m.run(runCtx, job, action, lock)

	return job, nil
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

func (m *Manager) run(ctx context.Context, job *Job, action *config.Action, lock *sync.Mutex) {
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

	job.setRunning()
	if err := m.audit.Write(audit.Record{
		Event:    audit.EventStarted,
		JobID:    job.ID,
		Identity: job.RequestedBy,
		Action:   job.Action,
	}); err != nil {
		m.logger.Error("audit write failed for job start", "job_id", job.ID, "error", err)
	}

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
	}

	if result != nil {
		job.mu.Lock()
		job.stdout, job.stdoutTrunc = result.Stdout, result.StdoutTruncated
		job.stderr, job.stderrTrunc = result.Stderr, result.StderrTruncated
		job.mu.Unlock()
	}

	job.setFinished(status, exitCode, errMsg)
	unlock()
	snap := job.Snapshot()

	if err := m.audit.Write(audit.Record{
		Event:      audit.EventFinished,
		JobID:      job.ID,
		Identity:   job.RequestedBy,
		Action:     job.Action,
		Result:     string(status),
		Reason:     errMsg,
		DurationMs: snap.DurationMs,
	}); err != nil {
		m.logger.Error("audit write failed for job completion", "job_id", job.ID, "error", err)
	}
}
