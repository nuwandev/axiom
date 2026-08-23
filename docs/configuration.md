# Configuration Reference

Axiom reads one YAML file (`-config`, default `/etc/axiom/config.yaml`),
validated in full at startup — any structural problem, missing/insecure
file, invalid reference, or out-of-range value aborts startup rather than
running with a partially valid configuration. See
[`configs/example.yaml`](../configs/example.yaml) for a complete annotated
example.

## Agent identity

```yaml
agent:
  id: example-uat-01      # surfaced on /health and in every audit record
  name: Example UAT Agent
  listen:
    address: 0.0.0.0
    port: 8443
```

## Listener / TLS / mTLS

```yaml
security:
  mtls:
    ca_file: /etc/axiom/certs/ca.crt        # internal CA — the trust root for client certs
    cert_file: /etc/axiom/certs/server.crt  # this agent's certificate
    key_file: /etc/axiom/certs/server.key   # this agent's private key
  health:
    allow_anonymous: false   # default; only set true if a network health
                              # check genuinely cannot present a client cert
```

Every request except `/health` (when `allow_anonymous` is left `false`,
the default) requires a client certificate that chain-verifies against
`ca_file`. TLS 1.2 minimum, with cipher suites restricted to forward-secret
AEAD suites on a 1.2 handshake (TLS 1.3 uses the standard library's fixed
strong suite set).

## Actions

```yaml
actions:
  backend.deploy:
    command: /opt/axiom/actions/backend-deploy.sh  # absolute path, no ./.. segments
    timeout: 10m           # 1s–24h; the full process tree is killed on expiry
    concurrency: exclusive # "shared" (default) or "exclusive"
    parameters:
      image_tag:
        type: string                          # only "string" in v1
        pattern: '^[a-zA-Z0-9._-]{1,128}$'    # validated before the script ever runs
        required: true
```

- `command` must be an absolute, clean path to an existing, executable,
  non-symlink file, owned by root (or the Axiom service account) and not
  group/world-writable — checked all the way up the directory tree, not
  just the file itself. Axiom refuses to start otherwise.
- `timeout` bounds how long the script may run before Axiom sends `SIGTERM`
  to its whole process group, waits a short fixed grace period, then
  `SIGKILL`s it.
- `concurrency: exclusive` serializes triggers of that one action (a second
  trigger while one is running gets `409`); `shared` (the default) allows
  concurrent runs.
- `parameters` is the complete accepted set for that action — a request
  parameter not listed here is a `400`, not silently dropped or passed
  through.

## Authorization

```yaml
authorization:
  identities:
    example-ci:              # must match a client cert's Common Name exactly
      actions:
        - backend.deploy
        - backend.rollback
```

Default-deny: an identity with no entry, or an action not on its list, is
rejected (`403`). There is no wildcard or implicit-allow.

## Output and job history limits

```yaml
output:
  max_bytes: 2097152   # optional, default 2 MiB; per stream, per job

jobs:
  max_history: 1000    # optional, default 1000; oldest FINISHED job is
                        # evicted once exceeded, a running job never is
```

Job history is in-memory only and does not survive a restart — the audit
log (`audit.path`, default `/var/log/axiom/audit.log`) is the durable
execution record. See [`operations`](INSTALL.md#11-job-history-and-restart-behavior).

## Audit log path

```yaml
audit:
  path: /var/log/axiom/audit.log   # optional, this is the default
```
