# Axiom — RHEL Installation & Operations Guide

This guide is generic by design: no company-specific hosts, IPs, credentials,
or private infrastructure details. Substitute your own values wherever a
placeholder like `<your-ca>` or `example-ci` appears.

Target platform: RHEL-family systems with systemd (RHEL, Rocky Linux,
AlmaLinux, CentOS Stream).

**Tested against:** Rocky Linux 9.8 ("Blue Onyx"), a 1:1 RHEL9-compatible
rebuild, real systemd as PID 1, real cgroups v2. Full end-to-end validation
(install → certs → config → start → mTLS → job execution → concurrency →
crash/restart → upgrade/rollback → uninstall) was run against this exact
target; see `docs/THREAT-MODEL.md` for the security-focused findings from
that pass. The one thing that environment could not exercise is SELinux
*enforcing* mode — see §9a for what was and wasn't verified there and what
you should check on your own enforcing host before going live.

---

## 1. Prerequisites

- A RHEL-family host with systemd.
- Root (or sudo) access on that host, for the one-time install.
- The `axiom` binary for linux/amd64 (or linux/arm64), built from this
  repository (`go build ./cmd/axiom`) or obtained from your internal build
  pipeline.
- An internal CA and the ability to issue:
  - one server certificate for this Axiom agent (Subject/SAN matching how
    clients will reach it),
  - one client certificate per CI system/identity that will call it
    (the certificate's Common Name is the identity Axiom authorizes against).
- The action scripts this agent will run, already written and tested
  independently of Axiom (Axiom only executes them; it has no knowledge of
  what they do).

Axiom itself has no other runtime dependencies (single static Go binary).

---

## 2. Filesystem layout

```
/usr/local/bin/axiom          binary                         root:root   0755
/etc/axiom/                   config directory               root:axiom  0750
/etc/axiom/config.yaml         agent config                   root:axiom  0640
/etc/axiom/certs/              mTLS material                  root:axiom  0750
/etc/axiom/certs/ca.crt         internal CA cert               root:axiom  0640
/etc/axiom/certs/server.crt     this agent's certificate       root:axiom  0640
/etc/axiom/certs/server.key     this agent's private key       root:axiom  0640
/opt/axiom/actions/            action scripts                 root:axiom  0750
/opt/axiom/actions/*.sh         individual scripts             root:axiom  0750 (0o750, not group/world-writable)
/var/log/axiom/                audit log directory             axiom:axiom 0750
/var/log/axiom/audit.log        audit log                       axiom:axiom 0640
/var/lib/axiom/                $HOME for the axiom account      axiom:axiom 0750
```

Design intent — **the axiom service account cannot modify its own binary,
configuration, certificates, or action scripts.** Everything under
`/etc/axiom` and `/opt/axiom` is root-owned and not group/world-writable;
Axiom starts up refusing to run if that isn't true (see §7). The only paths
the service account can write to are `/var/log/axiom` (the audit log) and
`/var/lib/axiom` (see below). This is enforced both by file
ownership/permissions and, at runtime, by the systemd sandbox
(`ProtectSystem=strict` + an explicit `ReadWritePaths` — see
[`packaging/axiom.service`](../packaging/axiom.service)).

`/var/lib/axiom` is set as the `axiom` account's `$HOME` in the systemd
unit (`Environment=HOME=/var/lib/axiom`). The account has no real home
directory (`useradd --no-create-home`) and `ProtectHome=true` hides
`/home` entirely, so without this, `$HOME` would be unset/unresolvable —
found during real-host validation to break tools an action script may
reasonably invoke regardless of container engine choice (e.g. the `docker`
CLI's `~/.docker/config.json` for private registry credentials, or a
rootless container engine's runtime-state resolution). Axiom's own job
history is still in-memory only and does not use this directory (see §11)
— it exists purely as a stable, writable `$HOME` for child processes.

---

## 3. Service account

Handled by [`scripts/install.sh`](../scripts/install.sh), or manually:

```bash
groupadd --system axiom
useradd --system --no-create-home --shell /usr/sbin/nologin --gid axiom \
  --comment "Axiom automation agent" axiom
```

No login shell, no home directory, no password. If a specific action script
needs group membership to reach a resource (e.g. the `docker` group for
`/var/run/docker.sock`), add the `axiom` user to that group explicitly and
document why — don't broaden it "just in case."

---

## 4. Running the installer

```bash
# from a checkout of this repository, after `go build ./cmd/axiom`
sudo BIN_SRC=./axiom ./scripts/install.sh
```

The script is idempotent — safe to re-run. It creates the service account,
the filesystem layout in §2, installs the binary and systemd unit, and
**stops there**. It deliberately does not generate certificates, does not
write a config file if one already exists, and does not start the service.

---

## 5. Certificates

Axiom never generates, downloads, or embeds certificate/key material —
that is entirely your PKI process. Place the three files at:

```
/etc/axiom/certs/ca.crt       # your internal CA's certificate
/etc/axiom/certs/server.crt   # this agent's certificate, signed by that CA
/etc/axiom/certs/server.key   # this agent's private key
```

Then lock down ownership and permissions (the installer creates the
directory with the right owner; you still need to set file-level modes when
you copy files in):

```bash
sudo chown root:axiom /etc/axiom/certs/*.crt /etc/axiom/certs/*.key
sudo chmod 0640 /etc/axiom/certs/ca.crt /etc/axiom/certs/server.crt /etc/axiom/certs/server.key
```

Axiom validates all three at startup (existence, valid PEM, valid X.509 for
the certs) and refuses to start otherwise.

**On `server.key`'s mode: 0640, not something tighter like 0600.** The
`axiom` service account is a *group* member of `axiom`, not the file's
owner (everything under `/etc/axiom` stays root-owned so the service
account can never modify it — see §2). `0600` leaves zero permission bits
for the group, so the running `axiom` process itself could not read its
own private key and would fail to start with a permission error. `0640`
(owner `root` read/write, group `axiom` read-only, no world access) is the
correct minimum here, not a relaxation — group-read is what lets Axiom
actually use the key at all, while the world having no access and the
`axiom` account having no write access are both still fully enforced.

Issue one client certificate per calling identity (e.g. per Jenkins
controller/credential), with a Common Name you'll reference in
`authorization.identities` in the config (§6). Client certificate
provisioning and rotation is your PKI process's responsibility, same as the
server certificate.

---

## 6. Configuration

Start from [`configs/example.yaml`](../configs/example.yaml), copy to
`/etc/axiom/config.yaml`, and edit for your environment: agent identity,
listener address/port, the action set this specific agent exposes, and
which client identities may call which actions.

```bash
sudo install -o root -g axiom -m 0640 configs/example.yaml /etc/axiom/config.yaml
sudo vi /etc/axiom/config.yaml
```

Key fields, see the example file for the full annotated reference:

- `agent.id` / `agent.name` — this agent's identity, surfaced on `/health`
  and in every audit record.
- `security.mtls.*` — the three certificate paths from §5.
- `security.health.allow_anonymous` — off by default; only set `true` if a
  network health check literally cannot present a client certificate.
- `audit.path` — defaults to `/var/log/axiom/audit.log`.
- `output.max_bytes` — per-stream captured-output cap, default 2 MiB.
- `jobs.max_history` — in-memory job count retained for polling, default
  1000 (see §11 — this is not durable storage).
- `actions.<name>` — one entry per action this agent exposes: `command`
  (absolute path to a script under `/opt/axiom/actions`), `timeout`,
  `concurrency` (`shared` or `exclusive`), and a `parameters` schema.
- `authorization.identities.<cert-common-name>.actions` — the allowlist for
  that identity. No entry means no access (default-deny).

Axiom validates the entire file at startup — structure, every file
reference, every permission requirement, every cross-reference between
`authorization` and `actions` — and refuses to start on any single problem
rather than starting in a partially-valid state. See §12 for the full list
of what's checked.

---

## 7. Action scripts

Place each script referenced by `actions.<name>.command` under
`/opt/axiom/actions/`, then:

```bash
sudo chown root:axiom /opt/axiom/actions/your-script.sh
sudo chmod 0750 /opt/axiom/actions/your-script.sh
```

Axiom checks, at startup, for every configured action:

- the path is absolute and contains no `.`/`..` segments,
- the file exists, is a regular file, and is executable,
- the file and every containing directory up to `/` is owned by root (or by
  whatever account Axiom itself runs as) and is not group/world-writable
  (except where protected by the sticky bit, e.g. `/tmp` — not a location
  you should use for action scripts anyway),
- (implicitly, via the same walk) no ancestor directory lets an untrusted
  local user delete-and-replace the script out from under those file-level
  checks.

Any violation aborts startup with a specific error naming the offending
path — this is intentionally a hard failure, not a warning.

Scripts receive their declared, validated parameters as environment
variables named `AXIOM_PARAM_<NAME>` (upper-cased), plus `AXIOM_JOB_ID` and
`AXIOM_ACTION`. See [`scripts/examples/backend-deploy.sh.sample`](../scripts/examples/backend-deploy.sh.sample)
for a complete worked example, including doing real health-check validation
rather than just trusting the launch command's exit code.

---

## 8. Permissions summary

| Path | Owner | Mode | Writable by axiom? |
|---|---|---|---|
| `/usr/local/bin/axiom` | root:root | 0755 | No |
| `/etc/axiom/` | root:axiom | 0750 | No |
| `/etc/axiom/config.yaml` | root:axiom | 0640 | No |
| `/etc/axiom/certs/` | root:axiom | 0750 | No |
| `/etc/axiom/certs/server.key` | root:axiom | 0640 | No |
| `/opt/axiom/actions/` | root:axiom | 0750 | No |
| `/var/log/axiom/` | axiom:axiom | 0750 | Yes |
| `/var/lib/axiom/` | axiom:axiom | 0750 | Yes ($HOME for child tools) |
| `/etc/systemd/system/axiom.service` | root:root | 0644 | No |

---

## 9. systemd installation and hardening

The installer places [`packaging/axiom.service`](../packaging/axiom.service)
at `/etc/systemd/system/axiom.service` and runs `systemctl daemon-reload`.
The unit runs Axiom as the unprivileged `axiom` account inside a systemd
sandbox (`ProtectSystem=strict`, no capabilities, no new privileges,
address-family/namespace restrictions, etc.) — see the unit file itself for
the full list with rationale for each setting, including the one hardening
option (`MemoryDenyWriteExecute`) deliberately left disabled by default
because it can be incompatible with certain interpreters an action script
might invoke, with instructions there for enabling it once your specific
action scripts are known to be compatible.

**Also set by the unit:** `Environment=HOME=/var/lib/axiom` and
`/var/lib/axiom` in `ReadWritePaths`. Found during real-host validation:
the `axiom` account has no real home directory
(`useradd --no-create-home`) and `ProtectHome=true` hides `/home`, so
`$HOME` is otherwise unset — which breaks tools an action script may
reasonably invoke regardless of container engine choice (e.g. the `docker`
CLI's `~/.docker/config.json` for registry auth, or a rootless container
engine's runtime-state resolution). Two things to know if you hit this
yourself: (1) some `useradd` defaults still populate the passwd
home-directory *field* as `/home/<user>` even with `--no-create-home` —
pass `--home-dir /var/lib/axiom` explicitly (the installer does this) so
the field itself is correct, since some tools resolve home via the user
database rather than `$HOME`; (2) if you `usermod` an *existing* account's
home directory instead of setting it at creation time, verify with
`getent passwd axiom` that it actually took effect — it did not in initial
testing under `usermod -d` and required a fresh `useradd --home-dir` (or a
direct fix of the passwd entry) to take effect.

---

## 9a. SELinux

RHEL-family systems normally run SELinux in `Enforcing` mode with the
`targeted` policy. **Do not disable it** to make Axiom work — none of the
validation done for this guide found a reason to.

What was verified: using the real, shipped RHEL9 policy database
(`selinux-policy-targeted`) against the actual installed layout,
`matchpathcon` resolves every path Axiom touches to a generic,
already-permissive type — no specialized or restrictive context is
implicated:

| Path | Resolved SELinux type |
|---|---|
| `/usr/local/bin/axiom` | `bin_t` |
| `/etc/axiom/config.yaml`, `/etc/axiom/certs/*` | `etc_t` |
| `/opt/axiom/actions/*.sh` | `usr_t` |
| `/var/log/axiom/audit.log` | `var_log_t` |

These are all standard path-based defaults with no per-service policy
module involved. A custom systemd service with no dedicated SELinux policy
runs under the default `unconfined_service_t` domain, which has broad,
unrestricted access to generic types like the ones above — so **no custom
SELinux policy module is expected to be required** for the documented
installation layout. Network binding needs no SELinux port-context work
either: Axiom's own `CapabilityBoundingSet=` is already empty (see §9), so
it cannot bind a privileged port (`<1024`) regardless of SELinux — the
documented port (`8443`) is unreserved and unrestricted under the targeted
policy.

**What this guide could not verify directly:** live *enforcing*-mode
behavior. The validation environment for this guide runs a kernel without
the SELinux LSM compiled in at all (confirmed via `getenforce` reporting
`Disabled` even with the policy packages installed, and no
`/sys/fs/selinux`) — a limitation of that specific test host, not of
Axiom or of RHEL. The static analysis above is real and uses RHEL's actual
policy data, but it is not a substitute for seeing zero AVC denials on a
genuine enforcing host. **Before a production rollout, on your actual
SELinux-enforcing RHEL host:**

```bash
getenforce   # confirm Enforcing
sudo systemctl start axiom
# exercise the API: health check, trigger an action, fetch logs
sudo ausearch -m avc -ts recent   # or: journalctl -t setroubleshoot
```

If that shows zero denials (expected, per the analysis above), no policy
work is needed. If a denial does appear (e.g. because of a locally
customized policy on that host), generate the *minimal* correcting module
for that exact denial and nothing broader:

```bash
sudo ausearch -m avc -ts recent | audit2allow -M axiom-local
sudo semodule -i axiom-local.pp
```

Do not use `audit2allow -a` (bulk-allows every recent denial from every
service) and do not run `setenforce 0` — both defeat the point of running
SELinux at all. Document exactly which denial required the module and why,
the same way this guide documents every other permission decision.

---

## 10. Starting and verifying

```bash
sudo systemctl enable --now axiom
sudo systemctl status axiom
sudo journalctl -u axiom -n 100 --no-pager
```

A clean start logs a single structured line noting the listen address. Any
configuration or security problem instead exits immediately with a specific
error — check `journalctl` first for anything that fails to come up.

Verify the listener from a client that holds a valid, authorized client
certificate:

```bash
curl --cacert ca.crt --cert client.crt --key client.key \
  https://<agent-host>:<port>/health
```

Expect `{"status":"ok","agent":"<agent.id>","version":"..."}`. If
`security.health.allow_anonymous` is `false` (the default), a request
without a client certificate correctly gets `401`.

---

## 11. Job history and restart behavior

The job list (`GET /v1/jobs/{id}`, `GET /v1/jobs/{id}/logs`) is **in-memory
only**. Restarting the Axiom process — for an upgrade, a crash, a host
reboot, anything — clears it completely; any job IDs a caller was polling
before the restart become unknown afterward.

**The audit log is the durable execution record.** Every accepted,
started, finished (success/failure/timeout/cancelled), and rejected request
is written there synchronously, and it survives process restarts. If a
caller needs to know what happened to a job across a restart boundary, that
answer lives in `/var/log/axiom/audit.log`, not in the job API.

This is a deliberate v1 scope decision — Axiom does not use a database to
persist job history. Given the durable audit trail already covers the
same information, adding one to make in-flight polling restart-durable
was judged not worth the added component.

---

## 12. Startup validation reference

Axiom fails closed: any one of the following aborts startup with a specific
error rather than starting partially configured.

- Structural: unknown top-level YAML fields, duplicate keys anywhere,
  missing required fields (`agent.id`, `agent.name`, listener
  address/port).
- Listener: port out of the 1–65535 range.
- mTLS material: `ca_file`/`cert_file`/`key_file` must all be set, exist,
  contain valid PEM/X.509 data, and live in directories not writable by an
  untrusted local user (walked all the way to `/`).
- Audit: `audit.path` must be absolute; its directory is subject to the
  same writability walk as certs/scripts.
- Output/history bounds: `output.max_bytes` and `jobs.max_history`, if set,
  must be within documented sane ranges.
- Actions: at least one configured; each action name must match a safe
  charset; `command` must be an absolute, clean (no `.`/`..`) path; the
  script (and every containing directory) must exist, be executable, be
  owned by root or the Axiom service account, and not be group/world-
  writable; `timeout` must be within a sane 1s–24h range; `concurrency`
  must be `shared` or `exclusive`; each parameter name must match a safe
  charset, and at most 32 parameters per action.
- Authorization: each identity name must match a safe charset, must
  declare at least one action, and every action it references must exist
  in `actions` — a typo here is a startup error, not a silent no-op.

---

## 13. Upgrade procedure

1. Build/obtain the new `axiom` binary.
2. Stop the service: `sudo systemctl stop axiom`.
3. Back up the current binary (§14 covers rollback):
   `sudo cp /usr/local/bin/axiom /usr/local/bin/axiom.previous`.
4. Install the new binary:
   `sudo install -o root -g root -m 0755 ./axiom /usr/local/bin/axiom`
   (or re-run `scripts/install.sh` with `BIN_SRC` pointing at the new
   binary — it's idempotent and won't touch your config/certs).
5. If this release changes the systemd unit, reinstall it too and
   `sudo systemctl daemon-reload`.
6. If this release changes the config schema, update
   `/etc/axiom/config.yaml` accordingly — check the changelog/spec diff.
7. Start it back up: `sudo systemctl start axiom`, then verify per §10.

In-flight jobs at the moment of `systemctl stop` do not survive the
restart (§11) — schedule upgrades for a quiet window, the same way you
would for any deploy-triggering control plane.

---

## 14. Rollback

Binary:

```bash
sudo systemctl stop axiom
sudo cp /usr/local/bin/axiom.previous /usr/local/bin/axiom
sudo systemctl start axiom
```

Config: keep your `/etc/axiom/config.yaml` under version control (outside
this repository, since it's environment-specific and may reference internal
paths) so a rollback is `git checkout` + redeploy the file, the same as any
other config-as-code rollback. There is no in-place "previous config"
kept on disk by Axiom itself.

---

## 15. Troubleshooting

| Symptom | Likely cause | Check |
|---|---|---|
| Service won't start, exits immediately | Config or security validation failure | `journalctl -u axiom -n 50` — the error names the exact field/path |
| `401` on every request including with a valid cert | Client cert not signed by the CA in `security.mtls.ca_file`, or clock skew (cert not-yet-valid/expired) | `openssl verify -CAfile ca.crt client.crt` |
| `403` on a request you expect to succeed | Client cert's Common Name isn't listed under `authorization.identities`, or doesn't list that specific action | Check the CN with `openssl x509 -in client.crt -noout -subject`; check it against config |
| `404` on `POST /v1/actions/{name}` | Action name doesn't exist in config, or path had disallowed characters | Check `actions:` in config |
| `409` on a trigger | That action is `concurrency: exclusive` and already running | `GET /v1/jobs/{id}` on the in-flight job, or check the audit log |
| Job stuck in `running` past its expected duration | Should self-resolve at `timeout` — Axiom kills the full process tree, not just the direct child | If it doesn't resolve, that's a bug — check `journalctl` for executor errors |
| `GET /v1/jobs/{id}` returns 404 for a job you triggered earlier | Axiom restarted since (§11), or the job was evicted from the bounded in-memory history (`jobs.max_history`) | Check `/var/log/axiom/audit.log` for the durable record |
| Action script fails but you can't see why | Fetch its captured output | `GET /v1/jobs/{id}/logs` |

---

## 16. Uninstall

```bash
sudo systemctl disable --now axiom
sudo rm -f /etc/systemd/system/axiom.service
sudo systemctl daemon-reload
sudo rm -f /usr/local/bin/axiom /usr/local/bin/axiom.previous

# Only remove these if you don't need the audit trail or configuration
# retained for compliance/records purposes — consider archiving first.
sudo rm -rf /etc/axiom /opt/axiom /var/log/axiom /var/lib/axiom

sudo userdel axiom
sudo groupdel axiom
```
