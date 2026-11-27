package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// restartReason is recorded on a job that was queued or running when the agent
// stopped. Its real outcome is unknown: the script may have finished, or been
// killed with the agent. The audit log is the place to look.
const restartReason = "interrupted: agent restarted before this job's result was recorded"

// EnableState turns on job state persistence at path. It must be called once,
// before the manager serves any request.
//
// It loads what the file holds, marks every job that was still queued or
// running as cancelled (see restartReason), rewrites the file so that is
// durable, and restores the jobs and unexpired idempotency keys into memory.
// Restored jobs carry no output: only their status is kept.
func (m *Manager) EnableState(path string) error {
	store, recs, err := openStateStore(path, m.maxHistory, time.Now)
	if err != nil {
		return err
	}

	now := time.Now()
	for i := range recs {
		r := &recs[i]
		if r.Type == recordJob && !r.Status.terminal() {
			r.Status = StatusCancelled
			r.Error = restartReason
			r.FinishedAt = now
		}
	}
	if err := store.rewrite(recs); err != nil {
		return err
	}

	m.mu.Lock()
	for _, r := range recs {
		if r.Type != recordJob {
			continue
		}
		m.jobs[r.ID] = &Job{
			ID:          r.ID,
			Action:      r.Action,
			RequestedBy: r.RequestedBy,
			CreatedAt:   r.CreatedAt,
			StartedAt:   r.StartedAt,
			FinishedAt:  r.FinishedAt,
			status:      r.Status,
			exitCode:    r.ExitCode,
			errMsg:      r.Error,
			restored:    true,
			interrupted: r.Error == restartReason,
		}
		m.elemOf[r.ID] = m.byInsert.PushBack(r.ID)
	}
	m.mu.Unlock()

	if err := m.restoreKeys(recs); err != nil {
		store.close()
		return err
	}
	m.state = store
	return nil
}

func (m *Manager) restoreKeys(recs []stateRecord) error {
	m.idem.mu.Lock()
	defer m.idem.mu.Unlock()
	for _, r := range recs {
		if r.Type != recordKey {
			continue
		}
		raw, err := hex.DecodeString(r.ParamsHash)
		var hash [sha256.Size]byte
		if err != nil || len(raw) != len(hash) {
			return fmt.Errorf("job state file: idempotency key for %q has an invalid parameter hash", r.Action)
		}
		copy(hash[:], raw)
		m.idem.entries[idempotencyKey{identity: r.RequestedBy, action: r.Action, key: r.Key}] = idempotencyEntry{
			paramsHash: hash,
			jobID:      r.JobID,
			expires:    r.Expires,
		}
	}
	return nil
}

// CloseState releases the state file. Call it after Shutdown, once every job
// has written its final record.
func (m *Manager) CloseState() error {
	if m.state == nil {
		return nil
	}
	return m.state.close()
}

// Restored reports whether the job was loaded from the state file rather than
// run by this process. Such a job has a status but no retained output.
func (m *Manager) Restored(jobID string) bool {
	m.mu.Lock()
	j, ok := m.jobs[jobID]
	m.mu.Unlock()
	return ok && j.restored
}

// persistJob records the job's current state. A failure is logged, not
// returned: the job has already been decided, the state file only exists so
// status survives a restart, and the audit log remains the durable record.
func (m *Manager) persistJob(job *Job) {
	if m.state == nil {
		return
	}
	s := job.Snapshot()
	err := m.state.append(stateRecord{
		Type:        recordJob,
		ID:          s.ID,
		Action:      s.Action,
		RequestedBy: s.RequestedBy,
		Status:      s.Status,
		ExitCode:    s.ExitCode,
		Error:       s.Error,
		CreatedAt:   s.CreatedAt,
		StartedAt:   s.StartedAt,
		FinishedAt:  s.FinishedAt,
	})
	if err != nil {
		m.logger.Error("persisting job state failed; this job's status will not survive a restart", "job_id", job.ID, "error", err)
	}
}

// persistKey records an idempotency key so a retry after a restart still
// finds the job it started.
func (m *Manager) persistKey(k idempotencyKey, e idempotencyEntry) {
	if m.state == nil {
		return
	}
	err := m.state.append(stateRecord{
		Type:        recordKey,
		RequestedBy: k.identity,
		Action:      k.action,
		Key:         k.key,
		ParamsHash:  hex.EncodeToString(e.paramsHash[:]),
		JobID:       e.jobID,
		Expires:     e.expires,
	})
	if err != nil {
		m.logger.Error("persisting idempotency key failed; a retry after a restart would run the action again", "job_id", e.jobID, "error", err)
	}
}
