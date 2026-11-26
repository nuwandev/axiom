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
    # max_connections: 256   # optional, off (0) by default: most connections
    #                        # open at once, TLS handshakes included
```

**Connection cap.** `agent.listen.max_connections` (0-100000, default 0 = off)
limits how many connections are open at the same time. A connection counts from
the moment it is accepted, so peers stalled in the TLS handshake use slots too.
At the cap Axiom stops accepting; further clients wait in the operating
system's listen backlog and are served as slots free up, and the existing
HTTP timeouts (10 s to send headers, 2 min idle) release slots held by stalled
peers. Set it above the number of concurrent clients a legitimate pipeline
needs. It is restart-only. Failed handshakes are logged at `WARN` by the
structured logger (`http: TLS handshake error from ...`); they are not audit
records, so an unauthenticated peer cannot cause a synced audit write per
attempt.

## Listener / TLS / mTLS

```yaml
security:
  mtls:
    ca_file: /etc/axiom/certs/ca.crt        # internal CA — the trust root for client certs
    cert_file: /etc/axiom/certs/server.crt  # this agent's certificate
    key_file: /etc/axiom/certs/server.key   # this agent's private key
    # crl_file: /etc/axiom/certs/ca.crl     # optional certificate revocation list
  health:
    allow_anonymous: false   # default; only set true if a network health
                              # check genuinely cannot present a client cert
```

Every request except `/health` (when `allow_anonymous` is left `false`,
the default) requires a client certificate that chain-verifies against
`ca_file`. TLS 1.2 minimum, with cipher suites restricted to forward-secret
AEAD suites on a 1.2 handshake (TLS 1.3 uses the standard library's fixed
strong suite set).

**Revocation list.** `security.mtls.crl_file` is optional. When set, a client
certificate whose serial number is listed is refused with `401` and an audit
`rejected` record, on every request, so a revocation also applies to
connections that are already open. The file is a PEM or DER CRL that must be
signed by a CA in `ca_file`, and it is held to the same ownership and
directory rules as the CA file, because anyone who can edit it can un-revoke a
certificate. Axiom reads it at startup and on reload
([`certificates.md`](certificates.md#reload-after-renewal)); it never fetches
it, and it does not do OCSP. Only the client certificate itself is checked, not
intermediates in its chain.

The list is trusted only until its `nextUpdate` time. At startup or reload an
already expired list is refused. If it expires while the agent runs, every
client is refused until a fresh list is published and reloaded: after
`nextUpdate` the list may be missing revocations, so Axiom fails closed
rather than keep trusting it. Refresh the file and reload before then.

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
  max_triggers_per_minute: 0   # optional, default 0 = no limit (0-10000);
                                # per identity, see below
  idempotency_window: 10m      # optional, default 10m (1s-24h), see below
  # state_path: /var/lib/axiom/jobs.state   # optional: keep job status and
                                # idempotency keys across restarts, see below
```

At `jobs.max_concurrent` a trigger returns `429` with `Retry-After` and
creates no job; an `exclusive` action's lock is not left held by a rejected
request. Every slot is released when a job ends (success, failure, timeout,
cancellation, or shutdown).

**Trigger rate limit.** `jobs.max_triggers_per_minute` caps how often one
identity may trigger actions. Each identity has its own token bucket that
starts full: it may trigger that many actions at once and regains one every
`60 / max_triggers_per_minute` seconds. Past the limit a trigger returns `429`
with a `Retry-After` of at least one second and creates no job. Every trigger
that reaches the job manager is charged, including one later rejected for an
invalid parameter or a busy exclusive action, so values cannot be probed faster
than the limit. Polling (`GET /v1/jobs/...`), `/health`, and requests rejected
earlier (authentication, authorization, malformed body) are not charged. A
rate-limited request is not written to the audit trail, like a request refused
at `jobs.max_concurrent`, so a flood cannot turn into synced audit writes. The
limit is off by default so an upgrade changes no behaviour; set it to the
highest rate a legitimate pipeline needs.

