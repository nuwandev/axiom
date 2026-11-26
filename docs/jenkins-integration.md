# CI/CD Integration (Jenkins example)

This guide explains how a CI/CD system — Jenkins is used as the concrete
example, but the same flow applies to any system that can make an HTTPS
request with a client certificate — triggers and monitors Axiom actions.

There is no Jenkins plugin or shared library yet (deliberately — see
[Status](#status) below). This is the raw HTTP flow; wrap it in whatever
your pipeline tooling makes convenient.

## Basic flow

```text
Jenkins
  │
  │ POST /v1/actions/{action}
  ▼
Axiom  ── returns job_id immediately, action runs in the background
  │
  │ GET /v1/jobs/{job_id}   (poll until status is terminal)
  ▼
Axiom  ── {status: succeeded|failed|cancelled, exit_code, ...}
  │
  │ (on failure) GET /v1/jobs/{job_id}/logs
  ▼
Jenkins marks the build SUCCESS or FAILURE accordingly
```

## Authentication

Jenkins authenticates with a client certificate issued by the CA your
Axiom agent trusts (`security.mtls.ca_file` — see
[`certificates.md`](certificates.md) for how that certificate material is
provisioned and rotated; Axiom itself has no role in issuing it). The
certificate's Common Name becomes Jenkins's identity, which Axiom checks
against that identity's allowed-actions list in `authorization.identities`
(see [`configuration.md`](configuration.md)) — default-deny, so an
identity or action not explicitly listed is rejected regardless of how
valid the certificate itself is.

Never commit real certificate paths, hostnames, or credentials into a
pipeline definition checked into source control — reference them via your
CI system's credential store (e.g. Jenkins Credentials) the same way you
would any other secret material.

## Example: trigger, poll, fail on error, fetch logs on failure

```groovy
pipeline {
    agent any

    environment {
        AXIOM_URL = 'https://axiom-agent.internal.example:8443'
        // Reference certificate material from your credential store —
        // never hardcode paths/contents in the pipeline definition.
        AXIOM_CA   = credentials('axiom-ca-cert')
        AXIOM_CERT = credentials('axiom-client-cert')
        AXIOM_KEY  = credentials('axiom-client-key')
    }

    stages {
        stage('Trigger action') {
            steps {
                script {
                    def triggerBody = '{"parameters":{"image_tag":"' + env.BUILD_TAG + '"}}'
                    def response = sh(
                        script: """
                            curl -sS --fail \
                              --cacert "$AXIOM_CA" --cert "$AXIOM_CERT" --key "$AXIOM_KEY" \
                              -X POST "${AXIOM_URL}/v1/actions/your.deploy" \
                              -H 'Content-Type: application/json' \
                              -d '${triggerBody}'
                        """,
                        returnStdout: true
                    ).trim()

                    def job = readJSON text: response
                    def jobId = job.job_id
                    echo "Axiom job: ${jobId}"

                    // Poll until terminal. See "Polling interval" below for
                    // why this uses a fixed interval, not a tight loop.
                    def status = 'queued'
                    def maxAttempts = 60   // e.g. 60 * 10s = 10 minutes; size
                                            // this to comfortably exceed the
                                            // action's own configured timeout
                    for (int i = 0; i < maxAttempts; i++) {
                        sleep(time: 10, unit: 'SECONDS')
                        def statusResponse = sh(
                            script: """
                                curl -sS --fail \
                                  --cacert "$AXIOM_CA" --cert "$AXIOM_CERT" --key "$AXIOM_KEY" \
                                  "${AXIOM_URL}/v1/jobs/${jobId}"
                            """,
                            returnStdout: true
                        ).trim()
                        def jobStatus = readJSON text: statusResponse
                        status = jobStatus.status
                        if (status in ['succeeded', 'failed', 'cancelled']) {
                            if (status != 'succeeded') {
                                // Fetch logs on failure so the failure is
                                // visible without a separate manual step.
                                def logs = sh(
                                    script: """
                                        curl -sS --fail \
                                          --cacert "$AXIOM_CA" --cert "$AXIOM_CERT" --key "$AXIOM_KEY" \
                                          "${AXIOM_URL}/v1/jobs/${jobId}/logs"
                                    """,
                                    returnStdout: true
                                ).trim()
                                echo "Axiom job ${jobId} logs:\n${logs}"
                                error("Axiom action failed: status=${status}, reason=${jobStatus.error ?: 'see logs above'}")
                            }
                            break
                        }
                    }
                    if (!(status in ['succeeded', 'failed', 'cancelled'])) {
                        error("Axiom job ${jobId} did not reach a terminal state in time — check its status manually")
                    }
                }
            }
        }
    }
}
```

## Further examples

The same trigger/poll/fail pattern applies to any configured action —
only the action name and parameters change.

**Frontend deploy:**
```bash
curl -sS --fail --cacert "$AXIOM_CA" --cert "$AXIOM_CERT" --key "$AXIOM_KEY" \
  -X POST "${AXIOM_URL}/v1/actions/frontend.deploy" \
  -H 'Content-Type: application/json' \
  -d '{"parameters":{"image_tag":"'"${BUILD_TAG}"'"}}'
```

**Deploy everything an identity is allowed to** (if your config defines
such an action — Axiom has no built-in "deploy all," this is just another
named action like any other, whose script happens to orchestrate several
components):
```bash
curl -sS --fail --cacert "$AXIOM_CA" --cert "$AXIOM_CERT" --key "$AXIOM_KEY" \
  -X POST "${AXIOM_URL}/v1/actions/all.deploy" \
  -H 'Content-Type: application/json' \
  -d '{"parameters":{"image_tag":"'"${BUILD_TAG}"'"}}'
```

**Rollback with an explicit `image_tag`** (see
[Rollback and duplicate deployments](#rollback-should-always-pass-an-explicit-image_tag)
below for why this must be explicit, not "roll back to previous"):
```bash
curl -sS --fail --cacert "$AXIOM_CA" --cert "$AXIOM_CERT" --key "$AXIOM_KEY" \
  -X POST "${AXIOM_URL}/v1/actions/your.rollback" \
  -H 'Content-Type: application/json' \
  -d '{"parameters":{"image_tag":"'"${PREVIOUS_GOOD_TAG}"'"}}'
```

## Operational guidance

### Request timeout vs. action timeout

These are two different, independently-configured timeouts and it's worth
not confusing them:

- **The action's own timeout** (`actions.<name>.timeout` in Axiom's
  config) bounds how long the *action script* may run before Axiom kills
  it and marks the job `failed`. This is server-side and Jenkins has no
  control over it.
- **Jenkins's own HTTP client timeout** on the `POST`/`GET` calls only
  needs to cover a single fast round-trip — `POST /v1/actions/{action}`
  returns as soon as the job is accepted (typically milliseconds), and
  each `GET /v1/jobs/{job_id}` poll is a cheap status lookup. Neither
  request blocks for the duration of the action itself. A short client
  timeout (a few seconds) on each individual call is appropriate; the
  *overall* pipeline stage's own timeout should instead comfortably
  exceed the action's configured `timeout` (see the `maxAttempts` sizing
  note in the example above).

### Polling interval

Poll on a fixed interval (the example above uses 10 seconds) rather than
tight-looping — `GET /v1/jobs/{job_id}` is cheap but there's no reason to
hammer it faster than a human-meaningful status update needs. There is no
webhook/callback mechanism and none is planned (see the project's
[README](../README.md#roadmap)) — polling is the supported pattern.

### Handling HTTP errors

- `400` — a parameter Axiom rejected (undeclared, or failed its pattern).
  Fix the request; retrying unchanged won't help.
- `403` — this identity isn't allowlisted for this action. A config
  problem on Axiom's side, not something to retry.
- `404` on trigger — the action name doesn't exist in that agent's config.
  `404` on job lookup — the job ID is wrong, the job belongs to a different
  client identity (status and logs are readable only by the certificate that
  triggered the job, so poll with the same credential), or Axiom has
  restarted since the job ran and `jobs.state_path` is not set (see [job history and restarts](#job_id-and-what-a-lost-connection-means) below).
  With `jobs.state_path` set, a job from before a restart still answers, but
  its `/logs` return `410` (output is never kept), and a job that was running
  at the restart is `cancelled` with an "interrupted: agent restarted" error.
- `429` — the agent-wide limit of running jobs (`jobs.max_concurrent`) is
  reached and no job was created. Wait for `Retry-After` and retry; unlike a
  lost trigger response, this is safe to retry.
- `409` — an `exclusive`-concurrency action is already running. Depending
  on your pipeline's needs, either fail the build (simplest) or wait and
  retry after a delay — Axiom does not queue the request for you.
- `5xx` / connection failure on the *trigger* request — the job may or may
  not have been accepted; see the next section before blindly retrying.

### `job_id` and what a lost connection means

**Use the returned `job_id`.** It's the only way to check on a
previously-triggered action, and once Axiom has returned it, **the action
continues running even if Jenkins loses connectivity, its own connection
times out, or the pipeline is aborted.** Axiom has no idea Jenkins went
away — it just runs the job to completion (or timeout) and records the
result. Design your pipeline around this: on any doubt about whether a
trigger request actually landed, check the audit trail or (if you kept a
`job_id`) poll it, rather than assuming failure and re-triggering blind.

**Send an `Idempotency-Key` header on every trigger you might retry.** If
the response is lost (connection dropped after Axiom accepted the request but
before Jenkins got the `202`), repeat the request with the same key. Axiom then
returns the job the first request started (`202`, the same `job_id`, the
current `status`, and an `Idempotent-Replay: true` header) and starts nothing.
If the first request never arrived, the retry simply starts the job.

```bash
curl --cacert ca.crt --cert client.crt --key client.key \
  -H 'Idempotency-Key: deploy-build-1423' \
  -H 'Content-Type: application/json' \
  -d '{"parameters":{"image_tag":"v1.4.2"}}' \
  https://<host>:8443/v1/actions/your.deploy
```

Rules to design the pipeline around:
- Use a key that is stable across retries of one logical trigger and different
  for the next one, for example the build number. It is 1 to 128 characters
  from `A-Z a-z 0-9 . _ : -`; anything else is a `400`.
- A key belongs to your identity and one action. Reusing it with different
  `parameters` is a `422`, never a silent return of the old job.
- A request that is refused (`409` busy, `429`, `400` invalid parameter) does
  not use up the key; retry it with the same key.
- A replay is not charged to your trigger rate limit or the job cap.
- Keys are remembered for `jobs.idempotency_window` (default 10 minutes). By
  default they are in memory only, so after an Axiom restart the same key starts
  a new job; with `jobs.state_path` set they survive a restart. After the window
  the same key always starts a new job. If the job's record has aged out of the job history while
  the key is still valid, the retry is a `409` that names the job, not a new
  run.

Without a key the old rules hold, because Axiom cannot tell a retry from a
second trigger:
- if the action is `concurrency: exclusive`, a blind retry gets a clean
  `409` if the first one actually did start — that's your signal;
- if it's `concurrency: shared`, a blind retry could genuinely run twice.
  Either write the action idempotently (check current deployed state
  before acting — the same guidance as the project's health-check
  recommendation, see [`actions.md`](actions.md)), or check the audit log
  / a recent job status before retrying rather than retrying
  automatically.

### Avoiding duplicate deployments

Two independent, complementary controls:
- **`concurrency: exclusive`** on the action itself rejects a second
  trigger (`409`) while one is already running — set this on any action
  where two overlapping runs would be unsafe (most deploys/rollbacks).
- **Pipeline discipline**: don't fire a deploy from multiple concurrent
  Jenkins builds for the same target unless you've deliberately reasoned
  through what should happen (usually: don't — serialize at the pipeline
  level too, e.g. Jenkins's own `disableConcurrentBuilds()`).

### Rollback should always pass an explicit `image_tag`

Axiom keeps no "previous deployment" state — there's no `rollback` action
that means "undo the last deploy" implicitly, because Axiom doesn't track
deploy history to undo. Your pipeline should record the tag it deployed
(e.g. as a build artifact or a value looked up from your own deployment
records) and pass that specific tag back explicitly when rolling back —
exactly as shown in the example above. This keeps rollback behavior
predictable and auditable: the audit log shows exactly which tag was
requested, not an inference Axiom made on your behalf.

### Surfacing logs on failure

Fetch `GET /v1/jobs/{job_id}/logs` as soon as a job's terminal status is
`failed` (or `cancelled`, if that's unexpected) and echo it into the
Jenkins build log — see the example above. Don't fetch logs on every poll;
they're static once a job is running and only meaningfully change once
it's finished, so fetching them only on failure (or once, after success,
if you want a record) avoids unnecessary requests.

## Status

This is intentionally just an HTTP-flow guide, not a packaged integration.
A thin Jenkins shared library that wraps this pattern (handling
certificate paths, JSON parsing, and polling once, instead of per
pipeline) is a natural next step once this raw flow has seen real use —
see the [README roadmap](../README.md#roadmap). It is not built yet, and
no new Axiom API endpoints are needed to build it — everything above is
implementable entirely from the existing four endpoints.
