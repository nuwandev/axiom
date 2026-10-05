# Axiom

A secure server-side automation/action agent for Linux and Windows Server.
Axiom exposes a small authenticated HTTPS API that lets a trusted system (a
CI/CD controller, an internal automation tool) trigger predefined,
server-local actions — deploy, rollback, restart, and whatever else you
configure — without SSH access, and without Axiom itself knowing anything
about what those actions actually do.

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
- **Certificates are external.** Axiom loads and verifies; it never issues,
  renews, or rotates. See [`docs/certificates.md`](docs/certificates.md)
  for the lifecycle boundary and a safe renewal flow (restart required —
  there's no hot-reload).
- Full threat-model write-up, including what's explicitly out of scope and
  why: [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md). Report
  vulnerabilities per [`SECURITY.md`](SECURITY.md), not as a public issue.

## Development / testing

Requires Go 1.23+. Linux (or other unix) is the primary development host;
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
| RHEL-family Linux (RHEL, Rocky, Alma, CentOS Stream), systemd-managed | Yes — static binary, no distro-specific dependency beyond `systemd` | **Rocky Linux 9**, full end-to-end (install → mTLS → job execution → concurrency → crash/restart → upgrade/rollback → uninstall; see `docs/THREAT-MODEL.md`) |
| Other systemd-based Linux (Ubuntu, Debian and derivatives) | Yes — same static binary and unit file layout; no RHEL-specific code path exists | Not yet run; expected to work, not validated |
| Windows Server 2016 / 2019 / 2022 / 2025 | Yes — service account model (`NT SERVICE\*` virtual accounts), Job Objects, and `icacls`/`takeown.exe` are all available since Windows Server 2008 R2, well below this project's Go-toolchain floor of Windows Server 2016/Windows 10 (Go 1.21+ does not run on anything older); capability scripts always run under the Windows PowerShell 5.1 that ships by default on every supported release, independent of whether PowerShell 7 is also installed | **Windows Server 2022**, full end-to-end real-host pass (install → ACLs → service start → mTLS → SCM recovery → bad-config diagnostics → least-privilege → uninstall; see [`docs/WINDOWS-SERVER-2022-VALIDATION.md`](docs/WINDOWS-SERVER-2022-VALIDATION.md)) |
| Windows 10 / 11 Pro (or any edition with Services + PowerShell, i.e. all of them) | Yes — nothing in the code checks for or depends on "Server" edition specifically; the service, ACL, and PowerShell-invocation logic is edition-agnostic | **Windows 11 Pro**, full end-to-end real-host pass identical in scope to the Windows Server run above. Found and fixed the one real Server-vs-Pro difference in the process: Windows 10/11 Pro's out-of-box PowerShell execution policy (`Restricted`) blocked every capability script until `internal/executor/launch_windows.go` was updated to invoke scripts with `-ExecutionPolicy Bypass` (scoped to that one fixed, ACL-protected invocation only — see `docs/windows-security.md`); re-validated clean afterward with the host's policy never touched. |
| Windows 7 / 8.1 / Server 2008 R2 / 2012 / 2012 R2 | **No** — the Go 1.23 toolchain this project builds with does not support running on these at all (Go dropped them in 1.21), independent of anything Axiom-specific | N/A |

See [`docs/INSTALL-WINDOWS.md`](docs/INSTALL-WINDOWS.md) and
[`docs/windows-security.md`](docs/windows-security.md) for the Windows
install/security detail. Not WSL, not a container on either platform —
a native service on the host OS.

## Current status

1.0 — the API surface, config schema, and security model are stable.
Validated end-to-end on a real RHEL-family host (install → mTLS → job
execution → concurrency → crash/restart → upgrade/rollback → uninstall;
see [`THREAT-MODEL.md`](docs/THREAT-MODEL.md)) before the initial release.
Windows Server 2022 support is implemented behind the same contract, tested
in CI (Linux cross-compile + a Windows runner), and has passed real-host
validation — see
[`docs/WINDOWS-SERVER-2022-VALIDATION.md`](docs/WINDOWS-SERVER-2022-VALIDATION.md).
See
[Releases](https://github.com/nuwandev/axiom/releases) for what's shipped
and [CHANGELOG](CHANGELOG.md) for what changed.

**Known gaps, stated plainly:**
- The Windows `.exe`/`.msi` are not Authenticode-signed, so Windows
  SmartScreen will warn on first run. RPMs and `SHA256SUMS` *are*
  GPG-signed — see [docs/verifying-downloads.md](docs/verifying-downloads.md).
- No third-party antivirus/EDR interoperability testing has been done.
- The Docker/Kubernetes success path (an action that drives a container
  runtime) is implemented but untested end-to-end.
- Ubuntu/Debian are not yet in the validated matrix (RHEL-family only).

## Roadmap

- Broader Linux distribution validation (Ubuntu/Debian) alongside the
  current RHEL-family matrix.
- Windows Server 2022 support has landed (native service, PowerShell
  capability execution, Job Object containment, NTFS ACL integrity checks —
  see [`docs/INSTALL-WINDOWS.md`](docs/INSTALL-WINDOWS.md)) and has passed
  full real-host validation — see
  [`docs/WINDOWS-SERVER-2022-VALIDATION.md`](docs/WINDOWS-SERVER-2022-VALIDATION.md).
- A thin CI system integration (e.g. a Jenkins shared library) that wraps
  the raw HTTP flow documented in
  [`docs/jenkins-integration.md`](docs/jenkins-integration.md) — that
  guide exists now; the packaged wrapper doesn't yet.

None of these are commitments to add architecture the current design
deliberately excludes — see [`CONTRIBUTING.md`](CONTRIBUTING.md) for what's
out of scope.

## Author

Created and maintained by [Theekshana Nuwan](https://github.com/nuwandev)
([@nuwandev](https://github.com/nuwandev)).

## License

[MIT](LICENSE)

## Security reporting

See [`SECURITY.md`](SECURITY.md). Please do not report vulnerabilities via
public GitHub issues.
