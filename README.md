# Axiom: mTLS Server Automation Agent

Axiom is a small server-side automation agent for Linux and Windows
Server: a mutually-authenticated (mTLS) HTTPS API that lets a
trusted system — a CI/CD controller, an internal automation tool — trigger
predefined, server-local actions as a safer alternative to giving it SSH
access. You configure what each action does — deploy, rollback, restart,
or anything else — and Axiom itself knows nothing about what those actions
actually do.

[![CI](https://github.com/nuwandev/axiom/actions/workflows/ci.yml/badge.svg)](https://github.com/nuwandev/axiom/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## The problem

Giving a CI system SSH access to production servers so it can run deploy
scripts is a common but blunt tool: the credential that grants "run the
deploy script" also grants everything else a shell can do. Axiom narrows
that down to exactly what's needed: a small, authenticated HTTP API that
can trigger only a fixed, server-defined set of named actions — nothing
else is reachable through it, ever.

## Security at a glance

What the agent enforces, and where each property is exercised. Nothing here
depends on a client behaving well.

| Property | How it is enforced |
|---|---|
| **Mutual TLS, no tokens** | A client certificate signed by your CA is required during the TLS handshake (TLS 1.2+, forward-secret AEAD suites only), and the chain and `clientAuth` key usage are verified again on every request. Anonymous `/health` is off unless you enable it. |
| **Default-deny authorization** | Each certificate identity has an explicit list of allowed actions. Unlisted identity or action: rejected. |
| **Job isolation** | A job's status and output are readable only by the identity that triggered it; anyone else gets the same `404` as for an unknown id. |
| **No arbitrary execution** | Only actions declared in config can run. Scripts are executed directly (no shell) with a fixed minimal `PATH`; the agent's own environment is not inherited. |
| **Validated parameters** | Each parameter must declare a pattern, matched against the whole value, and arrives only as an `AXIOM_PARAM_*` environment variable. Body size, parameter count and value length are capped. |
| **Integrity of what runs** | At startup the config, CA, server certificate and key, and every action script (and their parent directories) must be root-owned and not writable by others, with no symlinks; the private key must not be readable by other users. Otherwise the agent refuses to start. |
| **Bounded resources** | Per-action timeouts kill the whole process tree; output is size-capped; `jobs.max_concurrent` caps running jobs (`429` beyond it); exclusive actions cannot overlap. |
| **Audit that fails closed** | `accepted`, `started`, `finished` and `rejected` events are recorded, secret-looking values are redacted, and an action does not run if its accept or start record cannot be written. Linux can also forward every record to journald/syslog for a central collector. |
| **Contained process** | Dedicated unprivileged account in a systemd sandbox (no capabilities, `NoNewPrivileges`, read-only filesystem except its log and state directories). On Windows: `NT SERVICE\axiom` virtual account, NTFS ACL checks, Job Object containment. |
| **Verifiable releases** | Built in GitHub Actions with commit-pinned actions and an exact Go toolchain, `govulncheck` in CI, GPG-signed `SHA256SUMS` and RPMs, and SLSA build provenance. The signing key is available only to the publish job, and a real release fails without it. See [`docs/verifying-downloads.md`](docs/verifying-downloads.md). |

**How this is checked.** Unit and integration tests cover each rule above,
including negative cases (no certificate, untrusted CA, expired or
wrong-usage certificate, cross-identity job reads, injection-style
parameters, oversized requests, audit failure). Releases are also run on
real hosts: Rocky Linux 9.8 with SELinux enforcing (no denials) and
Windows 11 Pro, covering install, upgrade from v1.2.1, rollback and clean
stop. See [`docs/development.md`](docs/development.md) for how the test
layers are split.

**What it does not do.** No certificate revocation (use short-lived,
per-pipeline certificates and remove an identity from the config to cut it
off); the local audit file is not tamper-proof, so use the syslog sink and a
central collector where an audit record is required; a configured script
runs as the service account, so what it can reach is up to you (Docker
access, for instance, is root-equivalent); no request rate limiting. See
[`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md) for the full analysis.

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
POST /v1/actions/{action}
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

### What's the product vs. what's yours

Worth being explicit about, since it shapes how you deploy this:

| Piece | Owned by |
|---|---|
| The Axiom binary/release | This project |
| `config.yaml` (agent identity, action definitions, authorization) | You, per server |
| Action scripts (what `command:` actually points to) | You, per server |
| Certificates (server + CA + client) | Your organization's PKI — see [`docs/certificates.md`](docs/certificates.md); Axiom only loads and verifies, never issues |
| CI/CD integration (Jenkins or otherwise) | Your pipeline, calling the HTTP API — see [`docs/jenkins-integration.md`](docs/jenkins-integration.md) |

A target server receives a release binary plus its own config, certificates,
and action scripts — **it does not need Go, Git, this source repository, or
any development tooling installed.** See
[Installation overview](#installation-overview) below.

## Installation overview

### Linux (RHEL-family)

```bash
# 1. build or download a release binary (see Releases)
go build -o axiom ./cmd/axiom

# 2. install: creates the service account, directories, binary, systemd unit
sudo BIN_SRC=./axiom ./scripts/install.sh
# on RHEL/Rocky/Alma/CentOS Stream, an RPM is also published per release —
# `sudo dnf install ./axiom-<version>.x86_64.rpm` does the same thing, with
# `dnf upgrade`/`dnf remove` for upgrades and uninstalls. See docs/INSTALL.md §4.

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

### Windows Server 2022 / Windows 10+ Pro

```powershell
# 1. download the release MSI or zip bundle (see Releases), then from an
#    elevated PowerShell in the unpacked bundle directory:

# recommended: double-click the MSI, or silently --
msiexec /i axiom-<version>-windows-amd64.msi /qn
# -- registers the service, applies ACLs, and auto-places the hello.world
# example action, all in one step. Or, from the zip bundle:
.\Install-Axiom.ps1

# 2. provide your own certificates (Axiom never generates these)
Copy-Item ca.crt, server.crt, server.key "$env:ProgramData\Axiom\certs\"

# 3. edit C:\ProgramData\Axiom\config.yaml (the starter one is already there)

# 4. start it
Set-Service -Name axiom -StartupType Automatic; Start-Service -Name axiom
```

Full walkthrough: [`docs/INSTALL-WINDOWS.md`](docs/INSTALL-WINDOWS.md).

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
  hello.world:
    command: /opt/axiom/actions/hello-world.sh
    timeout: 30s
    concurrency: shared

  # Add your own actions below -- Axiom has no opinion on what they do.
  # your.action.name:
  #   command: /opt/axiom/actions/your-script.sh
  #   timeout: 10m
  #   parameters:
  #     some_param:
  #       type: string
  #       pattern: '^[a-zA-Z0-9._-]{1,128}$'
  #       required: true

authorization:
  identities:
    example-ci:
      actions: [hello.world]
```

Full reference: [`docs/configuration.md`](docs/configuration.md). Complete
annotated example: [`configs/example.yaml`](configs/example.yaml).

## Example action

An action is just a name pointing at a script Axiom runs directly (never
through a shell). Declared parameters arrive as `AXIOM_PARAM_<NAME>`
environment variables — never interpolated into a command line. Axiom
ships exactly one example, `hello-world` — a zero-dependency, side-effect-
free script that just confirms the whole chain works; Axiom has no opinion
on what a *real* action should do, so it ships no example of one:

```bash
#!/usr/bin/env bash
set -euo pipefail

echo "Hello from Axiom!"
echo "Ran on $(hostname) at $(date -Is)"
exit 0
```

See [`scripts/examples/`](scripts/examples/) for the shipped `hello-world`
script (both PowerShell and bash), and [`docs/actions.md`](docs/actions.md)
for the full model — including declared parameters and a template for
writing your own.

## Example API request and job flow

```bash
# Trigger — returns immediately
curl --cacert ca.crt --cert client.crt --key client.key \
  -X POST https://agent:8443/v1/actions/hello.world \
  -H 'Content-Type: application/json' -d '{}'
# {"job_id":"01J...","status":"queued"}

# Poll
curl --cacert ca.crt --cert client.crt --key client.key \
  https://agent:8443/v1/jobs/01J...
# {"job_id":"01J...","action":"hello.world","status":"succeeded",
#  "exit_code":0,"started_at":"...","finished_at":"...","duration_ms":48123}

# Logs
curl --cacert ca.crt --cert client.crt --key client.key \
  https://agent:8443/v1/jobs/01J.../logs
# {"stdout":"...","stderr":"","stdout_truncated":false,"stderr_truncated":false}
```

## API reference

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/actions/{action}` | Trigger a configured action. Optional JSON body: `{"parameters": {...}}`. Returns `202` + `{job_id, status}`, or `400`/`403`/`404`/`409`/`429` on rejection (`429`: the agent-wide job limit is reached). |
| `GET` | `/v1/jobs/{job_id}` | Job status/result. Visible only to the identity that triggered the job; `404` if unknown, owned by another identity, or the agent has since restarted. |
| `GET` | `/v1/jobs/{job_id}/logs` | Captured, size-bounded stdout/stderr. Same owner-only visibility as the job itself. |
| `GET` | `/health` | Agent health. Authenticated by default (a client certificate is required at the TLS handshake); anonymous access is an explicit opt-in per agent. |

That is the complete API. There is no endpoint that accepts an arbitrary
command, by design.

## Security notes

- **Certificates are external.** Axiom loads and verifies; it never issues,
  renews, or rotates. See [`docs/certificates.md`](docs/certificates.md)
  for the lifecycle boundary, a safe renewal flow (restart required, no
  hot-reload) and what to do if a client certificate is compromised.
- Full threat model, including what is explicitly out of scope and why:
  [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md). Report vulnerabilities per
  [`SECURITY.md`](SECURITY.md), not as a public issue.

## Development / testing

Requires Go 1.27.4+. Linux (or other unix) is the primary development host;
the tree also builds and tests for `GOOS=windows` (see
[`docs/development.md`](docs/development.md#windows)).

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

The release binary is static (`CGO_ENABLED=0`) with no libc dependency on
Linux and no runtime dependency beyond what each OS ships by default, so the
practical compatibility is wider than the validated matrix below — the
table separates **what this implementation supports architecturally** from
**what has actually been run end-to-end on a real host**.

| OS | Implementation support | Validated |
|---|---|---|
| RHEL-family Linux (RHEL, Rocky, Alma, CentOS Stream), systemd-managed | Yes: static binary, no distro-specific dependency beyond `systemd` | **Rocky Linux 9.8, SELinux enforcing**, v2.0.1: RPM install, upgrade from and rollback to v1.2.1, mTLS, job execution, concurrency limit, clean stop, uninstall (no SELinux denials; the service runs as `unconfined_service_t`, so containment comes from the systemd sandbox, not SELinux). Earlier releases were validated end to end on Rocky Linux 9 (see `docs/THREAT-MODEL.md`) |
| Other systemd-based Linux (Ubuntu, Debian and derivatives) | Yes: same static binary and unit file layout; no RHEL-specific code path exists | Exercised on Debian 13 under systemd (v2.0.x RPM); not part of the validated matrix |
| Windows Server 2016 / 2019 / 2022 / 2025 | Yes — service account model (`NT SERVICE\*` virtual accounts), Job Objects, and `icacls`/`takeown.exe` are all available since Windows Server 2008 R2, well below this project's Go-toolchain floor of Windows Server 2016/Windows 10 (Go 1.21+ does not run on anything older); capability scripts always run under the Windows PowerShell 5.1 that ships by default on every supported release, independent of whether PowerShell 7 is also installed | **Windows Server 2022**, full end-to-end real-host pass (install → ACLs → service start → mTLS → SCM recovery → bad-config diagnostics → least-privilege → uninstall; see [`docs/WINDOWS-SERVER-2022-VALIDATION.md`](docs/WINDOWS-SERVER-2022-VALIDATION.md)) |
| Windows 10 / 11 Pro (any edition with Services + PowerShell) | Yes: nothing depends on the "Server" edition | **Windows 11 Pro**, v2.0.1 MSI: service install and ACLs, mTLS, job execution, upgrade from the v1.2.1 MSI, rollback, clean stop. Windows 10/11's default PowerShell execution policy is handled by invoking each capability script with `-ExecutionPolicy Bypass` for that one fixed, ACL-protected invocation; the host policy is never changed (see [`docs/windows-security.md`](docs/windows-security.md)) |
| Windows 7 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 | **No** — the Go toolchain this project builds with (1.27) does not support running on these at all (Go dropped them in 1.21), independent of anything Axiom-specific | N/A |

See [`docs/INSTALL-WINDOWS.md`](docs/INSTALL-WINDOWS.md) and
[`docs/windows-security.md`](docs/windows-security.md) for the Windows
install/security detail. Not WSL, not a container on either platform —
a native service on the host OS.

## Current status

Current release: **v2.0.1**. The API surface and config schema are stable
within 2.x. 2.0 tightened several defaults (mandatory parameter patterns,
handshake-level client certificates, owner-only job access, root-owned
files); if you are upgrading from 1.x, read the breaking changes and
migration steps in [`CHANGELOG.md`](CHANGELOG.md) and
[`docs/INSTALL.md`](docs/INSTALL.md#14-rollback) first. A v1.2.1 example
config starts unchanged under 2.x when it meets those requirements.

See [Releases](https://github.com/nuwandev/axiom/releases) for what has
shipped. Windows Server 2022 and Windows 11 Pro real-host validation:
[`docs/WINDOWS-SERVER-2022-VALIDATION.md`](docs/WINDOWS-SERVER-2022-VALIDATION.md).

**Known gaps, stated plainly:**
- The Windows `.exe`/`.msi` are not Authenticode-signed, so Windows
  SmartScreen will warn on first run. RPMs and `SHA256SUMS` *are*
  GPG-signed and releases carry build provenance; see
  [docs/verifying-downloads.md](docs/verifying-downloads.md).
- Audit forwarding to a central collector has been checked against
  journald only; validate your own collector with
  [`docs/audit-validation.md`](docs/audit-validation.md).
- No certificate revocation; see [`docs/certificates.md`](docs/certificates.md).
- No third-party antivirus/EDR interoperability testing has been done.
- The Docker/Kubernetes success path (an action that drives a container
  runtime) is implemented but untested end to end.

## Roadmap

- Broader Linux distribution validation (Ubuntu/Debian) alongside the
  RHEL-family matrix.
- A thin CI integration (for example a Jenkins shared library) wrapping the
  HTTP flow in [`docs/jenkins-integration.md`](docs/jenkins-integration.md);
  the guide exists, the packaged wrapper does not.

None of these are commitments to add architecture the current design
deliberately excludes; see [`CONTRIBUTING.md`](CONTRIBUTING.md) for what is
out of scope.

## Author

Created and maintained by [Theekshana Nuwan](https://github.com/nuwandev)
([@nuwandev](https://github.com/nuwandev)).

## License

[MIT](LICENSE)

## Security reporting

See [`SECURITY.md`](SECURITY.md). Please do not report vulnerabilities via
public GitHub issues.
