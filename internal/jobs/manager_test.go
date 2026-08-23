//go:build unix

package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"axiom/internal/audit"
	"axiom/internal/config"
	"log/slog"
)

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return p
}

func newTestManager(t *testing.T, actions map[string]*config.Action) *Manager {
	dir := t.TempDir()
	al, err := audit.Open(filepath.Join(dir, "audit.log"), "test-agent")
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { al.Close() })

	cfg := &config.Config{
		AgentID: "test-agent",
		Actions: actions,
	}
	return NewManager(cfg, al, testLogger(t))
}

func newTestManagerWithHistory(t *testing.T, actions map[string]*config.Action, maxHistory int) *Manager {
	dir := t.TempDir()
	al, err := audit.Open(filepath.Join(dir, "audit.log"), "test-agent")
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { al.Close() })

	cfg := &config.Config{
		AgentID:       "test-agent",
		Actions:       actions,
		MaxJobHistory: maxHistory,
	}
	return NewManager(cfg, al, testLogger(t))
}

func waitForTerminal(t *testing.T, m *Manager, jobID string, timeout time.Duration) Snapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snap, ok := m.Get(jobID)
		if !ok {
			t.Fatalf("job %s disappeared", jobID)
		}
		switch snap.Status {
		case StatusSucceeded, StatusFailed, StatusCancelled:
			return snap
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach a terminal state within %s", jobID, timeout)
	return Snapshot{}
}

func TestManager_TriggerSuccess(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "ok.sh", "exit 0\n")
	m := newTestManager(t, map[string]*config.Action{
		"noop": {Name: "noop", Command: script, Timeout: 5 * time.Second, Concurrency: config.ConcurrencyShared},
	})

	job, err := m.Trigger(context.Background(), "noop", "ci-jenkins", nil)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	snap := waitForTerminal(t, m, job.ID, 3*time.Second)
	if snap.Status != StatusSucceeded {
		t.Errorf("Status = %v, want succeeded", snap.Status)
	}
}

func TestManager_UnknownAction(t *testing.T) {
	m := newTestManager(t, map[string]*config.Action{})
	_, err := m.Trigger(context.Background(), "nope", "ci-jenkins", nil)
	if err == nil {
		t.Fatalf("expected error for unknown action")
	}
}

func TestManager_ParameterValidation(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "deploy.sh", "exit 0\n")
	action := &config.Action{
		Name:    "deploy",
		Command: script,
		Timeout: 5 * time.Second,
		Parameters: map[string]config.Parameter{
			"image_tag": {Type: config.ParameterTypeString, Pattern: `^[a-z0-9-]+$`, Required: true},
		},
	}
	// Parameter.Validate normally runs as part of config.Load; call it
	// directly here since this test builds the config in-memory.
	p := action.Parameters["image_tag"]
	if err := p.Validate("image_tag"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	action.Parameters["image_tag"] = p

	m := newTestManager(t, map[string]*config.Action{"deploy": action})

	if _, err := m.Trigger(context.Background(), "deploy", "ci-jenkins", map[string]string{"unexpected": "x"}); err == nil {
		t.Errorf("expected error for undeclared parameter")
	}
	if _, err := m.Trigger(context.Background(), "deploy", "ci-jenkins", nil); err == nil {
		t.Errorf("expected error for missing required parameter")
	}
	if _, err := m.Trigger(context.Background(), "deploy", "ci-jenkins", map[string]string{"image_tag": "; rm -rf /"}); err == nil {
		t.Errorf("expected error for value not matching pattern (shell metacharacters)")
	}
	job, err := m.Trigger(context.Background(), "deploy", "ci-jenkins", map[string]string{"image_tag": "uat-20260823"})
	if err != nil {
		t.Fatalf("Trigger with valid parameter: %v", err)
	}
	waitForTerminal(t, m, job.ID, 3*time.Second)
}

