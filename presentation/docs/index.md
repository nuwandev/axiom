---
title: Architecture presentation
---

<div class="ax-hero" markdown>

<p class="ax-kicker">Architecture and engineering presentation</p>

# Axiom in a banking infrastructure { .ax-title }

<p class="ax-lede">Axiom lets a trusted CI system trigger predefined actions on a server over mutual TLS, without giving it SSH. This presentation places the agent in an illustrative banking estate, then opens it up and follows one request through the Go implementation.</p>

<div class="ax-facts">
<div><p class="k">Release described</p><p class="v">v2.3.0, source at the release tag</p></div>
<div><p class="k">Platforms</p><p class="v">Linux (systemd), Windows (service)</p></div>
<div><p class="k">API surface</p><p class="v">Four HTTP routes</p></div>
<div><p class="k">Outbound connections</p><p class="v">None made by Axiom</p></div>
<div><p class="k">Evidence</p><p class="v">Go source, tests, packaging</p></div>
</div>

<ol class="ax-index">
<li><a href="#introduction">Introduction</a><span>What Axiom is, what it is not, how to read this page</span></li>
<li><a href="#environment">The banking infrastructure environment</a><span>CI/CD zone, firewall, bank zone and every traffic class</span></li>
<li><a href="#topology">Axiom deployment topology</a><span>The agent on a managed host, and what the network must allow</span></li>
<li><a href="#scenario">End-to-end deployment scenario</a><span>From a pipeline trigger to a polled result</span></li>
<li><a href="#internals">Internal architecture</a><span>Packages, responsibilities and source locations</span></li>
<li><a href="#lifecycle">Request lifecycle</a><span>The audited path, including every rejection</span></li>
<li><a href="#authn">Authentication and authorization</a><span>Certificate identity and the default-deny allowlist</span></li>
<li><a href="#jobs">Job execution</a><span>State machine, admission control, output and ownership</span></li>
<li><a href="#audit">Audit architecture</a><span>JSONL, fsync, redaction, sinks and failure behaviour</span></li>
<li><a href="#platforms">Linux and Windows implementations</a><span>Ten concerns, side by side</span></li>
<li><a href="#boundaries">Security boundaries</a><span>What enforces each boundary, and what remains</span></li>
<li><a href="#limits">Failure handling and limitations</a><span>Stated plainly, with source references</span></li>
</ol>

</div>

## 1. Introduction {#introduction}

Axiom is one static Go binary that runs on a managed host and exposes four routes. A caller with a certificate from the bank's own CA can ask it to run an action that the host's operator declared in advance. It cannot ask for anything else: there is no field, route or parameter through which a request names an executable, an argument or a script path.

Everything that follows separates two things that are easy to blur in an architecture diagram.

| Axiom, implemented and source-verified | The bank, illustrative or customer-owned |
|---|---|
| mTLS listener, per-request certificate verification, Common Name identity | Jenkins, pipelines, the credential store |
| Default-deny identity-to-action allowlist, owner-only job access, optional CRL check | Firewalls, segmentation, WAF and load balancer |
| Fixed, validated execution: no shell, no arbitrary path | Web, application and database servers, and the application on them |
| Timeouts, output caps, job, trigger-rate and connection limits, process-tree containment | The action scripts: deploys, restarts, registry pulls |
| JSONL audit with fsync, system-log sink (journald or Windows Event Log) | SIEM or collector, retention, forwarding from the host |
| Integrity checks on config, certificates and scripts, at startup and on every reload | PKI: issuing, rotating and revoking certificates, publishing the CRL |

!!! note "The topology is an example"
    The banking layout in sections 2 and 3 is an illustrative deployment pattern. Axiom has no concept of tiers, banks, a fleet or a central controller. Each agent knows only its own configuration.

Sections 2 to 4 are about deployment. Sections 5 to 12 are about the implementation, and every behavioural claim names the file or function that backs it.

## 2. The banking infrastructure environment {#environment}

Three areas matter. An engineering zone runs Jenkins and the build. A perimeter firewall separates it from the bank network. Inside the bank zone, customer traffic flows through web, application and database tiers, and a separate path carries Axiom's control traffic to the agent on the host being deployed to. In this example that is the application server.

