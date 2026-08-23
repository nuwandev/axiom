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
  certificates, or action scripts.
- **Audit**: every accepted, started, finished, and rejected request is
  written synchronously to an append-only log, with values that look like
  secrets redacted.

## What Axiom does not protect against

Stated plainly, not glossed over:

- **What a configured action script itself does.** Axiom has zero
  visibility into or responsibility for action script content — that's a
  deliberate architectural boundary (see [`actions.md`](actions.md)). A
  script that is itself malicious or badly written is outside what an
  execution agent can protect against; least-privilege installation
  (root-owned, reviewed scripts) is the relevant control.
- **In-flight job tracking across a restart.** Job status/logs are
  in-memory only; a restart loses them. The audit log is the durable
  record — see [`INSTALL.md`](INSTALL.md#11-job-history-and-restart-behavior).
- **Request replay** has no dedicated nonce/timestamp defense — mitigated
  by mTLS channel security, exclusive-concurrency locking, and full
  audit visibility, not eliminated. See `THREAT-MODEL.md` §8 for the full
  reasoning.

## Full threat model

[`THREAT-MODEL.md`](THREAT-MODEL.md) works through each of these attacker
positions with the specific control that limits it and, where relevant,
the residual risk stated honestly: an unauthenticated remote attacker, an
attacker with an untrusted or unauthorized client certificate, a
compromised CI credential or CI controller, a low-privilege local Linux
user, a malicious or misconfigured action script, malformed/replayed/
concurrent requests, parameter injection, symlink/path manipulation,
partial deployment failure, and an Axiom crash or restart mid-job.

## Reporting a vulnerability

See [`SECURITY.md`](../SECURITY.md). Please do not report vulnerabilities
via public GitHub issues.
