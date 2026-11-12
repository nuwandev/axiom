# Certificate Lifecycle

Axiom is not a certificate authority and does not manage certificate
issuance, renewal, or revocation. That is a deliberate boundary, not a
missing feature:

- Axiom **loads** its server certificate, private key, and trusted-CA
  bundle from the paths in `security.mtls.*`.
- Axiom **verifies** client certificates against that CA on every request.
- Axiom **fails clearly** at startup if any of the three files is missing,
  unreadable, not valid PEM/X.509, or sits somewhere an untrusted local
  user could tamper with it (see [`configuration.md`](configuration.md)).

Issuing, renewing, rotating, and revoking certificates is your
organization's CA/PKI process or deployment tooling's job, external to
Axiom. This keeps Axiom's trust boundary simple and auditable: it trusts
exactly the CA bundle you give it, nothing more, and has no code path that
could itself become a target for certificate-forging attacks.

## Restart required after renewal

**Yes.** Axiom reads `ca_file`, `cert_file`, and `key_file` once, at
process startup (`tls.LoadX509KeyPair` plus building the CA pool into the
listener's `tls.Config`). There is no file-watcher, no `SIGHUP` handler,
no background renewal worker, and none is planned — that complexity
belongs in a dedicated certificate-management system if you need one, not
folded into an action-execution agent. A certificate renewal is not
"live" until Axiom restarts and re-reads the files.

This is an accepted, deliberate trade-off for the current design: it keeps
the TLS setup path small and easy to reason about, at the cost of a
required restart on every rotation. If your CA's certificate lifetime is
short enough that this becomes operationally painful, that's a signal to
automate the restart step (below) in your renewal tooling, not to add
hot-reload to Axiom.

## Safe generic renewal flow

This is deliberately generic — plug in whatever PKI tooling your
organization actually uses in place of `<your-pki-tool>`.

1. **Obtain the renewed certificate/key through your external PKI
   tooling.** Axiom has no role here — this step happens entirely outside
   Axiom, e.g.:
   ```bash
   <your-pki-tool> renew --cn axiom-server-01 \
     --out /tmp/renewed/server.crt \
     --key-out /tmp/renewed/server.key
   ```

2. **Validate the new material before touching anything live:**
   ```bash
   openssl x509 -in /tmp/renewed/server.crt -noout -checkend 0
   openssl verify -CAfile /etc/axiom/certs/ca.crt /tmp/renewed/server.crt
   openssl x509 -noout -modulus -in /tmp/renewed/server.crt | openssl md5
   openssl rsa   -noout -modulus -in /tmp/renewed/server.key  | openssl md5
   # (use `openssl ec -noout -pubin ...` / pubkey comparison instead of
   #  rsa -modulus if your key is EC, matching whatever algorithm you use)
   ```
   Confirm the two modulus/pubkey hashes match (cert and key are a real
   pair) and the cert isn't already expired.

3. **Atomically replace the files** (a plain `cp` over a file Axiom might
   be reading mid-request risks a torn read; write to a temp file on the
   same filesystem and `mv` it into place, which is atomic on POSIX
   filesystems):
   ```bash
   install -o root -g axiom -m 0640 /tmp/renewed/server.crt /etc/axiom/certs/server.crt.new
   install -o root -g axiom -m 0640 /tmp/renewed/server.key /etc/axiom/certs/server.key.new
   mv /etc/axiom/certs/server.crt.new /etc/axiom/certs/server.crt
   mv /etc/axiom/certs/server.key.new /etc/axiom/certs/server.key
   ```
   (Renewing the CA bundle itself, e.g. for a scheduled root rotation,
   follows the same atomic-replace pattern against `ca.crt` — plan that
   kind of rotation carefully, since it affects every client's trust
   relationship, not just this one server certificate.)

4. **Restart Axiom** so it re-reads the files (see [restart required
   above](#restart-required-after-renewal) — there's no reload signal):
   ```bash
   sudo systemctl restart axiom
   ```

5. **Verify `/health`:**
   ```bash
   curl --cacert ca.crt --cert client.crt --key client.key \
     https://<host>:<port>/health
   # {"status":"ok",...}
   ```

6. **Verify an authenticated mTLS request actually works end-to-end**
   (not just that the process started — confirm the new cert is actually
   being served and accepted):
   ```bash
   curl --cacert ca.crt --cert client.crt --key client.key \
     -X POST https://<host>:<port>/v1/actions/<a-harmless-action>
   ```

If step 5 or 6 fails, the previous binary/config/certs are still on disk
(this flow never deletes anything) — restore the prior `server.crt`/
`server.key` and restart again while you investigate.

## What Axiom validates automatically

At every startup, independent of the manual flow above: `ca_file` is
checked for valid PEM certificate data; `cert_file` is checked for valid
PEM/X.509; `key_file` is checked for valid PEM data; all three (and every
directory above them) are checked for correct ownership and that no
untrusted local user could have modified them. Axiom refuses to start
otherwise, with a specific error naming the problem — see
[`INSTALL.md` §12](INSTALL.md#12-startup-validation-reference).

## If a client certificate is compromised

Axiom does **not** check certificate revocation (no CRL or OCSP). A stolen
client certificate whose CA-signed chain is still valid keeps working for its
identity until it expires or that identity is removed from the config. So:

1. **Disable the identity immediately.** Remove its entry under
   `authorization.identities` (or just the actions it must not run) in
   `/etc/axiom/config.yaml`, then `systemctl restart axiom` (Axiom has no hot
   reload; `Restart-Service axiom` on Windows). A restart cancels running jobs
   cleanly and drops all in-memory job state. Until the restart, the old
   authorization stays in force.
2. **Stop the CA issuing that identity again**, or issue the replacement under
   a different Common Name. Authorization is by exact Common Name, so a new
   certificate for the same CN from the same CA is accepted as the same
   identity. Rotating the CA (new `ca.crt`, restart) invalidates every client
   certificate signed by the old one.
3. **Review the audit trail** (the central collector, not just the local
   file) for what that identity ran: `accepted`/`started`/`finished` records
   carry the identity, action and declared parameters.
4. Revisit lifetimes. Short-lived client certificates (days, not years) are
   the only expiry-based mitigation available, and one certificate per
   pipeline narrows what a single theft can run.

## CA trust assumptions

- Every certificate in `security.mtls.ca_file`, including an intermediate,
  is a trust anchor; Axiom applies no name constraints or path-length policy
  of its own. Put only the CA(s) you control and trust there.
- Identity is the client certificate's Common Name, matched exactly against
  `authorization.identities`. The CA must therefore never issue a certificate
  whose Common Name is chosen by the requester; otherwise any holder of a
  valid enrollment can claim an authorized identity. SANs are ignored.
- Client certificates need the `clientAuth` extended key usage; verification
  uses the current time, so expired and not-yet-valid certificates are
  refused on every request.
- The server private key is read from `security.mtls.key_file`. Axiom refuses
  to start if it is accessible by other users (mode wider than `0640`),
  writable by group or others, symlinked, or owned by anyone but root (set
  `root:axiom 0640`, as the installer layout does).
- `security.health.allow_anonymous` is `false` by default. If enabled, the
  TLS layer accepts any peer and only `/health` is served without a verified
  certificate; requests from an unauthenticated peer to any other route are
  rejected and each one writes an audit record, so leave it off unless a
  monitor truly cannot present a certificate.