func TestManager_ExclusiveConcurrencyRejectsSecondTrigger(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "slow.sh", "sleep 1\n")
	m := newTestManager(t, map[string]*config.Action{
		"deploy": {Name: "deploy", Command: script, Timeout: 5 * time.Second, Concurrency: config.ConcurrencyExclusive},
	})

	job1, err := m.Trigger(context.Background(), "deploy", "ci-jenkins", nil)
	if err != nil {
		t.Fatalf("first Trigger: %v", err)
	}

	// Give the first job a moment to actually acquire the lock and start.
	time.Sleep(100 * time.Millisecond)

	_, err = m.Trigger(context.Background(), "deploy", "ci-jenkins", nil)
	if err != ErrActionBusy {
		t.Fatalf("second Trigger: got %v, want ErrActionBusy", err)
	}

	snap := waitForTerminal(t, m, job1.ID, 5*time.Second)
	if snap.Status != StatusSucceeded {
		t.Errorf("first job status = %v", snap.Status)
	}

	// Now that the first job is done, the lock should be free again.
	job3, err := m.Trigger(context.Background(), "deploy", "ci-jenkins", nil)
	if err != nil {
		t.Fatalf("third Trigger after lock release: %v", err)
	}
	waitForTerminal(t, m, job3.ID, 3*time.Second)
}

func TestManager_TimeoutMarksJobFailed(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "hang.sh", "sleep 30\n")
	m := newTestManager(t, map[string]*config.Action{
		"deploy": {Name: "deploy", Command: script, Timeout: 200 * time.Millisecond, Concurrency: config.ConcurrencyShared},
	})

	job, err := m.Trigger(context.Background(), "deploy", "ci-jenkins", nil)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	snap := waitForTerminal(t, m, job.ID, 5*time.Second)
	if snap.Status != StatusFailed {
		t.Errorf("Status = %v, want failed (timeout)", snap.Status)
	}
}

func TestManager_JobResultsRaceSafe(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "ok.sh", "exit 0\n")
	m := newTestManager(t, map[string]*config.Action{
		"noop": {Name: "noop", Command: script, Timeout: 5 * time.Second, Concurrency: config.ConcurrencyShared},
	})

	job, err := m.Trigger(context.Background(), "noop", "ci-jenkins", nil)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	// Concurrently poll Get/Logs while the job is running/finishing — run
	// with -race to catch any unsynchronized access.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			m.Get(job.ID)
			m.Logs(job.ID)
		}
	}()
	waitForTerminal(t, m, job.ID, 3*time.Second)
	<-done
}

func TestManager_BoundedHistoryEvictsOldestTerminalJob(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "ok.sh", "exit 0\n")
	const maxHistory = 3
	m := newTestManagerWithHistory(t, map[string]*config.Action{
		"noop": {Name: "noop", Command: script, Timeout: 5 * time.Second, Concurrency: config.ConcurrencyShared},
	}, maxHistory)

	var firstJobID string
	for i := 0; i < maxHistory+2; i++ {
		job, err := m.Trigger(context.Background(), "noop", "ci-jenkins", nil)
		if err != nil {
			t.Fatalf("Trigger %d: %v", i, err)
		}
		waitForTerminal(t, m, job.ID, 3*time.Second)
		if i == 0 {
			firstJobID = job.ID
		}
	}

	if _, ok := m.Get(firstJobID); ok {
		t.Errorf("expected the earliest job to have been evicted once history exceeded %d", maxHistory)
	}

	m.mu.Lock()
	stored := len(m.jobs)
	m.mu.Unlock()
	if stored > maxHistory {
		t.Errorf("stored job count = %d, want <= %d", stored, maxHistory)
	}
}

func TestManager_MaxOutputBytesFromConfigIsHonored(t *testing.T) {
	dir := t.TempDir()
	al, err := audit.Open(filepath.Join(dir, "audit.log"), "test-agent")
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { al.Close() })

	script := writeScript(t, dir, "big.sh", "for i in $(seq 1 5000); do echo 0123456789; done\n")
	cfg := &config.Config{
		AgentID: "test-agent",
		Actions: map[string]*config.Action{
			"noop": {Name: "noop", Command: script, Timeout: 5 * time.Second, Concurrency: config.ConcurrencyShared},
		},
		MaxOutputBytes: 128,
	}
	m := NewManager(cfg, al, testLogger(t))

	job, err := m.Trigger(context.Background(), "noop", "ci-jenkins", nil)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForTerminal(t, m, job.ID, 3*time.Second)

	stdout, _, stdoutTrunc, _, ok := m.Logs(job.ID)
	if !ok {
		t.Fatalf("expected logs to be found")
	}
	if len(stdout) != 128 {
		t.Errorf("len(stdout) = %d, want 128 (config output.max_bytes)", len(stdout))
	}
	if !stdoutTrunc {
		t.Errorf("expected stdout to be marked truncated")
	}
}