<figure class="ax-fig" id="fig-topology" markdown>
<div class="ax-scroll">
--8<-- "topology.svg"
</div>
<figcaption>Figure 1. Illustrative banking deployment. Blue is Axiom's control plane, teal is local execution, grey is customer application traffic, amber is audit forwarding. Numbered badges follow the scenario in section 4. Only the application server runs an agent in this example; the web and database hosts do not. Port 8443 is the value in the shipped example configuration; the operator sets it.</figcaption>
</figure>

Reading the figure:

Control plane
:   Solid blue. The Jenkins build executor opens a TCP connection to port 8443 on a target host and speaks HTTPS with mutual TLS. Jenkins always initiates; the agent never calls back.

Local execution
:   Dotted teal. The agent runs a predefined script on its own host. What the script does, such as a deployment, a service restart or an artifact pull, is the bank's logic, not Axiom's.

Application traffic
:   Grey, in its own lane on the right. Customers reach the web tier through the WAF or load balancer, the web tier calls the application tier, and the application tier talks to the database. Axiom does not proxy, inspect or manage any of it.

Optional artifact pull
:   Dashed grey. If a script pulls an image or artifact from the registry, that is the script's own network path and the bank's own firewall rule.

Audit and certificates
:   Dashed amber is audit forwarding, available on both platforms: journald or syslog on Linux, the Application event log on Windows. Dotted grey is certificate issuance by the bank's PKI, which sits entirely outside Axiom.

!!! warning "No TLS-terminating device in the Axiom path"
    The agent takes the caller's identity from the client certificate presented on the TCP connection it terminates (`internal/api/middleware.go`, `withIdentity`). There is no forwarded-header support. A reverse proxy or a TLS-terminating WAF between Jenkins and the agent would remove the certificate and break authentication. Direct connections or Layer 4 TCP passthrough are the only supported shapes.

The web and database hosts run no agent in this example. A host gets one only when Jenkins must trigger an action on that host itself, such as a local operation on the database host. Application-to-database SQL has no relationship to Axiom.

## 3. Axiom deployment topology {#topology}

A host that Jenkins must act on runs its own agent, with its own configuration, its own server certificate and its own allowlist. Agents do not know about one another, and nothing aggregates them. A pipeline that deploys to four hosts makes four independent requests.

<figure class="ax-fig" id="fig-host" markdown>
<div class="ax-scroll">
--8<-- "host.svg"
</div>
<figcaption>Figure 2. One managed host. The service account reads its binary, configuration, certificates and scripts but cannot change them; it can write only the audit log directory and its state and temp locations.</figcaption>
</figure>

### What the network must allow

| Path | Direction | Port | Notes |
|---|---|---|---|
| CI executor to agent | Inbound to the host | The configured `agent.listen.port` (8443 in the shipped examples) | Mutual TLS, no termination in between |
| Axiom to anything else | None | n/a | The agent makes no outbound connections |
| Script to registry or services | Outbound, defined by the script | Customer defined | The systemd unit permits `AF_INET`, `AF_INET6`, `AF_UNIX`, `AF_NETLINK` |
| Host to log collector | Through the local syslog daemon or the Windows Event Log | Customer defined | Both platforms |

### Provisioning, as the installers actually behave

