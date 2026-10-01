//go:build windows

package jobs

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/config"
)

// These tests build config.Action values directly rather than going through
// config.Load, so no NTFS ACL fixture is required. They cover the job
// manager wiring through to the real Windows executor / PowerShell.

func newWinTestManager(t *testing.T, actions map[string]*config.Action) *Manager {
	t.Helper()
	dir := t.TempDir()
	al, err := audit.Open(filepath.Join(dir, "audit.log"), "test-agent")
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { al.Close() })
	cfg := &config.Config{AgentID: "test-agent", Actions: actions}
	return NewManager(cfg, al, slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func writePS(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return p
}

func waitTerminal(t *testing.T, m *Manager, id string, d time.Duration) Snapshot {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		snap, ok := m.Get(id)
		if !ok {
			t.Fatalf("job %s vanished", id)
		}
		switch snap.Status {
		case StatusSucceeded, StatusFailed, StatusCancelled:
			return snap
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish within %s", id, d)
	return Snapshot{}
}

// Action timeouts below (60s) are a generous test-infrastructure margin
// for a cold powershell.exe spawn on a loaded/first-use CI runner (observed
// exceeding 20s on GitHub's hosted windows-latest image) -- unrelated to
// any real action's own, operator-configured timeout.
func TestManager_Windows_TriggerSuccess(t *testing.T) {
	dir := t.TempDir()
	script := writePS(t, dir, "ok.ps1", "exit 0\r\n")
	m := newWinTestManager(t, map[string]*config.Action{
		"noop": {Name: "noop", Command: script, Timeout: 60 * time.Second, Concurrency: config.ConcurrencyShared},
	})
	job, err := m.Trigger(context.Background(), "noop", "ci", nil)
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if snap := waitTerminal(t, m, job.ID, 25*time.Second); snap.Status != StatusSucceeded {
		t.Errorf("status = %v, want succeeded", snap.Status)
	}
}

func TestManager_Windows_ParameterPatternRejectsInjection(t *testing.T) {
	dir := t.TempDir()
	script := writePS(t, dir, "deploy.ps1", "exit 0\r\n")
	action := &config.Action{
		Name: "deploy", Command: script, Timeout: 60 * time.Second,
		Parameters: map[string]config.Parameter{
			"image_tag": {Type: config.ParameterTypeString, Pattern: `^[a-zA-Z0-9._-]+$`, Required: true},
		},
	}
	p := action.Parameters["image_tag"]
	if err := p.Validate("image_tag"); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	action.Parameters["image_tag"] = p
	m := newWinTestManager(t, map[string]*config.Action{"deploy": action})

	if _, err := m.Trigger(context.Background(), "deploy", "ci", map[string]string{"image_tag": "a'; calc; '"}); err == nil {
		t.Errorf("expected rejection of a value that fails the declared pattern")
	}
	job, err := m.Trigger(context.Background(), "deploy", "ci", map[string]string{"image_tag": "uat-1"})
	if err != nil {
		t.Fatalf("Trigger with valid value: %v", err)
	}
	waitTerminal(t, m, job.ID, 25*time.Second)
}

func TestManager_Windows_ValidatedParamReachesScriptAsInertEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "seen.txt")
	sentinel := filepath.Join(dir, "pwned.txt")
	script := writePS(t, dir, "echo.ps1",
		"Set-Content -Path '"+out+"' -Value $env:AXIOM_PARAM_NOTE\r\n")

	action := &config.Action{
		Name: "echo", Command: script, Timeout: 60 * time.Second,
		Parameters: map[string]config.Parameter{
			// No pattern: prove the value is still inert even unvalidated.
			"note": {Type: config.ParameterTypeString},
		},
	}
	p := action.Parameters["note"]
	if err := p.Validate("note"); err != nil {
		t.Fatal(err)
	}
	action.Parameters["note"] = p
	m := newWinTestManager(t, map[string]*config.Action{"echo": action})

	hostile := "hi'; Set-Content '" + sentinel + "' x; '"
	job, err := m.Trigger(context.Background(), "echo", "ci", map[string]string{"note": hostile})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitTerminal(t, m, job.ID, 25*time.Second)

	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("hostile parameter value executed as PowerShell")
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading script output: %v", err)
	}
	if string(got) != hostile+"\r\n" && string(got) != hostile {
		t.Errorf("script saw %q, want the verbatim value %q", got, hostile)
	}
}
