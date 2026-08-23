# Axiom

A secure server-side automation/action agent for Linux servers. Axiom
exposes a small authenticated HTTPS API that lets a trusted system (a CI/CD
controller, an internal automation tool) trigger predefined, server-local
actions — deploy, rollback, restart, and whatever else you configure —
without SSH access, and without Axiom itself knowing anything about what
those actions actually do.

[![CI](https://github.com/nuwandev/axiom/actions/workflows/ci.yml/badge.svg)](https://github.com/nuwandev/axiom/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## The problem

Giving a CI system SSH access to production servers so it can run deploy
scripts is a common but blunt tool: the credential that grants "run the
deploy script" also grants everything else a shell can do. Axiom narrows
that down to exactly what's needed: a small, authenticated HTTP API that
can trigger only a fixed, server-defined set of named actions — nothing
else is reachable through it, ever.

## How it works

```text
                         ┌──────────────────────┐
                         │   Trusted CI system   │
                         └───────────┬───────────┘
                                     │  HTTPS + mutual TLS
                                     ▼
                         ┌──────────────────────┐
                         │      Axiom agent      │
                         │                        │
                         │ authenticate (mTLS)    │
                         │ authorize (identity →  │
                         │   allowed actions)     │
                         │ run the named action   │
                         │ audit every request     │
                         └───────────┬───────────┘
                                     │ exec (no shell)
                                     ▼
                         ┌──────────────────────┐
                         │  server-local script  │
                         │  (yours — Axiom has   │
                         │   no idea what's in it)│
                         └──────────────────────┘
```

Axiom is deliberately not a remote shell. There is no endpoint that accepts
arbitrary commands, and there never will be — see
[Why predefined actions](#why-predefined-actions-not-arbitrary-commands)
below.

## Why predefined actions, not arbitrary commands

An API that runs whatever command a client sends is a remote shell with
extra steps — its blast radius is "anything the service account can do."
Axiom instead requires every action to be declared ahead of time, server-side,
in config: a name, a script path, a timeout, a concurrency policy, and a
schema for whatever parameters it accepts. A client can only ask for one of
those named actions by name, with parameter values that pass their
declared regex pattern. There is no code path from an HTTP request to
`os/exec` that isn't a validated, pre-configured script path — this is a
structural property of the implementation, not a policy that could be
bypassed by a misconfigured flag.

## Authentication: mutual TLS

Every request (except an optionally-anonymous `/health`, off by default)
requires a client certificate signed by a CA you configure. No bearer
tokens, no API keys, no basic auth — the client's identity comes from its
certificate's Common Name, verified against the full chain on every
request.

## Authorization: identity → allowed actions

Each client identity maps to an explicit allowlist of action names in
config. An identity with no entry, or an action not on its list, is
rejected — there's no wildcard or implicit-allow. Different clients (say,
CI pipelines for different applications) can be scoped to completely
disjoint sets of actions, so a compromised credential for one pipeline
can't reach another's.

## Async jobs

Triggering an action returns a job ID immediately; the action runs in the
background. Poll for status and fetch captured (size-bounded) stdout/stderr
separately:

```http
POST /v1/actions/backend.deploy
GET  /v1/jobs/{job_id}
GET  /v1/jobs/{job_id}/logs
GET  /health
```

That's the entire API surface. See [API reference](#api-reference) below
for full request/response shapes.

## Supported deployment model

Axiom targets RHEL-family Linux servers (RHEL, Rocky Linux, AlmaLinux,
CentOS Stream) running systemd, installed as a single static binary under
a dedicated, unprivileged service account, hardened with a systemd sandbox
(`ProtectSystem=strict`, no capabilities, no new privileges, and more —
see [`packaging/axiom.service`](packaging/axiom.service)). See
[Supported platforms](#supported-platforms) for the full picture and
[Roadmap](#roadmap) for what's next.

## Installation overview

```bash
# 1. build or download a release binary (see Releases)
go build -o axiom ./cmd/axiom

# 2. install: creates the service account, directories, binary, systemd unit
sudo BIN_SRC=./axiom ./scripts/install.sh

# 3. provide your own certificates (Axiom never generates these)
sudo install -o root -g axiom -m 0640 ca.crt server.crt /etc/axiom/certs/
sudo install -o root -g axiom -m 0640 server.key /etc/axiom/certs/

# 4. write /etc/axiom/config.yaml (start from configs/example.yaml)

# 5. start it
sudo systemctl enable --now axiom
```

Full walkthrough: [`docs/getting-started.md`](docs/getting-started.md).
Complete reference (permissions, systemd hardening, SELinux, upgrade,
rollback, uninstall, troubleshooting): [`docs/INSTALL.md`](docs/INSTALL.md).

## Configuration overview

One YAML file declares the agent's identity, its mTLS material, its named
actions (script path, timeout, concurrency, declared parameters), and
which client identities may trigger which actions:

```yaml
agent:
  id: example-uat-01
  listen: { address: 0.0.0.0, port: 8443 }

security:
  mtls:
    ca_file: /etc/axiom/certs/ca.crt
    cert_file: /etc/axiom/certs/server.crt
    key_file: /etc/axiom/certs/server.key

actions:
  backend.deploy:
    command: /opt/axiom/actions/backend-deploy.sh
    timeout: 10m
    concurrency: exclusive
    parameters:
      image_tag:
        type: string
        pattern: '^[a-zA-Z0-9._-]{1,128}$'
        required: true

authorization:
  identities:
    example-ci:
      actions: [backend.deploy]
```

Full reference: [`docs/configuration.md`](docs/configuration.md). Complete
annotated example: [`configs/example.yaml`](configs/example.yaml).

## Example action

An action is just a name pointing at a script Axiom runs directly (never
through a shell). Declared parameters arrive as `AXIOM_PARAM_<NAME>`
environment variables — never interpolated into a command line:

```bash
#!/usr/bin/env bash
set -euo pipefail
: "${AXIOM_PARAM_IMAGE_TAG:?required}"

docker compose -f /opt/example-app/docker-compose.yml pull
IMAGE_TAG="$AXIOM_PARAM_IMAGE_TAG" \
  docker compose -f /opt/example-app/docker-compose.yml up -d

# Real health check, not just "the launch command exited 0":
for i in $(seq 1 30); do
  curl -sf http://127.0.0.1:8080/healthz && exit 0
  sleep 2
done
exit 1
```

Full example with rollback: [`scripts/examples/`](scripts/examples/). Full
model: [`docs/actions.md`](docs/actions.md).

## Example API request and job flow

```bash
# Trigger — returns immediately
curl --cacert ca.crt --cert client.crt --key client.key \
  -X POST https://agent:8443/v1/actions/backend.deploy \
  -H 'Content-Type: application/json' \
  -d '{"parameters":{"image_tag":"uat-20260823-abc123"}}'
# {"job_id":"01J...","status":"queued"}

# Poll
curl --cacert ca.crt --cert client.crt --key client.key \
  https://agent:8443/v1/jobs/01J...
# {"job_id":"01J...","action":"backend.deploy","status":"succeeded",
#  "exit_code":0,"started_at":"...","finished_at":"...","duration_ms":48123}

# Logs
curl --cacert ca.crt --cert client.crt --key client.key \
  https://agent:8443/v1/jobs/01J.../logs
# {"stdout":"...","stderr":"","stdout_truncated":false,"stderr_truncated":false}
```

## API reference

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/actions/{action}` | Trigger a configured action. Optional JSON body: `{"parameters": {...}}`. Returns `202` + `{job_id, status}`, or `400`/`403`/`404`/`409` on rejection. |
| `GET` | `/v1/jobs/{job_id}` | Job status/result. `404` if unknown or the agent has since restarted. |
| `GET` | `/v1/jobs/{job_id}/logs` | Captured, size-bounded stdout/stderr. |
| `GET` | `/health` | Agent health. Authenticated by default; anonymous access is an explicit opt-in per agent. |

That is the complete API. There is no endpoint that accepts an arbitrary
command, by design.

## Security notes

- **Not a remote shell.** No endpoint executes arbitrary input — see
  [Why predefined actions](#why-predefined-actions-not-arbitrary-commands).
- **Default-deny authorization**, mTLS-only authentication, parameters
  validated against a declared schema before an action ever runs.
- **Least-privilege by construction**: the agent runs as a dedicated,
  unprivileged account inside a hardened systemd sandbox; it cannot modify
  its own binary, config, certificates, or action scripts.
- **Every request is audited** (accepted/started/finished/rejected) to an
  append-only, synchronously-flushed log with sensitive values redacted.
- Full threat-model write-up, including what's explicitly out of scope and
  why: [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md). Report
  vulnerabilities per [`SECURITY.md`](SECURITY.md), not as a public issue.

## Development / testing

Requires Go 1.23+ and a Linux (or other unix) host — the executor and
config-security checks use unix process/file APIs and don't build on
Windows.

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./...
go test ./... -race
```

See [`docs/development.md`](docs/development.md) for the distinction
between these unit/integration tests and the real-RHEL-host validation
this project also goes through before a release.

## Supported platforms

Linux, RHEL-family, systemd-managed, is the current and only implemented
target — validated on Rocky Linux 9 (see `docs/THREAT-MODEL.md`). Other
systemd-based distributions (Ubuntu, Debian) are expected to work given the
same layout but are not part of the current validated matrix.

## Current status

1.0 — the API surface, config schema, and security model are stable.
Validated end-to-end on a real RHEL-family host (install → mTLS → job
execution → concurrency → crash/restart → upgrade/rollback → uninstall;
see [`THREAT-MODEL.md`](docs/THREAT-MODEL.md)) before this release. See
[Releases](https://github.com/nuwandev/axiom/releases) for what's shipped
and [CHANGELOG](CHANGELOG.md) for what changed.

## Roadmap

- Broader Linux distribution validation (Ubuntu/Debian) alongside the
  current RHEL-family matrix.
- **Windows Server** as a future, separate platform implementation (Windows
  service semantics, process lifecycle, PowerShell-based actions) behind
  the same API/config contract — not on the current Linux code path, and
  not started yet.
- A thin CI system integration (e.g. a Jenkins pipeline helper) that wraps
  the HTTP API — after the Linux/RHEL foundation is proven, which it now
  is.

None of these are commitments to add architecture the current design
deliberately excludes — see [`CONTRIBUTING.md`](CONTRIBUTING.md) for what's
out of scope.

## License

[MIT](LICENSE)

## Security reporting

See [`SECURITY.md`](SECURITY.md). Please do not report vulnerabilities via
public GitHub issues.
