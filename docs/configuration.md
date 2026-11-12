# Configuration Reference

Axiom reads one YAML file (`-config`, default `/etc/axiom/config.yaml` on
unix, `C:\ProgramData\Axiom\config.yaml` on Windows), validated in full at
startup — any structural problem, missing/insecure file, invalid reference,
or out-of-range value aborts startup rather than running with a partially
valid configuration. See [`configs/example.yaml`](../configs/example.yaml)
(and [`configs/example-windows.yaml`](../configs/example-windows.yaml)) for
complete annotated examples.

**The schema is identical on Linux and Windows.** The only Windows
differences are that paths are Windows paths (plain unquoted scalars with
backslashes, already in clean form) and each `actions.<name>.command` must
be an absolute path ending `.ps1`; the file-security requirements below are
enforced with NTFS ACLs instead of POSIX ownership/mode. There is no
Windows-specific configuration block. See
[`INSTALL-WINDOWS.md`](INSTALL-WINDOWS.md).

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
  your.action.name:
    command: /opt/axiom/actions/your-script.sh  # absolute path, no ./.. segments
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
        - your.action.name
```

Default-deny: an identity with no entry, or an action not on its list, is
rejected (`403`). There is no wildcard or implicit-allow.

## Output and job history limits

```yaml
output:
  max_bytes: 2097152   # optional, default 2 MiB; per stream, per job

jobs:
  max_concurrent: 16   # optional, default 16 (1-1024); queued+running jobs
                        # across ALL actions
  max_history: 1000    # optional, default 1000; oldest FINISHED job is
                        # evicted once exceeded, a running job never is
```

At `jobs.max_concurrent` a trigger returns `429` with `Retry-After` and
creates no job; an `exclusive` action's lock is not left held by a rejected
request. Every slot is released when a job ends (success, failure, timeout,
cancellation, or shutdown).

**Memory bound.** Finished jobs keep their captured output in memory until
evicted. The worst case is roughly
`max_history x 2 streams x output.max_bytes` plus the same for running jobs:
about 4 GiB at the defaults (1000 x 2 x 2 MiB), if every job fills both
streams. Size these to the host: for example `max_history: 200` and
`max_bytes: 1048576` bound retained output at about 400 MiB. Per-request
limits: body 64 KiB, 64 parameters, 4096 bytes per value; HTTP header and
timeout limits are fixed in `cmd/axiom/main.go`. Axiom does not rate-limit
connections or requests; put it behind your network controls, and add
systemd limits (`MemoryMax`, `TasksMax`, `LimitNOFILE`) in the unit for
resource containment of the whole process tree.

Job history is in-memory only and does not survive a restart — the audit
log (`audit.path`, default `/var/log/axiom/audit.log`) is the durable
execution record. See [`operations`](INSTALL.md#11-job-history-and-restart-behavior).

## Audit log

```yaml
audit:
  path: /var/log/axiom/audit.log   # optional, this is the default
  syslog: true                     # optional, default false; Linux only
```

`audit.syslog` additionally emits every audit record as one JSON message to
journald/syslog (facility AUTH, identifier `axiom-audit`). Axiom refuses to
start if the syslog socket is unavailable, and refuses to run an action whose
accept record cannot be delivered. The local file is owned by the service
account that runs your actions, so treat it as application-level evidence;
forward the syslog stream to a central collector the service account cannot
modify:

> **Local audit file: operational copy only.**
> **Central collector: authoritative audit record.**

Setting `audit.syslog` on Windows is a config error, so the Windows build has
no forwarded audit trail. To check the path on a real host, follow
[`audit-validation.md`](audit-validation.md).
