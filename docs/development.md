# Development

## Requirements

Go 1.23+, and a Linux (or other unix) host. The executor
(`internal/executor`) and the config security checks
(`internal/config/script_security_unix.go`) use unix-specific process and
file APIs (`syscall.SysProcAttr`, `Pdeathsig`, ownership/permission
syscalls) and do not build on Windows — this is a Linux-targeted product,
not a portability gap to be worked around.

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

**Don't claim an environment is tested unless one of the above tiers
actually covers it.** In particular: this project makes no claim about
SELinux *enforcing*-mode behavior beyond a static policy-database review
(see `INSTALL.md` §9a) unless that has genuinely been verified live on an
enforcing host — check the current `THREAT-MODEL.md` for what's actually
been confirmed before asserting otherwise in an issue or PR.

## CI

GitHub Actions runs `gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test ./...`, and `go test ./... -race` on every push/PR
(`.github/workflows/ci.yml`). CI does not run the RHEL validation tier —
that stays a deliberate, manual, pre-release step.

CodeQL static analysis (`.github/workflows/codeql.yml.disabled`) is
prepared but currently disabled: code scanning on a private repository
requires GitHub Advanced Security, which isn't purchased for this
account, and it's free on public repos with no config change needed. Once
this repository is public, `git mv codeql.yml.disabled codeql.yml`
re-enables it.
