# Changelog

All notable changes to this project are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning
follows [Semantic Versioning](https://semver.org/): breaking changes to
the API surface, config schema, or CLI flags bump the major version;
backward-compatible additions bump minor; fixes bump patch.

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
