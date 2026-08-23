# Testing Axiom with Postman

This is a manual/exploratory testing aid, not part of Axiom itself. It
exercises the exact same four HTTP endpoints a real caller (e.g. a Jenkins
pipeline — see [`jenkins-integration.md`](jenkins-integration.md)) would
use. Postman here is a temporary, convenient client for verifying an
agent's behavior by hand before or alongside wiring up real automation —
nothing about Axiom's API changes depending on which client calls it.

Collection file: [`postman/Axiom.postman_collection.json`](../postman/Axiom.postman_collection.json).
No certificates, private keys, credentials, or company-specific values are
included in it — every environment-specific value is a variable you fill
in yourself.

## 1. Import the collection

Postman → **Import** → select `postman/Axiom.postman_collection.json` (or
drag it into the Postman window). It appears as **"Axiom v1.0.0 API"** in
your sidebar, with three folders: Health, Actions, and Negative Tests.

## 2. Configure `{{base_url}}`

Collection variables are pre-filled with clearly fake placeholders (e.g.
`https://axiom-agent.example.internal:8443`) that resolve nowhere. Set
your own:

1. Click the collection name → **Variables** tab.
2. Set `base_url`'s **Current value** to your agent's actual
   `https://host:port` (matching `agent.listen` in that agent's
   `config.yaml`).
3. Save.

(Using a Postman **Environment** instead of editing collection variables
directly works the same way and is preferable if you test against more
than one agent — create one environment per agent and switch between
them.)

## 3. Configure Postman's client certificate for mTLS

**This is the one step Postman cannot do for you via the collection file**
— client certificates are configured at the Postman application level,
per domain, not per-request or per-collection. This is a deliberate
Postman design choice (so certificate material never ends up inside an
exportable/shareable collection JSON), and it's why this collection
doesn't and can't include one.

1. Postman → **Settings** (gear icon) → **Certificates**.
2. **Add Certificate**.
3. **Host**: the hostname (and port, if Postman's version asks for it)
   from your `base_url` — it must match exactly, or Postman won't attach
   the certificate to your requests.
4. **CRT file**: your client certificate (issued by the CA your target
   Axiom agent trusts — see [`certificates.md`](certificates.md) for how
   that material is provisioned; Axiom itself never issues it).
5. **KEY file**: the matching private key.
6. Save/enable the certificate.

From now on, every request Postman sends to that host automatically
includes the client certificate — you won't see it in the request editor,
and it isn't stored in the collection.

**CA trust note**: Postman validates the *server's* certificate against
your OS/Postman's trust store, same as a browser. If your Axiom agent's
server certificate is signed by an internal CA that isn't in your system's
trust store, Postman will show a TLS/SSL error on every request. Either
add your internal CA to your OS trust store, or (only for quick local
testing, never for anything you'd consider a real verification) disable
Postman's **SSL certificate verification** in Settings → General — turning
that off does not affect Axiom's own TLS enforcement, only whether
*Postman* double-checks the server cert before trusting it.

## 4. Trigger an action

Open **Actions → Trigger Action**. Before sending:
- Set the `action` collection variable to a name that actually exists in
  your target agent's `config.yaml` (`backend.deploy` is only an example
  from this project's docs — see [`actions.md`](actions.md)).
- Set `image_tag` (or edit the request body) to match whatever parameters
  that specific action actually declares — Axiom rejects any parameter an
  action hasn't declared, and any value that fails its declared pattern.

Send it. A successful trigger returns `202` with `{"job_id": "...",
"status": "queued"}`.

## 5. Capture the returned `job_id`

Already automatic: the Trigger Action request's **Tests** script reads the
response and, on a `202`, runs:

```js
pm.collectionVariables.set('job_id', body.job_id);
```

so the next two requests pick it up without you copying anything by hand.
To check on a job triggered some other way, set the `job_id` collection
variable manually instead.

## 6. Check job status

**Actions → Get Job Status** — `GET /v1/jobs/{{job_id}}`. Re-send this on
an interval to watch a job move through `queued` → `running` → a terminal
state (`succeeded`/`failed`/`cancelled`). Postman doesn't auto-poll; resend
manually or use Postman's Runner if you want repeated automatic checks.

## 7. Retrieve logs

**Actions → Get Job Logs** — `GET /v1/jobs/{{job_id}}/logs`. Captured,
size-bounded `stdout`/`stderr` for that job. Most useful once the job has
reached a terminal state, especially `failed`.

## 8. Test authorization failures

**Negative Tests → Unauthorized Action** sends a real, valid, CA-trusted
client certificate for an action your identity is deliberately *not*
allowlisted for (`{{unauthorized_action}}` — set it to a real action name
from your config that your identity doesn't have). Expect `403`. This is
the concrete way to confirm Axiom's default-deny authorization is
actually enforced on your specific config, not just documented.

**Negative Tests → Nonexistent Action** expects `404` — proving there's no
fallback/wildcard action, only exactly what's declared.

**Negative Tests → Unauthenticated Request** expects `401`, but needs one
manual extra step because of the Postman mTLS limitation in step 3: Postman
attaches your client certificate to *every* request to that host, so there
is no built-in way to send "this one request without a certificate."
To actually run this test: temporarily remove or disable the certificate
for your host in Settings → Certificates, send the request, confirm
`401`, then **re-add the certificate** before continuing with the rest of
the collection.

## 9. Test invalid parameters

**Negative Tests → Missing Required Parameter** sends an empty
`parameters` object against an action that declares a required one —
expect `400`. **Negative Tests → Invalid Parameter** sends a value that
violates the example `image_tag` pattern (a plain string with a space in
it — not an injection payload; Axiom never passes parameter values
through a shell in the first place, so there's nothing to demonstrate by
attempting one) — also expect `400`.

## What this collection deliberately doesn't do

No arbitrary-command examples (there's no such endpoint to call), no
automation framework beyond simple `pm.test()` assertions and one
variable-extraction script, and no certificate material anywhere in the
file. If you outgrow manual Postman testing, the next step is real CI
integration — see [`jenkins-integration.md`](jenkins-integration.md), which
documents the identical HTTP flow this collection exercises by hand.
