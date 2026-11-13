# Development

## Requirements

Go 1.27.4+. Linux (or other unix) is the primary development host. The whole
tree also builds for `GOOS=windows` — the Windows-specific process
execution, Job Object containment, NTFS ACL checks and Service Control
Manager adapter live behind `//go:build windows` files
(`internal/executor/*_windows.go`, `internal/config/script_security_windows.go`,
`internal/winsec/`, `internal/jobs/env_windows.go`,
`cmd/axiom/*_windows.go`). The shared packages carry no build tag and
compile for every OS.

## Standard checks

Run these before committing; CI runs the same set:

```bash
gofmt -l .          # must produce no output
go build ./...
go vet ./...
go test ./...
go test ./... -race
```

## What each test tier actually covers

It's worth being precise about this, since the levels catch different
things and none of them substitutes for another:

- **Unit tests** (`*_test.go` throughout `internal/`) — pure logic:
  config validation rules, parameter pattern matching, job state
  transitions, audit record redaction. Fast, no process execution, no
  network.
- **Integration tests** (`internal/api/integration_test.go`,
  `internal/executor/executor_test.go`) — exercise real behavior on
  whatever unix host `go test` runs on: a real mTLS handshake with
  freshly generated certificates, real process spawning, real
  `SIGTERM`/`SIGKILL` delivery, a real re-exec'd helper process to prove
  `Pdeathsig` fires when a "parent" process dies unexpectedly. These run
  as part of `go test ./...` above — no separate setup required, but they
  do need to run on Linux/unix, not Windows.
- **RHEL validation** — a separate, manual pass performed before a release
  against a real RHEL-family host (see [`THREAT-MODEL.md`](THREAT-MODEL.md)
  for what was tested and found): the actual installer, the actual
  systemd unit with its hardening directives active, real file
  ownership/permission enforcement as a genuine low-privilege local user,
  and a crash/restart scenario. This is not part of `go test` and is not
  automated in CI — it requires an actual RHEL-family systemd environment
  (a `--privileged --systemd=always` container works; see
  `scripts/rhel-test-*.sh` for the harness used).

- **RPM lifecycle in CI** (`scripts/ci-rpm-lifecycle.sh`, run by the
  `RPM package builds` job) — installs the published, signed v1.2.1 RPM in a
  Rocky Linux 9 container, runs mTLS and `hello.world` against the unchanged
  v1.2.1 config, upgrades to the candidate RPM, checks the ownership and
  key-mode refusals, and rolls back. Containers have no init, so this does
  not start the systemd unit.
- **Pre-release host runs** — for each release the published artifacts are
  also run on a real systemd host (the RHEL-family VM with SELinux enforcing,
  and Windows 11 Pro); results are summarized in `INSTALL.md` §9a and the
  validation documents.

**Don't claim an environment is tested unless one of the above tiers
actually covers it.** SELinux *enforcing* behavior was verified live on one
Rocky Linux 9.8 host for v2.0.1 (zero denials; see `INSTALL.md` §9a); do not
extend that to other hosts or policies without running it there.

## Windows

The Windows platform code has three test tiers of its own:

- **`GOOS=windows` cross-compile + vet** — runs on the Linux CI job. Keeps
  the platform seam honest; proves nothing about behaviour.
- **Windows CI unit / non-elevated integration** (`*_windows_test.go`) — runs
  on a `windows-latest` CI runner. Covers: the PowerShell executor
  (exit codes, stdout/stderr, output truncation, timeout, cancellation,
  constructed-not-inherited environment, parameter values as inert data),
  the fixed `buildCommand` argument vector (no `-Command`, no
  `-EncodedCommand`, no arbitrary argv — `-ExecutionPolicy Bypass` *is*
  present deliberately, scoped to this one fixed invocation only; see
  `docs/windows-security.md`), Job Object process-tree
  termination, `internal/winsec` pure ACL decision logic plus its Win32
  read path against real temp files (extension + ownership rejection,
  reparse-point / junction rejection), the job manager end-to-end through
  PowerShell, and the `serve(ctx)` lifecycle. It does **not** cover the full
  `config.Load` path or mTLS API integration, because those need an
  NTFS-ACL-configured fixture tree (an ordinary `t.TempDir()` is
  user-writable and correctly fails the security checks).
- **Windows Server 2022 / Windows 11 Pro validation** — a manual pass on
  real hosts, the analogue of the RHEL tier, **completed**:
  `Install-Axiom.ps1` → protected ACLs verified → `config.Load` accepting
  a correctly-secured layout and rejecting a tampered one → service
  start/stop/recovery → mTLS end-to-end with a Windows client cert →
  capability execution (including the Docker-backed sample's fail-closed
  path on a host with no Docker daemon) → Job Object teardown on service
  stop and on a forced kill → least-privilege confirmation (`whoami`,
  token privileges, from inside a real triggered capability). Findings are
  recorded in [`WINDOWS-SERVER-2022-VALIDATION.md`](WINDOWS-SERVER-2022-VALIDATION.md),
  the Windows analogue of the RHEL findings below. Re-run this pass against
  any future change to `internal/winsec`, `internal/executor`'s Windows
  files, or `packaging/windows/`.

## CI

GitHub Actions (`.github/workflows/ci.yml`) runs, on every push/PR:

- a Linux job: `gofmt -l .`, `go mod verify`, `go build ./...`,
  `go vet ./...`, `go test ./...`, `go test ./... -race`, `govulncheck`, then
  a `GOOS=windows` cross-compile;
- a Windows job: `go build`, `go vet`, `go test ./...`;
- an RPM job: builds a throwaway RPM with nfpm and runs the RPM lifecycle
  test above.

The release workflow (`release.yml`) can be dry-run without publishing via
`workflow_dispatch`; see `CONTRIBUTING.md` ("Cutting a release").

CI does not run the real-host validation tiers (systemd-managed RHEL host,
Windows Server 2022 / Windows 11 Pro) — those stay deliberate, manual,
pre-release steps.

CodeQL static analysis (`.github/workflows/codeql.yml`, `security-extended`
queries) runs on every push to `main`, every pull request and weekly. The Go
code is analysed once per target OS (Linux and Windows) so the Windows-only
files are covered. Results appear under Security > Code scanning.
