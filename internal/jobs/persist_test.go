//go:build unix

package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/config"
)

// newStateManager builds a manager that persists to statePath, as the agent
// does on start. Calling it twice with the same statePath simulates a restart.
func newStateManager(t *testing.T, dir, statePath string, maxHistory int, actions map[string]*config.Action) *Manager {
	t.Helper()
	al, err := audit.Open(filepath.Join(dir, "audit-"+t.Name()+time.Now().Format("150405.000000")+".log"), "test-agent")
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { al.Close() })
	cfg := &config.Config{AgentID: "test-agent", Actions: actions, MaxJobHistory: maxHistory}
	m := NewManager(cfg, al, testLogger(t))
	if err := m.EnableState(statePath); err != nil {
		t.Fatalf("EnableState: %v", err)
	}
	t.Cleanup(func() { m.CloseState() })
	return m
}

func stateActions(t *testing.T, dir string) map[string]*config.Action {
	t.Helper()
	ok := writeScript(t, dir, "ok.sh", "echo hello-output\nexit 0\n")
	fail := writeScript(t, dir, "fail.sh", "exit 7\n")
	// A manager built without config.Load needs the pattern compiled by hand.
	tag := config.Parameter{Type: config.ParameterTypeString, Pattern: ".+"}
	if err := tag.Validate("tag"); err != nil {
		t.Fatal(err)
	}
	return map[string]*config.Action{
		"ok":    {Name: "ok", Command: ok, Timeout: 10 * time.Second},
		"fail":  {Name: "fail", Command: fail, Timeout: 10 * time.Second},
		"param": {Name: "param", Command: ok, Timeout: 10 * time.Second, Parameters: map[string]config.Parameter{"tag": tag}},
	}
}

func TestState_FinishedJobsSurviveARestartWithStatusButNoOutput(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "jobs.state")
	actions := stateActions(t, dir)

	first := newStateManager(t, dir, statePath, 100, actions)
	okJob, err := first.Trigger(context.Background(), "ok", "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	failJob, err := first.Trigger(context.Background(), "fail", "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := waitForTerminal(t, first, okJob.ID, 10*time.Second)
	waitForTerminal(t, first, failJob.ID, 10*time.Second)
	if first.Restored(okJob.ID) {
		t.Fatal("a job this process ran must not be marked restored")
	}
	first.CloseState()

	second := newStateManager(t, dir, statePath, 100, actions)
	after, ok := second.Get(okJob.ID)
	if !ok {
		t.Fatal("finished job was not restored")
	}
	if after.Status != StatusSucceeded || after.RequestedBy != "ci" || after.Action != "ok" || !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("restored = %+v, want the same job as before the restart (%+v)", after, before)
	}
	failed, _ := second.Get(failJob.ID)
	if failed.Status != StatusFailed || failed.ExitCode == nil || *failed.ExitCode != 7 {
		t.Fatalf("restored failed job = %+v, want failed with exit code 7", failed)
	}
	if !second.Restored(okJob.ID) {
		t.Fatal("restored job must be flagged so the API can say its output is gone")
	}
}

func TestState_NeverWritesOutputOrParameterValues(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "jobs.state")
	m := newStateManager(t, dir, statePath, 100, stateActions(t, dir))

	job, err := m.Trigger(context.Background(), "param", "ci", map[string]string{"tag": "s3cret-value-xyz"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTerminal(t, m, job.ID, 10*time.Second)
	m.CloseState()

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"s3cret-value-xyz", "hello-output"} {
		if strings.Contains(string(data), leaked) {
			t.Fatalf("state file contains %q:\n%s", leaked, data)
		}
	}
}