- Neither installer generates certificates, overwrites an existing `config.yaml` or starts the service. The operator places certificates and configuration, then starts it.
- Linux creates a dedicated `axiom` user and a root-owned layout under `/etc/axiom` and `/opt/axiom/actions`; only `/var/log/axiom` and `/var/lib/axiom` belong to the service account.
- Windows registers the service as `NT SERVICE\axiom` and applies inheritance-protected NTFS ACLs, with modify rights only on `logs\`.
- The only example action Axiom installs is `hello.world`, a script that prints a greeting and exits 0.

## 4. End-to-end deployment scenario {#scenario}

<ol class="ax-steps">
<li>A developer action or a source-control webhook starts the Jenkins pipeline.</li>
<li>Jenkins checks out the application source.</li>
<li>Jenkins builds, tests and publishes the artifact to the registry.</li>
<li>The deployment stage invokes a predefined Axiom action.</li>
<li>Jenkins sends an HTTPS request carrying its client certificate.</li>
<li>The request crosses the firewall and reaches the target host's agent.</li>
<li>Axiom authenticates the certificate, authorizes the action and validates the parameters.</li>
<li>Axiom writes the accepted audit record and creates an asynchronous job.</li>
<li>Axiom writes the started audit record and runs the preconfigured local script.</li>
<li>Jenkins polls the job with the same certificate.</li>
<li>Jenkins receives the terminal status and marks the build.</li>
</ol>

<figure class="ax-fig" markdown>
<div class="ax-mmd">
--8<-- "mermaid/scenario.svg"
</div>
<figcaption>Figure 3. The deployment scenario. Responses travel on the connection Jenkins opened. Only Jenkins initiates; the pipeline itself can be started by a developer or a webhook.</figcaption>
</figure>

### The API in detail

=== "Trigger"

    ```http
    POST /v1/actions/your.deploy HTTP/1.1
    Host: app-01.internal.example:8443
    Content-Type: application/json
    Idempotency-Key: deploy-build-1842

    {"parameters": {"image_tag": "build-1842"}}
    ```

    ```http
    HTTP/1.1 202 Accepted
    Content-Type: application/json

    {"job_id":"01J8ZQ4M6T3N7V2K9R5W0XDHBE","status":"queued"}
    ```

    The `Idempotency-Key` header is optional. A retry with the same key within `jobs.idempotency_window` (default 10 minutes) returns this job with `Idempotent-Replay: true` and starts nothing.

=== "Poll"

    ```http
    GET /v1/jobs/01J8ZQ4M6T3N7V2K9R5W0XDHBE HTTP/1.1
    ```

    ```json
    {
      "job_id": "01J8ZQ4M6T3N7V2K9R5W0XDHBE",
      "action": "your.deploy",
      "status": "succeeded",
      "exit_code": 0,
      "started_at": "2026-11-20T09:14:03.412Z",
      "finished_at": "2026-11-20T09:14:41.907Z",
      "duration_ms": 38495
    }
    ```

=== "Logs"

    ```http
    GET /v1/jobs/01J8ZQ4M6T3N7V2K9R5W0XDHBE/logs HTTP/1.1
    ```

    ```json
    {
      "stdout": "deploying image tag: build-1842\n",
      "stderr": "",
      "stdout_truncated": false,
      "stderr_truncated": false
    }
    ```

=== "Server side"

    The host's operator declared the action, its parameter schema and who may run it. A request can select only what exists here.

    ```yaml
    actions:
      your.deploy:
        command: /opt/axiom/actions/deploy.sh
        timeout: 10m
        concurrency: exclusive
        parameters:
          image_tag:
            type: string
            pattern: '^[a-zA-Z0-9._-]{1,128}$'
            required: true

    authorization:
      identities:
        ci-jenkins:            # must equal the client certificate's Common Name
          actions:
            - your.deploy
    ```

    The script receives the validated value as `AXIOM_PARAM_IMAGE_TAG` in its environment. It is never part of a command line.

=== "Status codes"

    | Code | Meaning | Source |
    |---|---|---|
    | 202 | Job accepted, returned before the script starts | `handleTriggerAction` |
    | 400 | Bad action name, body, parameter name or value, or a malformed or repeated `Idempotency-Key` | `handlers.go`, `jobs.validateParameters` |
    | 401 | Certificate chain revoked by the CRL, or the CRL has passed its `nextUpdate`; with anonymous `/health`, also no or an unverifiable certificate | `withIdentity`, `TLSState.Verify` |
    | 403 | Identity not allowlisted for this action, including names that do not exist; or a job read for an action the identity is no longer allowed | `handleTriggerAction`, `ownedJob` |
    | 404 | Job unknown, malformed, or owned by another identity | `ownedJob` |
    | 409 | Exclusive action already running, or the job an `Idempotency-Key` started has left the history | `jobs.ErrActionBusy`, `handleTriggerAction` |
    | 410 | Logs of a job restored from `jobs.state_path`: output is never kept across a restart | `handleGetJobLogs` |
    | 415 | Content type is not JSON | `handleTriggerAction` |
    | 422 | `Idempotency-Key` reused with different parameters | `jobs.ErrIdempotencyKeyReused` |
    | 429 | Agent-wide job cap, this identity's trigger rate limit, or a full idempotency key store, with `Retry-After` | `jobs.ErrTooManyJobs`, `jobs.ErrRateLimited`, `jobs.ErrIdempotencyStoreFull` |
    | 503 | Agent is shutting down | `jobs.ErrShuttingDown` |

## 5. Internal architecture {#internals}

One lifecycle function, `serveWithReload`, builds and wires everything. The unix and Windows entrypoints differ only in how they obtain a context, a configuration and the reload signal (`SIGHUP`, or the service manager's `paramchange`). Platform behaviour lives in build-tagged files behind small seams, so the request path is shared code on both operating systems.

<figure class="ax-fig" id="fig-internal" markdown>
<div class="ax-scroll">
--8<-- "internal.svg"
</div>
<figcaption>Figure 4. Inside one agent. Blue boxes are the request path, amber is audit, teal is process execution. File paths are relative to the repository root.</figcaption>
</figure>

Implementation notes

- **Fail before listening.** `config.Load` validates the whole file, then checks ownership and permissions of the config, CA, certificate, key, optional CRL, every script and each ancestor directory. A problem aborts startup. A reload runs the same `config.Load`, and a problem there changes nothing.
- **The request cannot name what runs.** `executor.Spec` carries a script path, an environment, a timeout and an output cap. There is no field for an executable, an argument or inline source.
- **Audit is part of admission.** The job manager holds the audit logger and will not create or start a job without writing its record.
- **State is small and local.** Runtime state is the in-memory job store, guarded by explicit locks taken in a consistent order, and the audit file. An optional state file keeps job status and idempotency keys across restarts; it never holds output or parameter values.

## 6. Request lifecycle {#lifecycle}

<figure class="ax-fig" id="fig-lifecycle" markdown>
<div class="ax-mmd">
--8<-- "mermaid/lifecycle.svg"
</div>
<figcaption>Figure 5. One request, with the rejection and failure paths. Audit records are written in the order accepted, started, finished, and the first two gate execution.</figcaption>
</figure>

!!! tip "Ordering that matters"
    `accepted` is written before the job is returned to the caller and before any goroutine runs it. `started` is written before `executor.Run` is called. If either write, or its hand-off to a sink, fails, the script does not run. `finished` is best effort because the outcome is already decided.

### Why Axiom never accepts a path or a command

1. **No input carries one.** The only variable in a trigger is the action name, used as a map key into configuration the operator wrote. Parameters are named, typed and matched against a regular expression, then passed as environment variables.
2. **No shell is involved.** On unix the script is executed directly. On Windows the argument vector is fixed and only the configured script path varies; `-Command` and `-EncodedCommand` never appear, and a test asserts it (`executor_windows_test.go`).
3. **The script is verified, not just named.** At startup and on every reload each configured script must be an absolute, clean, non-symlink path, owned by root or Administrators, with no write access for the service account along its whole directory path.
4. **There is no such route.** `TestIntegration_NoArbitraryExecuteEndpoint` asserts an execute-shaped endpoint cannot succeed.

## 7. Authentication and authorization {#authn}

<figure class="ax-fig ax-fig-tight" id="fig-authn" markdown>
<div class="ax-mmd">
--8<-- "mermaid/authn.svg"
</div>
<figcaption>Figure 6. Authentication, then authorization. An unlisted identity, an unlisted action and a misspelt action all receive the same 403.</figcaption>
</figure>

| Property | Behaviour | Source |
|---|---|---|
| Trust root | Only the CAs in `security.mtls.ca_file`; every certificate there is a trust anchor | `auth.TLSState`, `auth.Verify` |
| TLS | Minimum 1.2; ECDHE with AEAD suites on 1.2; X25519 and P-256 | `TLSState.Config` |
| Client verification | Chain to the pool, `clientAuth` extended key usage, current time, non-empty CN | `auth.Verify` |
| Checked twice | At the handshake (default mode) and again on every request, against the CA and CRL current at that moment | `TLSState.Config`, `withIdentity` |
| Identity | Leaf Common Name only; SANs are ignored | `auth.Identity` |
| Authorization | Exact-string membership in a per-identity list; default deny | `config.Identity.IsAllowed` |
| Job access | A job is visible only to the identity that triggered it, and only while that identity is still allowed the job's action | `ownedJob` |
| Revocation | Optional CRL file, one list per issuing CA, every certificate in the chain checked on every request; fails closed after `nextUpdate`; no OCSP | `auth.TLSState.Verify` |

!!! warning "Identity is only as strong as certificate issuance"
    Because identity is the Common Name, the bank's CA must never let a requester choose it. A stolen client certificate keeps working until it expires, is listed in the CRL, or its identity is removed from the configuration; the last two take effect on the next reload. Short-lived, per-pipeline certificates and a published CRL are the mitigations.

## 8. Job execution {#jobs}

<figure class="ax-fig ax-fig-tight" id="fig-states" markdown>
<div class="ax-mmd">
--8<-- "mermaid/states.svg"
</div>
<figcaption>Figure 7. Job states. A job is created in memory only after capacity is reserved, and leaves memory when history is trimmed or the agent restarts. With <code>jobs.state_path</code> set, a job's status (not its output) is restored after a restart, and a job that was in flight comes back cancelled.</figcaption>
</figure>

<figure class="ax-fig ax-fig-tight" id="fig-admission" markdown>
<div class="ax-mmd">
--8<-- "mermaid/admission.svg"
</div>
<figcaption>Figure 8. Admission control. A rejected trigger creates no job and leaves no lock or slot held.</figcaption>
</figure>

| Control | Value | Source |
|---|---|---|
| Action timeout | 1 s to 24 h, per action | `config.MinActionTimeout`, `MaxActionTimeout` |
| Stop behaviour | Unix: SIGTERM to the group, 5 s grace, SIGKILL. Windows: immediate job termination | `executor.Run`, `TerminationGracePeriod` |
| Agent-wide job cap | `jobs.max_concurrent`, default 16 | `config.DefaultMaxConcurrentJobs` |
| Trigger rate per identity | `jobs.max_triggers_per_minute`, off by default; over it, 429 and no job | `Manager.Trigger`, `rateLimiter.allow` |
| Open connections | `agent.listen.max_connections`, off by default; at the cap new clients wait in the listen backlog | `limitListener` |
| Idempotency keys | Optional `Idempotency-Key` header, remembered for `jobs.idempotency_window` (default 10 min), at most 10,000 | `Manager.TriggerIdempotent` |
| Retained history | `jobs.max_history`, default 1000; the oldest finished job is evicted first | `Manager.recordJob` |
| Captured output | `output.max_bytes` per stream, default 2 MiB | `executor.cappedBuffer` |
| Request caps | 64 KiB body, 64 parameters, 4096 bytes per value | `handlers.go` |

Output and ownership
:   Stdout and stderr are captured into capped in-memory buffers, truncated beyond the limit without throttling the process, and returned only to the identity that triggered the job, while it is still allowed the job's action. Output is never written to the audit trail or the state file.

Memory, not storage
:   Job state is not persisted unless <code>jobs.state_path</code> is set. By default a restart, a crash or a host reboot clears it, and a caller polling an old job id receives 404. With the state file, status survives, output never does (its logs answer 410), and a job that was running at the restart comes back cancelled. The audit log is the durable record of what ran either way.

## 9. Audit architecture {#audit}

<figure class="ax-fig" id="fig-audit" markdown>
<div class="ax-scroll">
--8<-- "audit.svg"
</div>
<figcaption>Figure 9. From event to evidence. The local file is an operational copy; the central collector behind the system-log sink (journald on Linux, the Application event log on Windows) is the authoritative record.</figcaption>
</figure>

An accepted-through-finished sequence for one job, as it appears in the file (one JSON object per line):

```json
{"timestamp":"2026-11-20T09:14:03.3981204Z","agent":"example-app-01","event":"accepted","job_id":"01J8ZQ4M6T3N7V2K9R5W0XDHBE","identity":"ci-jenkins","action":"your.deploy","parameters":{"image_tag":"build-1842"}}
{"timestamp":"2026-11-20T09:14:03.4125533Z","agent":"example-app-01","event":"started","job_id":"01J8ZQ4M6T3N7V2K9R5W0XDHBE","identity":"ci-jenkins","action":"your.deploy"}
{"timestamp":"2026-11-20T09:14:41.9071882Z","agent":"example-app-01","event":"finished","job_id":"01J8ZQ4M6T3N7V2K9R5W0XDHBE","identity":"ci-jenkins","action":"your.deploy","result":"succeeded","duration_ms":38495}
```

What the audit trail guarantees, and what it does not

- Each write is flushed with `fsync` before the call returns, and any sink error is returned to the caller, which is what makes the accepted and started records fail closed.
- Redaction is by parameter name (names containing `token`, `secret`, `pass`, `key`, `cert` and similar). A secret carried in a parameter with an innocent name would be written verbatim.
- The local file is owned by the account that runs scripts, so that account can alter it. Tamper resistance comes from the collector, not the file.
- Failed TLS handshakes never reach this log, deliberately: an unauthenticated peer must not cause a synced write per attempt. `net/http` reports them to the structured service log at `WARN`, so failed-authentication monitoring means collecting that log or watching the network layer.
- Every reload is recorded: `reloaded` with the identities it added, removed or changed, or `reload_failed` with the reason (Windows event IDs 105 and 106).
- With `audit.max_rejections_per_minute` set, rejected requests past that cap per identity are still refused but counted, and one summary record per minute replaces their individual records. It is off by default, so every rejection is written.

## 10. Linux and Windows implementations {#platforms}

The external API, configuration schema, authentication and authorization are identical. What differs is how the host is protected and how a script is contained.

<figure class="ax-fig" id="fig-platforms" markdown>
<div class="ax-scroll">
--8<-- "platforms.svg"
</div>
<figcaption>Figure 10. Ten concerns on each platform. The red outline marks the one place where Windows has a gap that Linux does not: unsigned binaries.</figcaption>
</figure>

## 11. Security boundaries {#boundaries}

| Boundary | Enforced by | Source | What remains |
|---|---|---|---|
| Network to listener | Mutual TLS, one trust root, optional CRL over the whole chain | `auth.TLSState` | No OCSP; the rate of new connections and handshakes is not limited, only the number open |
| Identity to action | Default-deny allowlist keyed by CN, changed by a live reload without a restart | `Identity.IsAllowed`, `handleTriggerAction`, `Server.Reload` | CN issuance is the CA's job |
| Request to job manager | Schema, size, regex and parameter validation | `handlers.go`, `config.Parameter` | A script can still mishandle a validated value |
| Job visibility | Owner-only status and logs, and only while the owner is still allowed the action | `ownedJob` | Output never survives a restart; the state file is writable by the service account |
| Axiom to script | No shell, fixed invocation, constructed environment, timeout, output cap, containment | `executor`, `jobs.buildEnv` | Axiom does not constrain what a script does |
| Service account to its own files | Integrity checks at startup and reload, systemd or NTFS sandbox | `config/script_security_*`, `winsec`, `axiom.service` | Docker group access is root-equivalent; the Windows account holds `SeImpersonatePrivilege` |
| Axiom to audit | Fail-closed accepted and started, fsync, every reload recorded | `audit.Logger.Write`, `Server.ReloadFrom` | Local file alterable; finished record best effort; capped rejections keep only a count; Windows event-log entries are forgeable by the service account |
| Release to host | GPG-signed RPMs and checksums, SBOM, provenance | `release.yml`, `sign-release.sh` | Windows binaries are not Authenticode-signed |

## 12. Failure handling and limitations {#limits}

### How it fails

| Situation | Behaviour | Source |
|---|---|---|
| Bad configuration or insecure file | Refuses to start; under the Windows SCM it stops with exit code 13 | `config.Load`, `service_windows.go` |
| Audit write fails at accept | Job removed, 500, script never runs | `Manager.Trigger` |
| Audit write fails at start | Job marked failed, script never runs | `Manager.run` |
| Script exceeds its timeout | Tree terminated, job failed with the timeout reason | `executor.Run` |
| Panic in a job goroutine | Recovered, job failed, finished record written | `Manager.run` |
| Graceful stop | New triggers get 503, HTTP drains up to 30 s, running jobs are cancelled and recorded as interrupted | `serve`, `Manager.Shutdown` |
| Crash, SIGKILL or power loss | No finished record. The job's state is lost, or with `jobs.state_path` it returns cancelled as interrupted. The child is killed by Pdeathsig or the Job Object | `executor` containment, `Manager.EnableState` |
| Trigger response lost | With an `Idempotency-Key`: the retry returns the original job and starts nothing. Without one: an exclusive action answers 409, a shared action may run twice | `Manager.TriggerIdempotent`, `docs/jenkins-integration.md` |
| Certificate or identity change | Applied by a reload, without cancelling jobs; a failed or restart-only reload changes nothing, and either outcome is audited | `Server.ReloadFrom`, `docs/certificates.md` |
| Revoked client certificate | Refused with 401 from the next request, open connections included; a CRL past its `nextUpdate` refuses every client until a fresh one is reloaded | `TLSState.Verify` |
| Identity removed by a reload | Its triggers, and reads of the jobs it ran for the actions it lost, get 403 | `handleTriggerAction`, `ownedJob` |
| Flood of rejected requests | Each one is refused; with `audit.max_rejections_per_minute` set, records past the cap are counted into one summary per minute | `rejectionLimiter` |

### Former gaps, now handled

Each of these was a stated limitation before v2.2.0. Settings marked opt-in are off by default, so an upgrade changes no behaviour until they are set.

| Former gap | Now | Setting | Since |
|---|---|---|---|
| No certificate revocation | CRL file checked on every request across the whole chain, one list per issuing CA, fail-closed when stale | `security.mtls.crl_file` (opt-in) | 2.2.0; whole chain and several lists in 2.3.0 |
| A restart to change certificates or identities | Live reload on `SIGHUP` or `paramchange`: all or nothing, running jobs kept, every reload audited | always on | 2.2.0 |
| Unbounded open connections | Cap counted from accept, so stalled handshakes use slots | `agent.listen.max_connections` (opt-in) | 2.2.0 |
| A lost response could run a shared action twice | `Idempotency-Key` returns the original job and starts nothing | request header; `jobs.idempotency_window` | 2.2.0 |
| Job status lost on restart | Status and idempotency keys kept; a job in flight at a crash returns cancelled | `jobs.state_path` (opt-in) | 2.2.0 |
| Handshake failures only on unstructured stderr | Logged at `WARN` by the structured service log | always on | 2.2.0 |
| A removed identity could still read its jobs | 403 once the identity is no longer allowed the job's action | always on | 2.3.0 |
| Rejected requests could flood the audit log | Per-identity cap with a summary record per minute | `audit.max_rejections_per_minute` (opt-in) | 2.3.0 |

### What Axiom still does not do

<div class="ax-ledger">
<div><b>Fetch revocation data</b><span>No OCSP and no CRL download: Axiom makes no outbound connections. The operator publishes the CRL file and reloads before its <code>nextUpdate</code>; after that, every client is refused.<em>internal/auth/revocation.go</em></span></div>
<div><b>Limit the rate of new connections</b><span>The number of open connections can be capped, but not how fast new connections and TLS handshakes arrive, and every audited request costs a synced write. Contain with network controls and systemd resource limits.<em>cmd/axiom/limitlistener.go, docs/THREAT-MODEL.md</em></span></div>
<div><b>Tell an attacker's replay from a retry</b><span>An <code>Idempotency-Key</code> protects a client's own retries, but an attacker can omit it. A replayed request is indistinguishable from a repeated trigger; exclusive actions answer 409 while running, and shared actions should be idempotent.<em>docs/THREAT-MODEL.md, section 8</em></span></div>
<div><b>Reload actions, the listener or limits</b><span>These are read once. A reload that touches them is refused and changes nothing; applying them needs a restart, which cancels running jobs.<em>internal/config/reload.go</em></span></div>
<div><b>Keep script output</b><span>Output stays in memory and is gone after a restart, by design, since it may hold whatever a script printed. A restored job's logs answer 410; the audit log is the durable record.<em>internal/jobs/persist.go</em></span></div>
<div><b>Restrict what a script does</b><span>Scripts may use the network and any resource the service account can reach. Axiom controls how a script is invoked, not its behaviour.<em>packaging/axiom.service, RestrictAddressFamilies</em></span></div>
<div><b>Make Windows event-log entries tamper-proof</b><span>Entries carry no sender attribute a script cannot also set. Reconcile forwarded events with the local file.<em>internal/audit/eventlog_windows.go</em></span></div>
<div><b>Sign Windows binaries</b><span>The exe and MSI are not Authenticode-signed. Checksums and the Linux RPMs are GPG-signed, and releases carry build provenance and an SBOM.<em>README.md known gaps, SECURITY.md, docs/verifying-downloads.md</em></span></div>
<div><b>Claim validation it has not had</b><span>Not yet run: the event-log sink on Windows Server, the Docker and Kubernetes success path, and third-party EDR. Ubuntu and Debian work but are outside the validated matrix; on Ubuntu, move <code>audit.path</code> off the group-writable <code>/var/log</code>.<em>README.md, supported platforms</em></span></div>
</div>

Each of these is a stated boundary, not a hidden one, and each can be compensated for in the surrounding infrastructure: short-lived certificates and a published CRL, network segmentation, a central log collector, narrow allowlists and reviewed, idempotent scripts.