**Idempotency window.** `jobs.idempotency_window` (1s-24h, default `10m`) is how
long an `Idempotency-Key` sent with a trigger is remembered. Within it, the same
identity, action and key returns the original job and starts nothing; see
[`jenkins-integration.md`](jenkins-integration.md#job_id-and-what-a-lost-connection-means)
for the client rules. Keys are lost on restart unless `jobs.state_path` is set
(below), at most 10,000 are held at once, and past that new keys get `429` while
existing ones keep working. Requests without the header are unaffected. It is
restart-only.

**Job state across restarts.** `jobs.state_path` is an absolute path to a file
that keeps each job's status, exit code, error and times, plus idempotency keys,
so they survive a restart. It is off by default, so an upgrade changes nothing.
The directory must not be writable by anyone but the service account and root
(the same rule as `audit.path`), and the file cannot be the audit log.
Script output and parameter values are never written to it. After a restart the
newest `jobs.max_history` jobs answer `GET /v1/jobs/{id}` with their recorded
status; their `/logs` answer `410 Gone`; and a job that was queued or running
at the time comes back `cancelled` with no duration. Write failures are logged
and do not fail the job. The file is not a record of truth (the audit log is),
and a damaged file, other than a half-written last line, stops Axiom from
starting. See [`INSTALL.md` §11](INSTALL.md#11-job-history-and-restart-behavior).
It is restart-only. On Windows use a path in the service's writable data
directory, for example `C:\ProgramData\Axiom\logs\jobs.state`.

**Memory bound.** Finished jobs keep their captured output in memory until
evicted. The worst case is roughly
`max_history x 2 streams x output.max_bytes` plus the same for running jobs:
about 4 GiB at the defaults (1000 x 2 x 2 MiB), if every job fills both
streams. Size these to the host: for example `max_history: 200` and
`max_bytes: 1048576` bound retained output at about 400 MiB. Per-request
limits: body 64 KiB, 64 parameters, 4096 bytes per value; HTTP header and
timeout limits are fixed in `cmd/axiom/main.go`. Connections can be capped with
`agent.listen.max_connections`; Axiom does not rate limit new connections, TLS
handshakes or requests other than triggers, so keep it behind your network
controls, and add systemd limits (`MemoryMax`, `TasksMax`, `LimitNOFILE`) in
the unit for resource containment of the whole process tree.

Job history is in memory only and does not survive a restart unless
`jobs.state_path` is set, and even then output does not. The audit log
(`audit.path`, default `/var/log/axiom/audit.log`) is the durable execution
record. See [`operations`](INSTALL.md#11-job-history-and-restart-behavior).

## Audit log

```yaml
audit:
  path: /var/log/axiom/audit.log   # optional, this is the default
  syslog: true                     # optional, default false; Linux and Windows
```

`audit.syslog` additionally emits every audit record, as one JSON message, to
the platform's system log. The setting keeps its name on both platforms:

- **Linux:** journald/syslog (facility AUTH, identifier `axiom-audit`). Axiom
  refuses to start if the syslog socket is unavailable.
- **Windows:** the Application event log, source `axiom` (registered by the
  installer). Each record is the event text. `rejected` and `reload_failed`
  records are warnings; `accepted`, `started` and `finished` are informational
  with event IDs 101, 102 and 103 (`rejected` is 104), and a configuration
  reload is 105 (`reloaded`, informational) or 106 (`reload_failed`, warning),
  so a collector can filter on them. Axiom
  refuses to start if the source is not registered. One event string is limited
  to 31,839 characters, and a record over that limit is not truncated: the
  write fails.

On either platform Axiom refuses to run an action whose accept record cannot be
delivered. The local file is owned by the service account that runs your
actions, so treat it as application-level evidence; forward the system-log
stream to a central collector the service account cannot modify:

> **Local audit file: operational copy only.**
> **Central collector: authoritative audit record.**

Windows Event Log entries carry no sender attribute a script cannot also set,
so a script running as the service account could add an entry that looks like an
Axiom record; the central collector is still the authoritative copy, and
records are compared with the local file. To check the path on a real host,
follow [`audit-validation.md`](audit-validation.md).