func TestState_AJobInFlightAtARestartComesBackCancelledAndStaysThatWay(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "jobs.state")
	running := jobRec(idA, StatusRunning, 1)
	queued := jobRec(idB, StatusQueued, 2)
	finished := jobRec(idC, StatusSucceeded, 3)
	var content strings.Builder
	for _, r := range []stateRecord{running, queued, finished} {
		content.WriteString(string(mustJSON(t, r)) + "\n")
	}
	if err := os.WriteFile(statePath, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	m := newStateManager(t, dir, statePath, 100, stateActions(t, dir))
	for _, id := range []string{idA, idB} {
		snap, ok := m.Get(id)
		if !ok || snap.Status != StatusCancelled || snap.Error != restartReason || snap.FinishedAt.IsZero() {
			t.Fatalf("job %s = %+v (found %v), want cancelled with the restart reason", id, snap, ok)
		}
		if snap.DurationMs != 0 {
			t.Fatalf("job %s reports a duration of %dms, but how long it ran before the restart is unknown", id, snap.DurationMs)
		}
	}
	if snap, _ := m.Get(idC); snap.Status != StatusSucceeded {
		t.Fatalf("a finished job must be left alone, got %s", snap.Status)
	}
	m.CloseState()

	// The change is durable: a further restart sees cancelled, not running.
	again := newStateManager(t, dir, statePath, 100, stateActions(t, dir))
	if snap, _ := again.Get(idA); snap.Status != StatusCancelled || snap.Error != restartReason {
		t.Fatalf("after a second restart: %+v", snap)
	}
}

func TestState_IdempotencyKeySurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "jobs.state")
	actions := stateActions(t, dir)

	first := newStateManager(t, dir, statePath, 100, actions)
	id, replayed, err := first.TriggerIdempotent(context.Background(), "ok", "ci", "deploy-9", nil)
	if err != nil || replayed {
		t.Fatalf("first: id=%q replayed=%v err=%v", id, replayed, err)
	}
	waitForTerminal(t, first, id, 10*time.Second)
	first.CloseState()

	second := newStateManager(t, dir, statePath, 100, actions)
	again, replayed, err := second.TriggerIdempotent(context.Background(), "ok", "ci", "deploy-9", nil)
	if err != nil || !replayed || again != id {
		t.Fatalf("after restart: id=%q replayed=%v err=%v, want a replay of %q", again, replayed, err, id)
	}
	if n := jobCount(second); n != 1 {
		t.Fatalf("jobs = %d, want 1: the retry must not run the action again", n)
	}
	if _, _, err := second.TriggerIdempotent(context.Background(), "ok", "ci", "deploy-9", map[string]string{"x": "y"}); err == nil {
		t.Fatal("the restored key must still reject different parameters")
	}
}

func TestState_RestoredHistoryIsBounded(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "jobs.state")
	actions := stateActions(t, dir)

	first := newStateManager(t, dir, statePath, 100, actions)
	for i := 0; i < 6; i++ {
		job, err := first.Trigger(context.Background(), "ok", "ci", nil)
		if err != nil {
			t.Fatal(err)
		}
		waitForTerminal(t, first, job.ID, 10*time.Second)
	}
	first.CloseState()

	second := newStateManager(t, dir, statePath, 3, actions)
	if n := jobCount(second); n != 3 {
		t.Fatalf("restored %d jobs, want the newest 3 (jobs.max_history)", n)
	}
}

func TestState_ADamagedFileStopsTheAgentFromStartingOnIt(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "jobs.state")
	good := string(mustJSON(t, jobRec(idA, StatusSucceeded, 1)))
	if err := os.WriteFile(statePath, []byte(good+"\ngarbage\n"+good+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	al, err := audit.Open(filepath.Join(dir, "audit.log"), "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer al.Close()
	m := NewManager(&config.Config{AgentID: "test-agent", Actions: stateActions(t, dir)}, al, testLogger(t))
	if err := m.EnableState(statePath); err == nil {
		t.Fatal("EnableState accepted a damaged file")
	}
}

func TestState_DisabledByDefaultWritesNothing(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t, stateActions(t, dir))
	job, err := m.Trigger(context.Background(), "ok", "ci", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForTerminal(t, m, job.ID, 10*time.Second)
	if m.Restored(job.ID) {
		t.Fatal("no state file is configured, so nothing can be restored")
	}
	if err := m.CloseState(); err != nil {
		t.Fatalf("CloseState without state: %v", err)
	}
}
