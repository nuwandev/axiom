# Axiom

Authenticated server-side automation agent for Linux servers. Lets a trusted
CI/CD system trigger predefined, server-local actions (deploy, rollback,
restart, ...) over HTTPS + mutual TLS, without SSH access and without the
agent knowing anything about what the actions actually do.

- No arbitrary command execution — only named actions declared in config.
- mTLS client authentication; identity → allowlisted-action authorization,
  default-deny.
- Each action is a path to an operator-owned script; the agent never
  interprets shell logic itself.
- Async job model: trigger, poll status, fetch bounded logs.

## API

| Method | Path | Purpose |
|---|---|---|
| POST | `/v1/actions/{action}` | Trigger a configured action (optional JSON `parameters`). |
| GET | `/v1/jobs/{job_id}` | Job status/result. |
| GET | `/v1/jobs/{job_id}/logs` | Captured stdout/stderr. |
| GET | `/health` | Agent health (authenticated by default). |

## Configuration

See [`configs/example.yaml`](configs/example.yaml) for a complete annotated
example: agent identity, mTLS material, one audit log path, a set of named
actions (each with a script path, timeout, concurrency policy, and a
declared parameter schema), and identity → action authorization.

## Building and testing

Requires Go 1.23+ and a Linux (or other unix) target — the executor and
config-security checks use unix process/file APIs and do not build on
Windows.

```bash
go build ./...
go vet ./...
go test ./... -race
```

## Running

```bash
go run ./cmd/axiom -config /path/to/config.yaml
```

In production, install the binary, config, certs, and action scripts as
described in the config reference above, then run under systemd using
[`packaging/axiom.service`](packaging/axiom.service) as a dedicated
non-root `axiom` service account.

## Scope

v1 deliberately does not include: a web UI, a database, a message broker,
webhooks, WebSockets, a central control plane, agent federation, or a
reverse/outbound-agent mode for unreachable targets.
