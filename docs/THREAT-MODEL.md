# Axiom — Threat Model

Generic by design: no company-specific hosts, IPs, credentials, or private
infrastructure details. This is the v1 security review referenced by the
project's hardening work; findings here that required a code change have
already been fixed (see the corresponding commit), not merely documented.

For each scenario: what an attacker in that position can and cannot do, and
which specific control is responsible for the boundary.

---

## 1. Remote attacker without credentials

**Can:** open a TCP connection to the listener, attempt a TLS handshake.
**Cannot:** complete any request. `security.mtls.ca_file` is the sole trust
root; `internal/auth.TLSConfig` requests a client certificate, and every
route except an explicitly-opted-in anonymous `/health` requires one that
chain-verifies against that CA (`internal/auth.Verify`, enforced in
`internal/api` middleware — see [`internal/api/middleware.go`](../internal/api/middleware.go)).
No certificate, or a certificate not signed by the configured CA, gets `401`
with no further processing.
**Blast radius:** none. TLS handshake / connection-level noise only.

## 2. Attacker with an unauthorized (but CA-trusted) client certificate

E.g. a certificate the CA issued for a different, unrelated purpose, or a
decommissioned identity whose cert is still technically valid.
**Can:** complete the TLS handshake and reach the authorization check.
**Cannot:** trigger any action. Authorization is an explicit
identity → allowlist mapping (`config.Identity.IsAllowed`) with **default
deny**: an identity absent from `authorization.identities`, or an action
absent from that identity's list, is rejected (`403`) — there is no
wildcard or implicit-allow path. Every rejection is written to the audit
log (`audit.EventRejected`) with the identity and requested action.
**Blast radius:** none beyond a visible rejected-request audit entry (useful
for detecting a cert that should be revoked).
**Residual risk:** Axiom performs no certificate revocation check (no
CRL/OCSP) — a compromised-but-not-yet-revoked-by-CA certificate for an
*already-authorized* identity is covered by scenario 3, not this one. If
your CA supports short-lived certs, prefer that over long-lived ones for
this reason.

## 3. Compromised Jenkins credential / private key (an authorized identity, key stolen)

**Can:** do everything the real Jenkins controller for that identity could
do: trigger every action on that identity's allowlist, with any parameter
values that pass each action's declared schema, as many times as the
concurrency policy permits.
**Cannot:** trigger any action outside that identity's allowlist, run
arbitrary commands (there is no such endpoint), or pass parameters outside
each action's declared name/type/pattern.
**Blast radius:** bounded by exactly what that one identity is allowed to
do — this is the core reason `authorization.identities` should be scoped as
narrowly as each real CI system's actual job requires, per identity, rather
than one broad "ci" identity shared across unrelated pipelines. Every
triggered action is in the audit trail with that identity's name, giving a
full record for incident response.
**Mitigating control that's outside Axiom's scope:** credential rotation
and Jenkins-side secret storage are the CI system's responsibility; Axiom
can't detect "this is really Jenkins" vs. "this is someone who stole
Jenkins's key" — that's inherent to bearer-credential (here,
possession-of-key) authentication and is why the allowlist should be as
narrow as practical per identity.

## 4. Compromised Jenkins controller (attacker has full control of the CI system, not just one credential)

**Can:** everything scenario 3 covers, across every identity/credential the
controller holds (potentially several, if multiple pipelines' credentials
live on the same controller).
**Cannot:** exceed the union of what those identities are allowed to do;
still cannot run arbitrary commands or bypass any action's parameter
schema.
**Blast radius:** the union of all actions reachable from that controller's
credentials. This is why running one Axiom agent per environment/blast-
radius boundary (as the architecture already does — one agent per server,
config-driven per-server action sets) limits how much a single compromised
controller can reach, versus a hypothetical central multi-tenant control
plane (explicitly out of scope for this reason among others).

## 5. Low-privilege local user on the target server (not root, not the axiom account)

**Can:** read whatever the filesystem's normal permission model already
lets them read; observe the running axiom process in `ps`.
**Cannot:** read `/etc/axiom/certs/server.key` (mode `0600`, owned
`root:axiom`), read or modify `/etc/axiom/config.yaml` or any action script
(root-owned, `0750`/`0640`, not group/world-writable — enforced both by the
installed file modes and by Axiom refusing to start at all if any of these
is found to be insecure at load time), or write to `/var/log/axiom` (owned
`axiom:axiom`, not group/world-writable). Cannot escalate: the axiom
service account itself runs with `CapabilityBoundingSet=` (empty) and
`NoNewPrivileges=true`.
**Blast radius:** none, assuming the standard install permissions in
[`docs/INSTALL.md`](INSTALL.md) §2/§8 are followed. A misconfigured install
(e.g. an operator hand-copying a script with `0777`) is caught at Axiom's
own startup validation regardless (`internal/config` script/parent-directory
ownership and writability checks) and refuses to start rather than run
insecurely.

