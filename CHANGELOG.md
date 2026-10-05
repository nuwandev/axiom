# Changelog

All notable changes to this project are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning
follows [Semantic Versioning](https://semver.org/): breaking changes to
the API surface, config schema, or CLI flags bump the major version;
backward-compatible additions bump minor; fixes bump patch.

## [1.2.1] — 2026-10-05

### Added
- **Signed RPMs and checksums.** Release RPMs are now GPG-signed, and the
  published `SHA256SUMS` has a detached signature (`SHA256SUMS.asc`), using
  a dedicated release-signing key kept only in GitHub Actions. See
  [docs/verifying-downloads.md](docs/verifying-downloads.md) for the
  fingerprint and how to verify. No code changes — same binaries as 1.2.0.

### Known gaps (unchanged, stated for clarity)
- Windows `.exe`/`.msi` are still not Authenticode-signed: there is no free
  option that removes the SmartScreen warning (a self-signed certificate
  does not; SignPath Foundation's free OSS program does, but needs an
  application/review this release isn't waiting on).
- No third-party antivirus/EDR interoperability testing.
- Docker/Kubernetes action success path is implemented but untested
  end-to-end; Ubuntu/Debian are not yet in the validated matrix.

## [1.2.0] — 2026-10-03

### Added
- **Windows Server 2022 support.** A native Windows service (Service Control
  Manager adapter over the same `serve(ctx)` lifecycle the unix binary uses)
  that runs approved PowerShell `.ps1` capability scripts through a single
  fixed, non-configurable invocation
  (`powershell.exe -NoProfile -NonInteractive -File <configured script>`) —
  no `-Command`, no request-controlled arguments, no arbitrary script
  selection. The external API, JSON formats, config schema, capability
  model, mTLS authentication, identity→action authorization and audit trail
  are unchanged and identical across platforms.
  - Process-tree containment via a Windows Job Object with
    `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` — kills the whole capability tree on
    timeout, cancellation, or an uncontrolled death of the agent.
  - Filesystem integrity via NTFS ACLs (`internal/winsec`): capability
    scripts, config and TLS key material must be owned by
    Administrators/SYSTEM/TrustedInstaller with no write access for any other
    principal (the service account included), under an inheritance-protected
    directory. Verified fail-closed at startup, the analogue of the unix
    ownership/mode checks.
  - Runs as the virtual service account `NT SERVICE\axiom` (no password, not
    an Administrator, not LocalSystem) with a minimal privilege set.
  - Offline install: `packaging/windows/Install-Axiom.ps1` +
    `Uninstall-Axiom.ps1` (no Internet at install or runtime), plus a
    double-clickable / silently-deployable MSI (`packaging/windows/msi/Axiom.wxs`,
    built with WiX v5) that wraps the identical scripts as custom actions —
    no separate install logic to maintain, and no paid WiX license (pinned
    below v7's Open Source Maintenance Fee requirement). `scripts/build-release.sh`
    now also produces a `windows/amd64` binary, zip, and MSI.
  - New docs: `docs/INSTALL-WINDOWS.md`, `docs/windows-security.md`,
    `docs/WINDOWS-SERVER-2022-VALIDATION.md`, `configs/example-windows.yaml`,
    sample `.ps1` capability scripts.
  - CI gains a `GOOS=windows` cross-compile/vet step and a `windows-latest`
    build/vet/test job.

### Changed
- `internal/executor` and `internal/config` script-security no longer use
  unix-only symbols in their shared files; the platform specifics moved
  behind `//go:build` seams. Linux runtime behaviour is unchanged
  (regression-guarded: the unix `buildCommand` argv and `baseEnv` PATH are
  byte-identical to before).
- `cmd/axiom` startup split into a shared `serve(ctx)` plus per-platform
  entrypoints. No change to config loading, serving, or graceful shutdown on
  unix.

### Dependencies
- Added `golang.org/x/sys` (used only under `//go:build windows`, for Job
  Objects and the Service Control Manager). Vendored.

### Fixed (Windows validation pass)
- `packaging/windows/*.ps1` and `scripts/examples/*.ps1.sample` contained
  EM DASH characters; a BOM-less `.ps1` with non-ASCII bytes is mis-decoded
  by Windows PowerShell 5.1 and `Install-Axiom.ps1` would not parse. All
  shipped PowerShell is now ASCII; CI enforces this and parses every script.
- `Install-Axiom.ps1`: `"$SID:..."` was parsed as scoped-variable syntax;
  now `"${SID}:..."`. The config-template copy now also finds the file in the
  flat release bundle layout.
- Windows executor: an `AssignProcessToJobObject` failure on a still-running
  child is now fatal to the job (child killed, error returned) instead of
  silently running the capability uncontained.
- `internal/winsec`: inherit-only ACEs (CREATOR OWNER etc., present on real
  Windows directories) are ignored rather than treated as effective grants,
  which had made `RequireProtectedAncestry` reject valid layouts.
- Windows service: `config.Load` now runs inside the SCM handler (after
  `StartPending`), so a bad configuration surfaces as a clean Stopped state
  with `ERROR_INVALID_DATA` and an event-log entry rather than an SCM
  "did not respond in time" error.

### Fixed (found and fixed on a real Windows Server 2022 host, during validation)
- `icacls`'s `/setowner` flag rejected being combined with any other
  operation in one invocation (`Set-ProtectedAcl`); it is now its own call.
- Applying an `(OI)(CI)`-flagged (inheritable) grant recursively onto a
  directory that already contained real files (`axiom.exe`, `config.yaml`)
  could make `icacls` silently write an **empty DACL** to a leaf file instead
  of the intended grant — an empty-but-present DACL denies *everyone*,
  including Administrators and SYSTEM. Fixed: inheritable grants now apply
  only to the directory itself; anything already inside gets its own
  explicit, non-inheriting grant.
- Recovering ownership of a file left in a broken ACL state by an older,
  pre-fix install (`Uninstall-Axiom.ps1` deliberately preserves
  `%ProgramData%\Axiom` across an uninstall) now uses `takeown.exe` instead
  of `icacls /setowner`, which is itself DACL-checked and so cannot recover
  from exactly that state. `takeown.exe`'s `/R`/`/D` flags are applied only
  when the target is a directory (both error against a single file).
- `Set-ProtectedAcl` protecting `%ProgramData%\Axiom` recursed into the
  `certs\`/`capabilities\`/`logs\` subdirectories *before* their own,
  differently-scoped ACL pass ran, leaving duplicate/incorrect ACEs that
  Axiom's own startup ACL validator correctly refused to trust. Fixed: the
  parent directory's pass now excludes those subdirectories.

### Windows Server 2022 validation
- **Full real-host validation passed.** Fresh offline install, protected-ACL
  application and its reject path, service registration under the
  least-privileged `NT SERVICE\axiom` account, the service actually starting
  and serving real mTLS traffic, 12 end-to-end mTLS cases (valid client,
  every rejection path, parameter validation, a succeeding and a failing
  job), the audit trail, SCM auto-restart after a crash (and no restart on a
  clean stop), four bad-configuration/tampering scenarios each failing
  closed with a specific diagnosable error, timeout process-tree
  containment, and the full uninstall/reinstall/purge cycle including while
  the service is live. See `docs/WINDOWS-SERVER-2022-VALIDATION.md` for the
  complete evidence trail. **Windows Server 2022 is approved for
  production**, with one noted exception: validating a Docker/Kubernetes-
  backed capability's success path requires a host that has Docker or
  Kubernetes installed, which is a property of the deployment target, not
  of Axiom — the fail-closed behavior without one was verified instead.

### Fixed (found and fixed on a real Windows 10/11-class host, not WS2022)
- `internal/executor/launch_windows.go`'s PowerShell invocation never passed
  `-ExecutionPolicy`, on the theory that execution policy is not Axiom's
  security boundary (true) and therefore doesn't need to be touched (not
  true in practice). Windows 10/11 Pro ships with the effective policy
  `Restricted` by default (Windows Server ships `RemoteSigned`), which
  blocks every capability script unconditionally — every action silently
  failed on a real Windows 11 Pro host while the service itself appeared
  healthy (`/health` still worked), the worst kind of failure since it only
  surfaces once someone actually tries to use an action. Fixed by having
  `buildCommand` pass `-ExecutionPolicy Bypass` on its own fixed,
  ACL-protected invocation only (no `Set-ExecutionPolicy`, no persisted
  change, no effect on anything else on the machine — safe because the
  script path/content integrity guarantees come from NTFS ACLs and
  server-side config, not execution policy), and having
  `CheckPlatformPrerequisites` fail startup with a specific message only if
  a *real Group Policy* (the one thing `Bypass` cannot override) enforces
  `Restricted`/`AllSigned`. Re-validated clean (96/96) on the same Windows
  11 Pro host afterward, with the host's local policy never changed. See
  `docs/WINDOWS-SERVER-2022-VALIDATION.md` Finding 11.

## [1.1.0] — 2026-08-29

Packaging release. No Axiom binary/API/runtime changes — the released Go
binary is functionally identical to v1.0.1.

### Added
- RPM packaging for RHEL/Rocky/AlmaLinux/CentOS Stream
  (`packaging/rpm/nfpm.yaml.tmpl` + scriptlets, built via
  [`nfpm`](https://nfpm.goreleaser.com)): `sudo dnf install axiom-<version>.x86_64.rpm`
  does the same thing `scripts/install.sh` does (service account,
  filesystem layout, binary, systemd unit — no certificates or config
  touched, service not auto-started), but with real `dnf upgrade` /
  `dnf remove` support instead of a manual uninstall doc section. A
  running instance is not disrupted by an upgrade transaction (verified:
  same PID throughout, new binary takes effect on the next
  `systemctl restart`); a real removal correctly stops/disables the
  service and removes the binary/unit while deliberately leaving
  `/etc/axiom`, `/opt/axiom`, `/var/log/axiom`, `/var/lib/axiom`, and the
  `axiom` user/group in place, same reasoning `docs/INSTALL.md` §16
  already documented for the manual path. `scripts/build-release.sh` now
  also produces `.rpm` packages for both published architectures
  alongside the existing raw binaries, falling back to binaries-only with
  a warning if `nfpm`/`envsubst` aren't present on the build machine —
  the existing binary + `install.sh` path is unchanged and stays fully
  supported, this is an additional option, not a replacement.
- `docs/INSTALL.md` §4 restructured into three explicit install paths
  (4a RPM, 4b downloaded binary, 4c source checkout) and §16 Uninstall
  now covers both the RPM path and the existing manual path.

### Verified
- Real install → real startup (mTLS handshake succeeds, correctly rejects
  an unauthenticated request) → upgrade while running → removal, all run
  against a genuine systemd-enabled Rocky Linux 9 container, not simulated.

## [1.0.1] — 2026-08-24

Documentation/tooling patch release. No Axiom binary/API/runtime changes —
the released Go binary is functionally identical to v1.0.0.

### Fixed
- `scripts/install.sh` computed its systemd-unit source path relative to
  its own location, assuming a full repository checkout (`scripts/install.sh`
  next to a sibling `packaging/` directory). This broke the newly-documented
  "download just the release binary + install script" flow (see Added
  below): run standalone, it looked in the wrong directory and failed with
  "systemd unit not found." Now overridable via `SYSTEMD_UNIT_SRC`
  (matching the existing `BIN_SRC` pattern), defaulting to the prior
  behavior when unset so a repo-checkout install is unaffected. Verified
  by actually running both a fresh install and an idempotent re-run
  against a real Rocky Linux 9 systemd host using the documented flat
  two-file layout.

### Added
- `docs/INSTALL.md` §4 now documents two explicit install paths: 4a,
  downloading the release binary plus two small text files
  (`install.sh`, `packaging/axiom.service`) directly from the tagged
  release via `curl` — no Git, no Go, no repository checkout needed on
  the target server; and 4b, the existing from-source build. §6 gained
  the equivalent `curl` option for `configs/example.yaml`.
- `postman/Axiom.postman_collection.json` and `docs/postman-testing.md`:
  a Postman collection covering all four API endpoints plus five negative
  tests (unauthenticated, unauthorized action, nonexistent action, missing
  required parameter, invalid parameter) for manual verification of the
  security model, with a guide covering import, `base_url`, and Postman's
  client-certificate/mTLS configuration (including its one real
  limitation: certificates are configured app-wide per-domain, not
  per-request or per-collection, so they can't ship inside the file).

## [1.0.0] — 2026-08-23

Initial public release. Linux/RHEL-family, systemd-managed deployment,
validated end-to-end on a real RHEL 9 host before tagging.

### Added
- HTTPS API secured with mutual TLS: `POST /v1/actions/{action}`,
  `GET /v1/jobs/{job_id}`, `GET /v1/jobs/{job_id}/logs`, `GET /health`.
- Identity → allowed-actions authorization, default-deny.
- Config-driven action registry: named actions, each a path to a local
  script, with a declared/validated parameter schema, a timeout, and a
  concurrency policy (`shared`/`exclusive`).
- Process executor: no shell involved in running a script; timeout and
  cancellation escalate `SIGTERM` → a bounded grace period → `SIGKILL`
  across the full process group; `PR_SET_PDEATHSIG` ensures a direct child
  can't outlive an unexpected Axiom process death; bounded stdout/stderr
  capture.
- Append-only, synchronously-flushed audit log with sensitive-parameter
  redaction, covering every accepted/started/finished/rejected request.
- Bounded in-memory job history (oldest finished job evicted once the
  configured limit is exceeded; a running job is never evicted).
- `scripts/install.sh`: idempotent RHEL-family installer (service account,
  directory layout, binary, systemd unit); never generates or overwrites
  certificates or an existing config.
- Hardened systemd unit (`packaging/axiom.service`): dedicated unprivileged
  account, `ProtectSystem=strict`, no capabilities, no new privileges,
  restricted address families, and more, each with documented rationale.
- Full documentation set: getting started, configuration reference,
  actions model, operations/installation guide, security overview, and
  threat model.

### Known limitations
- Job history/status is in-memory only and does not survive an Axiom
  restart; the audit log is the durable execution record.
- No request-replay defense beyond the mTLS channel, exclusive-concurrency
  locking, and audit visibility.
- SELinux *enforcing*-mode behavior has been reviewed against RHEL9's
  static policy database but not exercised on a live enforcing host as
  part of this release's validation — see `docs/INSTALL.md` §9a for the
  verification steps to run on your own enforcing host before a
  production rollout.
- Linux/RHEL-family only. Windows Server support is a planned future,
  separate platform implementation — see the README roadmap.
