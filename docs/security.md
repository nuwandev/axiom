# Security

Axiom is a control plane: it accepts authenticated network requests and
executes server-side actions. It's built and reviewed with that weight.
This page is a short orientation; the full per-scenario analysis is in
[`THREAT-MODEL.md`](THREAT-MODEL.md).

## Axiom is intentionally not a remote shell

There is no endpoint that accepts an arbitrary command, and there never
will be. Every action is declared server-side, ahead of time, in config —
see [`actions.md`](actions.md). This is the single most important design
decision in the project, and it's structural, not a policy setting that
could be toggled.

## Trust model, briefly

- **Authentication**: mutual TLS. Every request (except an
  explicitly-opted-in anonymous `/health`) requires a client certificate
  that chain-verifies against a CA you control. No bearer tokens, no API
  keys.
- **Authorization**: an explicit identity → allowed-actions mapping,
  default-deny. A client can only do what its certificate's identity is
  specifically listed as allowed to do.
- **Execution boundary**: scripts run directly (never via a shell), with
  declared/validated parameters passed only as specific environment
  variables. Timeouts kill the full process tree, not just the direct
  child. The agent itself runs as a dedicated, unprivileged account inside
  a hardened systemd sandbox and cannot modify its own binary, config,
  certificates, or action scripts. At startup Axiom enforces this layout:
  config, CA, server certificate/key and scripts must be root-owned and
  not group/world-writable (the key not accessible by others), and no
  ancestor directory may be writable by another user. Concurrency is
  capped (`jobs.max_concurrent`).
- **Audit**: every accepted, started, finished, and rejected request is
  written synchronously to a local log and, with `audit.syslog: true`, also
  emitted to journald/syslog, with values that look like secrets redacted.
  The local file is owned by the account that runs actions and is not
  tamper-resistant; the central collector you forward syslog to is the
  authoritative record (see `THREAT-MODEL.md`, "Audit trail integrity"):

  > **Local audit file: operational copy only.**
  > **Central collector: authoritative audit record.**

  The forwarded sink is Linux-only; the Windows build keeps the local file
  alone and is not suitable where forwarded audit evidence is required.
- **Job visibility**: a job's status and logs are readable only by the
  identity that triggered it.

## Scope and operational guidance

- **Action scripts are yours.** Axiom executes a script exactly as written
  and cannot see into it, which is a deliberate boundary (see
  [`actions.md`](actions.md)). Keep scripts root-owned and reviewed, and give
  each one only the access it needs.
- **Job history is in memory.** Job status and logs are not kept across a
  restart; the audit log is the durable record (see
  [`INSTALL.md`](INSTALL.md#11-job-history-and-restart-behavior)).
- **Replay.** Requests have no nonce or timestamp; mTLS channel security,
  exclusive-concurrency locking and full audit visibility cover this (see
  `THREAT-MODEL.md` §8).

## Full threat model

[`THREAT-MODEL.md`](THREAT-MODEL.md) works through each of these attacker
positions with the specific control that limits it and, where relevant,
the residual risk: an unauthenticated remote attacker, an
attacker with an untrusted or unauthorized client certificate, a
compromised CI credential or CI controller, a low-privilege local Linux
user, a malicious or misconfigured action script, malformed/replayed/
concurrent requests, parameter injection, symlink/path manipulation,
partial deployment failure, and an Axiom crash or restart mid-job.

## Reporting a vulnerability

See [`SECURITY.md`](../SECURITY.md). Please do not report vulnerabilities
via public GitHub issues.