## 6. Malicious or misconfigured action script

Action scripts are operator-authored and root-installed; Axiom has no
visibility into what they actually do — this is by design (the agent has
zero domain knowledge, per the architecture). The relevant question is what
Axiom's own controls limit regardless of script content.
**Can:** do anything the axiom service account's OS-level permissions and
the systemd sandbox allow (network access, the paths declared in the
service's `ReadWritePaths`, etc. — see `packaging/axiom.service`). A
genuinely malicious script that got past root-only script installation is
already a compromise of the root-controlled install pipeline, not an Axiom
control boundary.
**Cannot:** exceed its configured `timeout` (the full process tree is
killed on expiry — verified by an integration test that spawns a
background child and confirms it doesn't outlive the timeout), produce
unbounded captured output (hard-capped per `output.max_bytes`), or run
outside the concurrency policy declared for its action (`exclusive` actions
are serialized via a per-action lock).
**Blast radius:** bounded by OS/systemd-level sandboxing, not by Axiom
application logic — this is the correct boundary for something Axiom
intentionally doesn't interpret. Least-privilege script installation (root-
owned, reviewed before deployment, per §7 of the install guide) is the real
control here.

## 7. Malformed or malicious HTTP request

Oversized bodies, deeply nested/garbage JSON, unknown fields, wrong
Content-Type, excessive parameter counts, oversized parameter values,
malformed job IDs, disallowed action-name characters, wrong HTTP method.
**Can:** send any bytes over an authenticated TLS connection.
**Cannot:** get past `internal/api` request validation, which rejects each
of the above with a clean `4xx` before the request reaches the job manager:
64 KiB body cap, `DisallowUnknownFields` JSON decoding, Content-Type
enforcement, a 64-parameter/4096-byte-value request-level cap (defense in
depth ahead of each action's own declared schema), action-name and job-ID
charset validation, and `net/http`'s built-in method-not-allowed handling
for registered routes. None of this input reaches `os/exec` as anything
other than validated environment-variable values (§10 below).
**Blast radius:** none beyond the rejected request itself; none of these
paths reach action execution.

## 8. Replayed request

An attacker who can observe (or has previously captured) a legitimate,
successfully-authenticated request and resends it.
**Analysis:** Axiom has no replay-prevention nonce/timestamp mechanism in
v1. A replayed `POST /v1/actions/{action}` is, from Axiom's perspective,
indistinguishable from the caller legitimately triggering the same action
again — which is not inherently a security violation, since triggering
that action is exactly what that identity is authorized to do.
**What actually limits this:** (a) TLS itself — a replayed request requires
either a MITM position on an already-authenticated mTLS channel (out of
scope; equivalent to compromising the channel itself) or a captured
plaintext request, which mTLS specifically prevents in transit; (b) an
`exclusive`-concurrency action naturally rejects (`409`) a rapid-fire
replay while the original is still running; (c) every trigger — replayed or
not — is fully audited with identity, action, and parameters, so a
suspicious repeat is visible after the fact.
**Residual risk, honestly stated:** a shared-concurrency, idempotent-by-
design action (e.g. `service.restart`) triggered twice in a row via replay
is not detected as anomalous by Axiom itself. If a specific action is
sensitive to duplicate execution, that action's *script* should be written
idempotently (checking current state before acting) — the spec's action-
result-validation guidance already recommends this regardless of replay
concerns.

## 9. Concurrent deployment requests (same action, overlapping in time)

**Can:** two callers (or one caller racing itself) issue
`POST /v1/actions/backend.deploy` at nearly the same instant.
**Cannot:** actually run both simultaneously if the action is configured
`concurrency: exclusive` — the second request gets `409` immediately
(`jobs.ErrActionBusy`), verified by a test that starts a slow action and
confirms a second trigger during its execution is rejected, and that the
lock is released (allowing a subsequent trigger) promptly once the first
job reaches a terminal state — not only after the lock-holder's audit
write completes, which would otherwise create an unnecessary window where
a caller could stall on an already-finished job.
**Blast radius:** for `concurrency: shared` actions, both are allowed to
run concurrently — this is the declared policy for actions where that's
safe (e.g. a read-only status check); it is a per-action config choice, not
an oversight.

## 10. Action-parameter injection

**Can:** submit any string as a declared parameter's value.
**Cannot:** turn that value into shell syntax, an extra command, or an
extra argument the script didn't expect. Two independent controls: (a)
`exec.Command` invokes the script binary directly — never through
`/bin/sh -c` — so there is no shell to inject into in the first place; (b)
every declared parameter is validated against its configured regex
`pattern` before the value is accepted, and reaches the child process only
as a specific `AXIOM_PARAM_<NAME>` environment variable, never
concatenated into a command line. A value like `; rm -rf /` either fails
its pattern (rejected, `400`, verified by test) or, if the action genuinely
declared no pattern for that parameter, arrives in the environment as an
inert string — the script only becomes vulnerable if *it* then does
something unsafe with that environment variable (e.g. `eval`s it), which is
a script-authoring concern documented in `scripts/examples/*.sample`, not
an Axiom-side gap.
**Blast radius:** none through Axiom's own execution path. Declaring a
pattern for every parameter that reaches a script is the recommended
practice, not merely optional.

## 11. Symlink / path manipulation

**Can:** attempt to have `command:` or a certificate/audit path in config
point through a symlink.
**Cannot:** use a symlink to make an insecurely-owned or writable target
pass Axiom's security checks — `checkScriptSecurity` and
`requireSecureParentDir` use `os.Stat` (which follows symlinks), so the
*real* target's ownership/permissions are what's judged, not the symlink's;
a relative path or a path containing `.`/`..` segments is rejected outright
at config load (`command != filepath.Clean(command)`); every ancestor
directory in the resolved path is walked and must be non-group/world-
writable (except where the sticky bit legitimately protects it, e.g.
system `/tmp` — not a location used for Axiom's own paths).
**Blast radius:** none — this class of attack is caught at config-load
time (startup failure), not discovered at execution time.

## 12. Partial deployment failure

E.g. a deploy script pulls a new image successfully but the service fails
its own health check before the script exits.
**Analysis:** this is explicitly the action script's responsibility to
detect and report, not Axiom's — Axiom only observes the script's exit
code (and timeout/output). The spec's guidance (see
[`scripts/examples/backend-deploy.sh.sample`](../scripts/examples/backend-deploy.sh.sample))
is for the script itself to wait for and verify real application health
before exiting `0`, so "launched but broken" correctly surfaces to the
caller as a failed job rather than a false success.
**What Axiom guarantees regardless:** the job's terminal state
(`succeeded`/`failed`/`cancelled`), exit code, and captured output are
always available via the job/logs endpoints and always audited — the
caller (e.g. a Jenkins pipeline) has what it needs to decide whether to
alert or roll back. Axiom does not attempt automatic rollback orchestration
(explicitly out of v1 scope) — that decision stays with the calling system.

## 13. Axiom restart or crash during a job

**Can:** the axiom process is killed (crash, OOM, `systemctl restart`,
host reboot) while a job is `running`.
**Effect:** the child process being executed is in its own process group,
started via `os/exec` from the axiom process — it is not guaranteed to be
killed just because axiom itself died (no PR_SET_PDEATHSIG or equivalent is
set in v1), so a long-running deploy script could, in principle, keep
running orphaned after axiom exits. The in-memory job record for it is
lost (§11 of the install guide) — a caller polling `GET /v1/jobs/{id}`
after axiom restarts gets `404`, not stale state.
**What's durable regardless:** the audit log already has the `accepted` and
`started` records for that job (both written synchronously before/at
execution start) — an operator can see from the audit trail that a job was
in flight when the crash happened, even though no `finished` record was
ever written for it. This is stated plainly rather than glossed over: v1
does not reconcile in-flight jobs on restart, and does not guarantee killing
orphaned children on crash. If your action scripts are not safely
re-runnable after an interrupted execution, that's a script-design
consideration independent of Axiom (the spec's real-health-check guidance
in §12 above also mitigates this: a caller re-triggering after a restart
will get an accurate health-checked result either way).
**Blast radius:** an orphaned child process running to completion
unsupervised — bounded by whatever that script itself does, same as any
other unsupervised process; not a security boundary bypass, since the
script was legitimately authorized to run when it started.

---

## Summary of fixes made during this hardening pass (not just documented)

- Config now validates action/parameter/identity name charsets, script and
  cert/key/audit *directory* ownership and writability (not just the file
  itself), CA/cert/key PEM validity, and sane bounds on timeouts, output
  size, and parameter counts — closing the "insecure directory, secure
  file" gap and several "garbage config accepted, fails later" gaps.
- The exclusive-concurrency lock is now released as soon as a job reaches
  a terminal state, not after its completion-audit write — closing a
  window where a legitimate next trigger could be rejected as "busy" for a
  job that had, from the caller's perspective, already finished.
- A goroutine panic during job execution is now recovered, recorded as a
  failed job, and audited — previously it would have crashed the entire
  agent process, taking down every other in-flight and future job as
  collateral damage.
- The in-memory job store is now bounded (`jobs.max_history`, default
  1000, oldest terminal job evicted) — previously unbounded and could grow
  without limit over the agent's uptime.
- Request-level hardening added: unknown-JSON-field rejection,
  Content-Type enforcement, action-name and job-ID format validation, and
  request-level parameter count/size caps ahead of the job manager.
- TLS listener now pins to forward-secret AEAD cipher suites for a TLS 1.2
  fallback and sets explicit curve preferences (TLS 1.3 already uses a
  fixed strong suite set by design of the standard library).
- `http.Server` now sets `ReadTimeout`/`WriteTimeout`/`IdleTimeout`/
  `MaxHeaderBytes`, previously only `ReadHeaderTimeout` was set.
