//go:build windows

package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePS(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "capability.ps1")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("writing script: %v", err)
	}
	return p
}

func run(t *testing.T, spec Spec) *Result {
	t.Helper()
	if spec.Timeout == 0 {
		spec.Timeout = 20 * time.Second
	}
	if spec.MaxOutputBytes == 0 {
		spec.MaxOutputBytes = 1 << 20
	}
	res, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestBuildCommand_Windows_FixedInvocation(t *testing.T) {
	cmd := buildCommand(Spec{Command: `C:\ProgramData\Axiom\capabilities\sample-action.ps1`})

	if !strings.EqualFold(filepath.Base(cmd.Path), "powershell.exe") {
		t.Errorf("interpreter = %q, want powershell.exe", cmd.Path)
	}
	want := []string{
		cmd.Path,
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File",
		`C:\ProgramData\Axiom\capabilities\sample-action.ps1`,
	}
	if len(cmd.Args) != len(want) {
		t.Fatalf("Args = %v, want %v", cmd.Args, want)
	}
	for i := range want {
		if cmd.Args[i] != want[i] {
			t.Errorf("Args[%d] = %q, want %q", i, cmd.Args[i], want[i])
		}
	}
	// -ExecutionPolicy Bypass is deliberately present (see buildCommand's
	// comment) -- what must never appear is anything that turns the
	// argument vector into caller-controlled code.
	for _, forbidden := range []string{"-Command", "-EncodedCommand", "-c"} {
		for _, a := range cmd.Args {
			if strings.EqualFold(a, forbidden) {
				t.Errorf("argument vector must not contain %q; got %v", forbidden, cmd.Args)
			}
		}
	}
}

func TestBuildCommand_Windows_OnlyScriptPathVaries(t *testing.T) {
	a := buildCommand(Spec{Command: `C:\a.ps1`})
	b := buildCommand(Spec{Command: `C:\b.ps1`})
	if a.Args[len(a.Args)-1] != `C:\a.ps1` || b.Args[len(b.Args)-1] != `C:\b.ps1` {
		t.Fatalf("script path is not the trailing argument")
	}
	for i := 0; i < len(a.Args)-1; i++ {
		if a.Args[i] != b.Args[i] {
			t.Fatalf("argument %d differs between invocations (%q vs %q) — only the script path may vary", i, a.Args[i], b.Args[i])
		}
	}
}

func TestRun_Windows_ExitCodeSuccess(t *testing.T) {
	res := run(t, Spec{Command: writePS(t, "exit 0\r\n")})
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestRun_Windows_ExitCodePropagated(t *testing.T) {
	res := run(t, Spec{Command: writePS(t, "exit 7\r\n")})
	if res.ExitCode != 7 {
		t.Errorf("ExitCode = %d, want 7", res.ExitCode)
	}
}

func TestRun_Windows_TerminatingErrorIsNonZero(t *testing.T) {
	res := run(t, Spec{Command: writePS(t, "$ErrorActionPreference='Stop'\r\nthrow 'boom'\r\n")})
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want non-zero for an unhandled terminating error")
	}
}

func TestRun_Windows_StdoutStderrCaptured(t *testing.T) {
	res := run(t, Spec{Command: writePS(t,
		"Write-Output 'out-line'\r\n[Console]::Error.WriteLine('err-line')\r\n")})
	if !strings.Contains(string(res.Stdout), "out-line") {
		t.Errorf("Stdout = %q, want it to contain out-line", res.Stdout)
	}
	if !strings.Contains(string(res.Stderr), "err-line") {
		t.Errorf("Stderr = %q, want it to contain err-line", res.Stderr)
	}
}

func TestRun_Windows_OutputBounded(t *testing.T) {
	res := run(t, Spec{
		Command:        writePS(t, "1..5000 | ForEach-Object { Write-Output '0123456789' }\r\n"),
		MaxOutputBytes: 200,
	})
	if len(res.Stdout) != 200 {
		t.Errorf("len(Stdout) = %d, want 200", len(res.Stdout))
	}
	if !res.StdoutTruncated {
		t.Errorf("expected StdoutTruncated = true")
	}
}

func TestRun_Windows_Timeout(t *testing.T) {
	start := time.Now()
	res := run(t, Spec{Command: writePS(t, "Start-Sleep -Seconds 30\r\n"), Timeout: 400 * time.Millisecond})
	if !res.TimedOut {
		t.Errorf("expected TimedOut = true")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Run took %s; process tree likely not killed", elapsed)
	}
}

func TestRun_Windows_TimeoutKillsProcessTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "still-alive.txt")
	// Launch a detached child that would create a marker after a delay if it
	// were not killed with the rest of the job.
	body := "Start-Process -WindowStyle Hidden powershell -ArgumentList '-NoProfile','-Command'," +
		"\"Start-Sleep -Seconds 8; Set-Content -Path '" + marker + "' -Value alive\"\r\n" +
		"Start-Sleep -Seconds 30\r\n"
	res := run(t, Spec{Command: writePS(t, body), Timeout: 800 * time.Millisecond})
	if !res.TimedOut {
		t.Fatalf("expected TimedOut = true")
	}
	time.Sleep(10 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("marker exists: a descendant process survived the Job Object termination")
	}
}

func TestRun_Windows_Cancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *Result, 1)
	go func() {
		res, _ := Run(ctx, Spec{Command: writePS(t, "Start-Sleep -Seconds 30\r\n"), Timeout: 30 * time.Second, MaxOutputBytes: 1 << 20})
		done <- res
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if !res.Cancelled {
			t.Errorf("expected Cancelled = true")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRun_Windows_EnvIsConstructedNotInherited(t *testing.T) {
	t.Setenv("AXIOM_SHOULD_NOT_LEAK", "leaked")
	res := run(t, Spec{
		Command: writePS(t, "Write-Output \"tag=[$env:AXIOM_PARAM_IMAGE_TAG]\"\r\nWrite-Output \"leak=[$env:AXIOM_SHOULD_NOT_LEAK]\"\r\n"),
		Env: []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"AXIOM_PARAM_IMAGE_TAG=uat-1",
		},
	})
	out := string(res.Stdout)
	if !strings.Contains(out, "tag=[uat-1]") {
		t.Errorf("expected the declared parameter env var to reach the script; got %q", out)
	}
	if !strings.Contains(out, "leak=[]") {
		t.Errorf("Axiom's own environment leaked into the child; got %q", out)
	}
}

// TestRun_Windows_ParameterValueIsInertData proves a parameter value
// containing PowerShell metacharacters reaches the script verbatim and is
// never evaluated as code.
func TestRun_Windows_ParameterValueIsInertData(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "pwned.txt")
	hostile := "x'; Set-Content -Path '" + sentinel + "' -Value pwned; $($x)`n; & calc"
	res := run(t, Spec{
		Command: writePS(t, "Write-Output \"value=[$env:AXIOM_PARAM_X]\"\r\n"),
		Env: []string{
			"SystemRoot=" + os.Getenv("SystemRoot"),
			"AXIOM_PARAM_X=" + hostile,
		},
	})
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatalf("the parameter value was executed as PowerShell — sentinel file was created")
	}
	if !strings.Contains(string(res.Stdout), "value=["+hostile+"]") {
		t.Errorf("parameter value did not reach the script verbatim; got %q", res.Stdout)
	}
}
