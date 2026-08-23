# Actions

This is the core design decision behind Axiom, so it's worth stating
plainly and repeating: **actions are server-defined, not client-supplied.**

## Actions are explicit, named, and server-defined

Every action Axiom can run is declared ahead of time in that server's own
`config.yaml` — a name, a script path, a timeout, a concurrency policy, and
a parameter schema. A client can only ask Axiom to run one of those
pre-declared names. There is no way to make a request that specifies *what*
to run, only *which already-configured thing* to run.

## Commands are local scripts, not inline logic

`command:` is always a path to a script file on the server, never inline
shell text in the YAML. Axiom invokes that script directly — never through
`/bin/sh -c` or any shell — so shell metacharacters in arguments or
parameter values have no special meaning to Axiom itself. The script
contains whatever operational logic your deployment actually needs; Axiom
has zero knowledge of Docker, Kubernetes, systemd, or anything else that
might be inside it.

## There is no arbitrary-command endpoint

There is no `POST /v1/execute` or equivalent, and there will not be one —
this is a structural property of the API, not a policy switch. See the
[README](../README.md#why-predefined-actions-not-arbitrary-commands) for
why.

## Parameters are declared and validated, never passed through raw

If an action needs input from the caller (an image tag, a version string),
declare exactly that parameter, its type, and a regex pattern it must
match:

```yaml
actions:
  backend.rollback:
    command: /opt/axiom/actions/backend-rollback.sh
    timeout: 10m
    parameters:
      image_tag:
        type: string
        pattern: '^[a-zA-Z0-9._-]{1,128}$'
        required: true
```

A request parameter not declared here is rejected (`400`) before the
script ever runs. A declared parameter whose value doesn't match its
pattern is also rejected. Only parameters that pass both checks reach the
script — as an environment variable named `AXIOM_PARAM_<NAME>` (upper-cased),
never concatenated into a command line. `AXIOM_JOB_ID` and `AXIOM_ACTION`
are always set too.

```bash
#!/usr/bin/env bash
set -euo pipefail
: "${AXIOM_PARAM_IMAGE_TAG:?required}"
echo "deploying image tag: $AXIOM_PARAM_IMAGE_TAG"
```

A value like `; rm -rf /` either fails its declared pattern (rejected
outright) or, if a parameter genuinely has no pattern, arrives as an inert
string in the environment — it can only become dangerous if the script
itself does something unsafe with it (e.g. `eval`s it). Always declare a
pattern for anything that reaches a script.

## Scripts must be protected from local modification

Axiom enforces this at config load time, not just as a deployment
recommendation:

- the script path must be absolute and clean (no `.`/`..` segments),
- it must not be a symlink,
- it must exist, be a regular file, and be executable,
- it (and every directory above it, all the way to `/`) must be owned by
  root or the Axiom service account, and must not be group- or
  world-writable.

Axiom **refuses to start** if any configured action's script fails this
check — this is deliberately a hard failure, not a warning, because a
script an untrusted local user can modify is equivalent to giving that
user everything the script's execution context can do.

Install scripts accordingly:

```bash
sudo install -o root -g axiom -m 0750 my-action.sh /opt/axiom/actions/
```

## Real health checks, not just exit codes

A deploy script should verify the deployed service is actually healthy
before reporting success — "the launch command exited 0" is not the same
as "the new version is serving traffic." See
[`scripts/examples/`](../scripts/examples/) for a worked example that
pulls, launches, and polls a real health check before exiting `0`.

## Concurrency

Declare `concurrency: exclusive` for actions where two overlapping runs
would be unsafe (most deploys/rollbacks); leave the default `shared` for
actions safe to run concurrently (e.g. a read-only status check). An
exclusive action's second trigger while one is already running gets `409`
immediately — Axiom does not queue it.
