//go:build unix

package jobs

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/config"
)

func newRateLimitedManager(t *testing.T, perMinute int) *Manager {
	t.Helper()
	dir := t.TempDir()
	script := writeScript(t, dir, "ok.sh", "exit 0\n")
	al, err := audit.Open(filepath.Join(dir, "audit.log"), "test-agent")
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { al.Close() })
	cfg := &config.Config{
		AgentID:              "test-agent",
		MaxTriggersPerMinute: perMinute,
		Actions:              map[string]*config.Action{"ok": {Name: "ok", Command: script, Timeout: 10 * time.Second}},
	}
	return NewManager(cfg, al, slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func TestManager_TriggerRateLimitIsPerIdentityAndCreatesNoJob(t *testing.T) {
	m := newRateLimitedManager(t, 2)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := m.Trigger(ctx, "ok", "ci-a", nil); err != nil {
			t.Fatalf("trigger %d: %v", i+1, err)
		}
	}
	m.mu.Lock()
	before := len(m.jobs)
	m.mu.Unlock()

	_, err := m.Trigger(ctx, "ok", "ci-a", nil)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("third trigger: err = %v, want ErrRateLimited", err)
	}
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %+v, want > 0", rl)
	}
	m.mu.Lock()
	after := len(m.jobs)
	m.mu.Unlock()
	if after != before {
		t.Error("a rate-limited trigger must not create a job")
	}
	if _, err := m.Trigger(ctx, "ok", "ci-b", nil); err != nil {
		t.Errorf("another identity must not be limited: %v", err)
	}
}

func TestManager_NoRateLimitWhenUnset(t *testing.T) {
	m := newRateLimitedManager(t, 0)
	for i := 0; i < 20; i++ {
		if _, err := m.Trigger(context.Background(), "ok", "ci", nil); err != nil {
			t.Fatalf("trigger %d: %v", i+1, err)
		}
	}
}
