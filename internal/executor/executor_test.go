//go:build unix

package executor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return p
}

func TestRun_ExitCodeSuccess(t *testing.T) {
	script := writeScript(t, "exit 0\n")
	res, err := Run(context.Background(), Spec{Command: script, Timeout: 5 * time.Second, MaxOutputBytes: 1024})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestRun_ExitCodeFailure(t *testing.T) {
	script := writeScript(t, "exit 7\n")
	res, err := Run(context.Background(), Spec{Command: script, Timeout: 5 * time.Second, MaxOutputBytes: 1024})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
}

func TestRun_StdoutStderrCaptured(t *testing.T) {
	script := writeScript(t, "echo out-line; echo err-line 1>&2\n")
	res, err := Run(context.Background(), Spec{Command: script, Timeout: 5 * time.Second, MaxOutputBytes: 1024})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Stdout) != "out-line\n" {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	if string(res.Stderr) != "err-line\n" {
		t.Errorf("Stderr = %q", res.Stderr)
	}
}

func TestRun_OutputBounded(t *testing.T) {
	// Emit far more than the cap.
	script := writeScript(t, "for i in $(seq 1 5000); do echo 0123456789; done\n")
	res, err := Run(context.Background(), Spec{Command: script, Timeout: 5 * time.Second, MaxOutputBytes: 100})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Stdout) != 100 {
		t.Errorf("len(Stdout) = %d, want 100", len(res.Stdout))
	}
	if !res.StdoutTruncated {
		t.Errorf("expected StdoutTruncated = true")
	}
}

func TestRun_TimeoutKillsProcessTree(t *testing.T) {
	// Parent sleeps briefly then spawns a long-lived child; if only the
	// parent were killed the child would survive and keep running.
	marker := filepath.Join(t.TempDir(), "still-alive")
	script := writeScript(t, `
(sleep 30; touch `+marker+`) &
sleep 30
`)
	start := time.Now()
	res, err := Run(context.Background(), Spec{Command: script, Timeout: 300 * time.Millisecond, MaxOutputBytes: 1024})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.TimedOut {
		t.Errorf("expected TimedOut = true")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took too long (%s); process tree likely not killed", elapsed)
	}

	// Give a leaked child a moment to have created the marker if it wasn't
	// actually killed, then confirm it never did.
	time.Sleep(1 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("marker file exists: background child survived the timeout kill")
	}
}

func TestRun_CancellationKillsProcess(t *testing.T) {
	script := writeScript(t, "sleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, Spec{Command: script, Timeout: 30 * time.Second, MaxOutputBytes: 1024})
		done <- res
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case res := <-done:
		if !res.Cancelled {
			t.Errorf("expected Cancelled = true")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after cancellation")
	}
}
