//go:build unix

package executor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// TestRun_TimeoutEscalatesFromSIGTERMToSIGKILL demonstrates the
// SIGTERM-then-grace-period-then-SIGKILL ordering: a script that ignores
// SIGTERM is NOT killed instantly (proving SIGTERM was actually tried and
// given a chance) but IS killed within TerminationGracePeriod of the
// timeout firing (proving the grace period is bounded, not indefinite).
func TestRun_TimeoutEscalatesFromSIGTERMToSIGKILL(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "still-alive-after-term")
	// Ignore SIGTERM entirely, then prove we survived it by creating a
	// marker file 1s in — if SIGKILL didn't eventually arrive, the sleep
	// would run to completion (30s) and the test's own timeout would catch
	// that; what we're actually asserting below is the elapsed-time window.
	script := writeScript(t, `
trap '' TERM
(sleep 1; touch `+marker+`) &
sleep 30
`)
	start := time.Now()
	res, err := Run(context.Background(), Spec{Command: script, Timeout: 300 * time.Millisecond, MaxOutputBytes: 1024})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	if !res.TimedOut {
		t.Errorf("expected TimedOut = true")
	}
	// Must have survived long enough to prove SIGTERM alone didn't kill it
	// (it's trapped/ignored) — i.e. Run did not return instantly.
	if elapsed < 900*time.Millisecond {
		t.Errorf("Run returned in %s, too fast for a SIGTERM-ignoring script — SIGKILL escalation may not have waited for the grace period", elapsed)
	}
	// Must still be bounded: timeout (300ms) + grace period (5s) + a little
	// scheduling slack, not indefinite.
	if elapsed > TerminationGracePeriod+2*time.Second {
		t.Errorf("Run took %s, longer than the bounded TerminationGracePeriod (%s) allows", elapsed, TerminationGracePeriod)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("marker file missing: the background job should have survived long enough (SIGTERM ignored) to create it before SIGKILL arrived")
	}

	// And once SIGKILL does escalate, the process group is actually gone —
	// not just the parent.
	time.Sleep(200 * time.Millisecond)
	// (No portable process-liveness check beyond what the parent tests
	// above already assert via the marker/timing; TestPdeathsig below
	// checks liveness directly via /proc for the death-of-parent case.)
}

// TestConfigureProcessGroup_PdeathsigKillsChildIfParentDiesUnexpectedly
// demonstrates the actual failure mode named in the review request — "Axiom
// is killed and a child process may survive it" — and verifies the fix:
// the child is configured (via configureProcessGroup, the same helper
// executor.Run uses) with PR_SET_PDEATHSIG=SIGKILL, so the kernel itself
// kills the child the instant its parent process terminates for any
// reason, with no cooperation required from the dying parent.
//
// This does NOT go through executor.Run (Run's own timeout/cancellation
// logic is a controlled shutdown of a parent that stays alive — a
// different code path, covered by the tests above). This test instead
// simulates the parent disappearing out from under a live child, via a
// re-exec'd helper subprocess that plays the role of "Axiom": it starts a
// long-sleeping grandchild using the exact same SysProcAttr, prints that
// grandchild's PID, then is itself killed with SIGKILL from this test —
// simulating a crash/OOM/`kill -9` of the real Axiom process.
func TestConfigureProcessGroup_PdeathsigKillsChildIfParentDiesUnexpectedly(t *testing.T) {
	if os.Getenv("AXIOM_PDEATHSIG_HELPER") == "1" {
		runPdeathsigHelper()
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^TestConfigureProcessGroup_PdeathsigKillsChildIfParentDiesUnexpectedly$")
	cmd.Env = append(os.Environ(), "AXIOM_PDEATHSIG_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting fake-parent helper: %v", err)
	}

	reader := bufio.NewReader(stdout)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading grandchild PID from helper: %v", err)
	}
	grandchildPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parsing grandchild PID %q: %v", line, err)
	}

	if !processAlive(grandchildPID) {
		t.Fatalf("grandchild pid %d is not alive right after the helper reported it", grandchildPID)
	}

	// Simulate Axiom disappearing without any controlled shutdown.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing fake-parent helper: %v", err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(grandchildPID) {
			return // Pdeathsig did its job.
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Best-effort cleanup so a failing test doesn't leak a sleeping process.
	_ = syscall.Kill(grandchildPID, syscall.SIGKILL)
	t.Fatalf("grandchild pid %d was still alive %s after its parent was killed: Pdeathsig did not fire", grandchildPID, 3*time.Second)
}

// runPdeathsigHelper plays the role of "Axiom" for
// TestConfigureProcessGroup_PdeathsigKillsChildIfParentDiesUnexpectedly: it
// starts a long-lived grandchild with the package's real Pdeathsig
// configuration, reports its PID, then blocks until this whole process is
// killed from outside (by the parent test), at which point the kernel
// delivers SIGKILL to the grandchild directly — no code here runs at that
// point, which is exactly the scenario being verified.
func runPdeathsigHelper() {
	cmd := exec.Command("sleep", "30")
	configureProcessGroup(cmd) // the same SysProcAttr executor.Run uses
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "helper: starting grandchild:", err)
		os.Exit(1)
	}
	fmt.Println(cmd.Process.Pid)
	select {} // wait to be killed
}

func processAlive(pid int) bool {
	// Signal 0 performs no actual signal delivery, only existence/
	// permission checks (see kill(2)) — the standard portable way to probe
	// whether a pid is still alive without affecting it.
	err := syscall.Kill(pid, 0)
	return err == nil
}
