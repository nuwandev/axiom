//go:build unix

package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/config"
)

// newLimitedManager builds a Manager with an explicit global job cap and
// returns the audit log path so tests can inspect what was recorded.
func newLimitedManager(t *testing.T, actions map[string]*config.Action, maxConcurrent int, sinks ...audit.Sink) (*Manager, string) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "audit.log")
	al, err := audit.Open(logPath, "test-agent", sinks...)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { al.Close() })
	cfg := &config.Config{AgentID: "test-agent", Actions: actions, MaxConcurrentJobs: maxConcurrent}
	return NewManager(cfg, al, testLogger(t)), logPath
}

func activeSlots(m *Manager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

func waitForSlots(t *testing.T, m *Manager, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if activeSlots(m) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active slots = %d, want %d (slot leaked?)", activeSlots(m), want)
}

func jobCount(m *Manager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}

// At the global cap a trigger is rejected with ErrTooManyJobs, creates no job,
// and does not leave the exclusive lock of the rejected action held. Once
// running jobs finish, every slot and lock is available again.
func TestManager_GlobalLimitRejectsWithoutLeakingLockOrJob(t *testing.T) {
	dir := t.TempDir()
	slow := writeScript(t, dir, "slow.sh", "sleep 1\n")
	quick := writeScript(t, dir, "quick.sh", "exit 0\n")
	m, _ := newLimitedManager(t, map[string]*config.Action{
		"slow":  {Name: "slow", Command: slow, Timeout: 10 * time.Second},
		"excl":  {Name: "excl", Command: quick, Timeout: 10 * time.Second, Concurrency: config.ConcurrencyExclusive},
		"other": {Name: "other", Command: quick, Timeout: 10 * time.Second},
	}, 2)

	var running []*Job
	for i := 0; i < 2; i++ {
		j, err := m.Trigger(context.Background(), "slow", "ci", nil)
		if err != nil {
			t.Fatalf("Trigger %d: %v", i, err)
		}
		running = append(running, j)
	}

	before := jobCount(m)
	for _, action := range []string{"slow", "other", "excl", "excl"} { // "excl" twice: its lock must not stay held
		if _, err := m.Trigger(context.Background(), action, "ci", nil); !errors.Is(err, ErrTooManyJobs) {
			t.Fatalf("Trigger(%s) at the cap = %v, want ErrTooManyJobs", action, err)
		}
	}
	if got := jobCount(m); got != before {
		t.Errorf("rejected triggers created %d job(s)", got-before)
	}

	for _, j := range running {
		waitForTerminal(t, m, j.ID, 10*time.Second)
	}
	waitForSlots(t, m, 0)

	j, err := m.Trigger(context.Background(), "excl", "ci", nil)
	if err != nil {
		t.Fatalf("Trigger(excl) after jobs finished: %v (lock or slot leaked)", err)
	}
	waitForTerminal(t, m, j.ID, 10*time.Second)
	waitForSlots(t, m, 0)
}

// Many simultaneous triggers admit exactly the configured number of jobs.
func TestManager_GlobalLimitHoldsUnderConcurrentTriggers(t *testing.T) {
	dir := t.TempDir()
	slow := writeScript(t, dir, "slow.sh", "sleep 1\n")
	const limit = 5
	m, _ := newLimitedManager(t, map[string]*config.Action{
		"slow": {Name: "slow", Command: slow, Timeout: 10 * time.Second},
	}, limit)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted []*Job
		rejected int
	)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := m.Trigger(context.Background(), "slow", "ci", nil)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted = append(accepted, j)
			case errors.Is(err, ErrTooManyJobs):
				rejected++
			default:
				t.Errorf("unexpected Trigger error: %v", err)
			}
		}()
	}
	wg.Wait()

	if len(accepted) != limit || rejected != 40-limit {
		t.Fatalf("accepted=%d rejected=%d, want %d and %d", len(accepted), rejected, limit, 40-limit)
	}
	for _, j := range accepted {
		waitForTerminal(t, m, j.ID, 15*time.Second)
	}
	waitForSlots(t, m, 0)
}

// failOnStarted accepts every record except the "started" one, modelling a
// collector that goes away between the accept and the start of a job.
type failOnStarted struct{}

func (failOnStarted) WriteRecord(b []byte) error {
	if strings.Contains(string(b), `"started"`) {
		return errors.New("collector unreachable")
	}
	return nil
}
func (failOnStarted) Close() error { return nil }

// If the started record cannot be written, the script is never executed, the
// job is failed with a clear reason, and the lock and slot are released.
func TestManager_StartedAuditFailureDoesNotRunAction(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := writeScript(t, dir, "mark.sh", "touch "+marker+"\n")
	m, _ := newLimitedManager(t, map[string]*config.Action{
		"deploy": {Name: "deploy", Command: script, Timeout: 5 * time.Second, Concurrency: config.ConcurrencyExclusive},
	}, 4, failOnStarted{})

	for i := 0; i < 2; i++ {
		// The second iteration proves the exclusive lock was released.
		job, err := m.Trigger(context.Background(), "deploy", "ci", nil)
		if err != nil {
			t.Fatalf("Trigger %d: %v (accept record is deliverable, so the job is accepted)", i, err)
		}
		snap := waitForTerminal(t, m, job.ID, 5*time.Second)
		if snap.Status != StatusFailed || !strings.Contains(snap.Error, "audit log unavailable") {
			t.Fatalf("job = %+v, want failed with an audit-unavailable reason", snap)
		}
	}
	waitForSlots(t, m, 0)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the action script ran although its started record could not be audited")
	}
}

// Shutdown stops running jobs (process group included), records them as
// interrupted, refuses new jobs, and leaves no slot or lock behind.
func TestManager_ShutdownInterruptsRunningJobsAndAuditsThem(t *testing.T) {
	dir := t.TempDir()
	hang := writeScript(t, dir, "hang.sh", "sleep 30\n")
	m, logPath := newLimitedManager(t, map[string]*config.Action{
		"deploy": {Name: "deploy", Command: hang, Timeout: 60 * time.Second, Concurrency: config.ConcurrencyExclusive},
	}, 4)

	job, err := m.Trigger(context.Background(), "deploy", "ci", nil)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	for i := 0; i < 100; i++ { // wait until it is actually running
		if snap, _ := m.Get(job.ID); snap.Status == StatusRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if time.Since(start) > 15*time.Second {
		t.Errorf("Shutdown took %s", time.Since(start))
	}

	snap, _ := m.Get(job.ID)
	if snap.Status != StatusCancelled || !strings.Contains(snap.Error, "shutting down") {
		t.Errorf("job = %+v, want cancelled/interrupted", snap)
	}
	if _, err := m.Trigger(context.Background(), "deploy", "ci", nil); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Trigger after Shutdown = %v, want ErrShuttingDown", err)
	}
	if got := activeSlots(m); got != 0 {
		t.Errorf("active slots = %d after Shutdown", got)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, `"finished"`) || !strings.Contains(log, "interrupted: agent shutting down") {
		t.Errorf("audit log has no interrupted finished record:\n%s", log)
	}
}
