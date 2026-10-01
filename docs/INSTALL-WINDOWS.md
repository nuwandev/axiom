# Axiom — Windows Server 2022 Installation & Operations

**On Linux?** See [`getting-started.md`](getting-started.md) instead.

Generic by design: no company-specific hosts, IPs, or credentials.

Target platform: **Windows Server 2022** (x64), using the in-box Windows
PowerShell 5.1. The same binary and installers also run on **Windows 10/11
Pro** (client SKUs share the same service/ACL/PowerShell primitives) — both
have been validated end-to-end on real hosts, not just architecturally
reasoned about; see [`WINDOWS-SERVER-2022-VALIDATION.md`](WINDOWS-SERVER-2022-VALIDATION.md)
for the complete evidence trail on both. No Internet access is required at
install time or at runtime.
The external API, configuration schema, capability model, authentication and
authorization are identical to the Linux build — see the main
[`README.md`](../README.md), [`configuration.md`](configuration.md) and
[`actions.md`](actions.md). This page covers only what is Windows-specific.

**Validation status:** fresh offline install, protected-ACL application
(and its reject path), service registration under the least-privileged
virtual service account, the service actually serving real mTLS traffic,
SCM auto-restart, bad-configuration diagnostics, and the full
uninstall/reinstall/purge cycle have all been run end-to-end on real
Windows Server 2022 **and** Windows 11 Pro hosts — see
[`WINDOWS-SERVER-2022-VALIDATION.md`](WINDOWS-SERVER-2022-VALIDATION.md) for
the complete findings and evidence.

---

## 1. Prerequisites

- Windows Server 2022, with the default in-box Windows PowerShell 5.1 at
  `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`. PowerShell 7
  is **not** required or used.
- Local administrator rights for the one-time install.
- The Axiom Windows installer from
  [Releases](https://github.com/nuwandev/axiom/releases) — either
  `axiom-<version>-windows-amd64.msi` (double-click / Add-Remove-Programs /
  `msiexec /qn` silent install — §3a) or
  `axiom-<version>-windows-amd64.zip` (extract and run
  `Install-Axiom.ps1` yourself — §3b, if you want to read exactly what runs
  before running it). Both install the identical layout and ACLs; the MSI
  runs the same `Install-Axiom.ps1` internally as a custom action rather
  than reimplementing the logic. Download onto the server (or copy it in —
  no Internet access is needed after this one file is on the box) and verify
  it against the release's `SHA256SUMS` before installing:

  ```powershell
  # from a machine with network access, or directly on the server:
  Invoke-WebRequest -Uri "https://github.com/nuwandev/axiom/releases/download/<version>/axiom-<version>-windows-amd64.msi" -OutFile axiom.msi
  Invoke-WebRequest -Uri "https://github.com/nuwandev/axiom/releases/download/<version>/SHA256SUMS" -OutFile SHA256SUMS
  $expected = (Select-String -Path SHA256SUMS -Pattern 'windows-amd64\.msi$').Line.Split()[0]
  if ((Get-FileHash axiom.msi -Algorithm SHA256).Hash.ToLower() -ne $expected) { throw "checksum mismatch — do not install" }
  ```

  A `windows-amd64.exe` (the raw binary, no installer) is published alongside
  these if you only need the executable. Every tagged release also publishes
  the linux/amd64, linux/arm64, and RPM artifacts described in
  [`INSTALL.md`](INSTALL.md) — one release, every supported platform.
- An internal CA able to issue one server certificate for this agent and one
  client certificate per calling identity (the certificate Common Name is the
  identity Axiom authorizes against).
- The capability `.ps1` scripts this agent will run, written and tested
  independently — Axiom only executes them.
- Anything a capability itself calls (Docker, `kubectl`, …) already installed
  and reachable by the service account (§5).

Axiom has no other runtime dependency — it is a single static binary and
uses no .NET beyond what PowerShell 5.1 itself needs.

---

## 2. Filesystem layout

```
%ProgramFiles%\Axiom\axiom.exe                 the agent
%ProgramData%\Axiom\config.yaml                agent config
%ProgramData%\Axiom\certs\{ca,server}.crt      mTLS material (you place these)
%ProgramData%\Axiom\certs\server.key           private key   (you place this)
%ProgramData%\Axiom\capabilities\*.ps1         approved capability scripts (you place these)
%ProgramData%\Axiom\logs\audit.log             durable audit trail
%ProgramData%\Axiom\logs\axiom.log             service diagnostic log (JSON)
```

`Install-Axiom.ps1` creates this with an inheritance-protected NTFS ACL
(owner `Administrators`; `Administrators`/`SYSTEM` full; `NT SERVICE\axiom`
read/execute on the binary, config, certs and capabilities, modify only on
`logs\`). The exact table is in
[`packaging/windows/README.md`](../packaging/windows/README.md#ntfs-acl-model).

**Design intent — the Axiom service account cannot modify its own binary,
configuration, certificates, or capability scripts.** Axiom verifies the ACL
and ownership of each of those at startup and refuses to run if an
unauthorized principal (the service account included) can write them, or if
`%ProgramData%\Axiom` does not have inheritance disabled.

---

## 3. Install

### 3a. MSI (recommended)

Double-click `axiom-<version>-windows-amd64.msi` (accept the UAC prompt), or
for an unattended/scripted deployment:

```powershell
msiexec /i axiom-<version>-windows-amd64.msi /qn
```

Shows up in Add/Remove Programs, supports Group Policy software deployment
and SCCM/Intune, and upgrades cleanly in place when you install a newer MSI
later (same product family, fixed `UpgradeCode`). It does **not** generate
certificates, overwrite an existing `config.yaml`, or start the service —
same safe defaults as the script. To have it start the service immediately
(only appropriate when certs and config are already staged, e.g. by your
configuration-management tool, before running the MSI):

```powershell
msiexec /i axiom-<version>-windows-amd64.msi /qn STARTSERVICE=1
```

Uninstall from Add/Remove Programs, or `msiexec /x axiom-<version>-windows-amd64.msi /qn`
— leaves `%ProgramData%\Axiom` (config, certs, capabilities, audit log) in
place, same as the uninstall script's default.

### 3b. Zip + script (if you want to read exactly what runs first)

From an **elevated** PowerShell, in the unpacked bundle directory:

```powershell
.\Install-Axiom.ps1
```

Idempotent — safe to re-run. Creates the layout and ACLs, installs
`axiom.exe`, and registers the service. It does **not** generate
certificates, overwrite an existing `config.yaml`, or start the service —
the MSI runs this exact script internally, so both paths behave identically.

Then place your material. `Install-Axiom.ps1` already placed `hello.world`
for you — a zero-dependency action that just confirms the whole chain
works, no external dependency needed — and the shipped example config
already wires it up, so only certs and (when you're ready) your own
action's script are left:

```powershell
Copy-Item ca.crt, server.crt, server.key "$env:ProgramData\Axiom\certs\"
Copy-Item your-script.ps1 "$env:ProgramData\Axiom\capabilities\"   # your own action, once you have one
notepad "$env:ProgramData\Axiom\config.yaml"
.\Install-Axiom.ps1        # re-run so the new files inherit the protected ACL
```

Start it:

```powershell
Set-Service -Name axiom -StartupType Automatic
Start-Service -Name axiom
Get-Service axiom
```

Trigger `hello.world` first to confirm mTLS, authorization, PowerShell
execution and the audit trail all actually work end-to-end before relying
on a real action — see [`getting-started.md`](getting-started.md) §7 for
the exact `curl`/`Invoke-WebRequest` calls (same request shape on both
platforms).

---

## 4. The Windows service

`axiom.exe` registers itself (via `axiom.exe -install-service`, which the
installer calls) as a `SERVICE_WIN32_OWN_PROCESS` service:

- **Account:** the virtual account `NT SERVICE\axiom` — no password,
  SCM-managed, per-service SID. Not an Administrator, not LocalSystem.
- **Start type:** automatic.
- **Recovery:** restart after 5s, three times, failure count reset after one
  day — the analogue of the Linux unit's `Restart=on-failure`.
- **Stop / shutdown:** SCM `Stop` cancels the same graceful path the unix
  build runs on `SIGTERM` — the HTTP listener drains for up to 30s. A job
  still running when the process exits is terminated by its Job Object
  (§6); its `accepted` and `started` records are already in the audit log,
  the same as an unexpected restart on Linux.
- **Logging:** structured JSON to `%ProgramData%\Axiom\logs\axiom.log`;
  startup and fatal errors also go to the Windows **Application** event log
  (source `axiom`), so a failed `Start-Service` is diagnosable without file
  access. Configure log rotation with your normal tooling — Axiom does not
  rotate `axiom.log`.

Run `axiom.exe` directly (not as a service) for a console/dev session — it
behaves exactly like the unix binary, logging JSON to stdout and stopping on
Ctrl-C.

---

## 5. Service account privileges

The `axiom` account needs almost nothing itself: `SeServiceLogonRight` (SCM
grants it) plus outbound network for the listener's peers. Everything else is
**resource** access, granted narrowly per deployment and documented:

| A capability that… | needs | grant it with |
|---|---|---|
| runs `docker` / `docker compose` | the Docker engine named pipe | add `NT SERVICE\axiom` to the local `docker-users` group |
| runs `kubectl` | to read a kubeconfig; outbound 443/6443 | place the kubeconfig where the account can read it (`icacls … /grant "NT SERVICE\axiom:(R)"`) |
| deploys files into an app directory | Modify on that one directory | `icacls "D:\apps\myapp" /grant "NT SERVICE\axiom:(OI)(CI)M"` |
| restarts one dependency Windows service | Start/Stop on that service | `sc.exe sdset <svc> <SDDL granting the axiom service SID>` |

Do **not** add the account to `Administrators`. If a capability genuinely
needs elevation, use a dedicated scheduled task that runs as a specific
account and does exactly one hard-coded operation, triggered from the
capability with `Start-ScheduledTask`.

---

## 6. Capability execution and process containment

For every `POST /v1/actions/{name}` that passes authentication,
authorization and parameter validation, Axiom runs exactly:

```
C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe -NoProfile -NonInteractive -File <configured .ps1>
```

The interpreter path and the three switches are fixed in the binary. The
only variable is the `.ps1` path, which comes from `actions.<name>.command`
in config and is integrity-checked at startup. `-Command` is never used; no
request data reaches the command line; validated parameters arrive only as
`AXIOM_PARAM_<NAME>` environment variables, exactly as on Linux.
`-ExecutionPolicy` is not passed — Axiom neither sets nor depends on it (see
[`windows-security.md`](windows-security.md)).

The capability process and everything it spawns are confined to a Windows
**Job Object** created with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`. On timeout,
cancellation, or an uncontrolled death of the Axiom process, the whole tree
is terminated. A Windows service has no console, so there is no cooperative
"SIGTERM-equivalent" grace phase — a timed-out capability's tree is
terminated immediately. Write capability scripts to be safely re-runnable
after an interrupted execution.

---

## 7. Configuration

Identical schema to Linux. Copy `configs\example-windows.yaml` to
`%ProgramData%\Axiom\config.yaml` and edit. The only Windows specifics:

- paths are Windows paths, written as plain (unquoted) YAML scalars with
  backslashes (`C:\ProgramData\Axiom\...`), already in clean form (no `.` /
  `..`, forward slashes are rejected);
- each `actions.<name>.command` must be an absolute path ending `.ps1`.

There is no Windows-only configuration block. Startup validation is the same
fail-closed behaviour as Linux, with the file-security checks expressed in
NTFS terms.

---

## 8. Certificates

Same model as Linux ([`certificates.md`](certificates.md)): Axiom loads and
verifies PEM files, never issues or renews them. Place `ca.crt`,
`server.crt`, `server.key` in `%ProgramData%\Axiom\certs\`. Renewal requires
a service restart (`Restart-Service axiom`) — there is no hot reload. The
Windows certificate store is not used.

---

## 9. Day-2 operations: status, logs, restart

The Windows service-management commands you already know, applied to this
service — there is no Axiom-specific CLI for any of this:

```powershell
Get-Service axiom                                    # Running / Stopped / etc.
Restart-Service axiom                                 # graceful stop, drains in-flight HTTP, then starts
Stop-Service axiom; Start-Service axiom                # same, in two steps
Get-EventLog -LogName Application -Source axiom -Newest 20   # startup/fatal errors
Get-Content "$env:ProgramData\Axiom\logs\axiom.log" -Tail 50 -Wait   # live structured JSON log
Get-Content "$env:ProgramData\Axiom\logs\audit.log" -Tail 50 -Wait   # live audit trail
```

A clean start logs a single structured line noting the listen address to
`axiom.log`. Any configuration or security problem instead exits
immediately with a specific error in the Application event log — check
that first for anything that fails to come up. Axiom does not rotate
`axiom.log` or `audit.log` itself; configure rotation with your normal
tooling if you need it.

**Job history does not survive a restart.** `GET /v1/jobs/{id}` and
`GET /v1/jobs/{id}/logs` are backed by in-memory state only — restarting
the process (an upgrade, a crash, a host reboot, `Restart-Service`,
anything) clears it completely, and any job ID a caller was polling before
the restart becomes unknown afterward. **`audit.log` is the durable
execution record** — every accepted, started, finished
(success/failure/timeout/cancelled), and rejected request is written there
synchronously and survives restarts; if you need to know what happened to
a job across a restart boundary, that's where the answer lives, not the
job API. This is a deliberate v1 scope decision shared with the Linux
build, not a Windows-specific limitation — see
[`INSTALL.md`](INSTALL.md#11-job-history-and-restart-behavior) §11.

Verify the listener itself from a client holding a valid, authorized
client certificate:

```powershell
curl.exe --cacert ca.crt --cert client.crt --key client.key `
  https://<host>:8443/health
# {"status":"ok","agent":"<agent.id>","version":"..."}
```

A request without a client certificate correctly gets `401` unless
`security.health.allow_anonymous` is `true`.

---

## 10. Troubleshooting

| Symptom | Check |
|---|---|
| `Start-Service` fails immediately | Application event log, source `axiom`; and `%ProgramData%\Axiom\logs\axiom.log` — the error names the exact field or path |
| startup error `…must be owned by Administrators…` or `…grants write…` | a capability script, cert, config file or one of their directories has an ACL an unauthorized principal can write; re-run `Install-Axiom.ps1` or fix with `icacls` |
| startup error `…inheritance-protected directory…` | `%ProgramData%\Axiom` still inherits its ACL — `icacls "%ProgramData%\Axiom" /inheritance:r` (the installer does this) |
| startup error `PowerShell interpreter integrity` | `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` is missing or has a non-standard ACL |
| startup error naming `Group Policy enforces... execution policy` | a domain or local **Group Policy** (not a local `Set-ExecutionPolicy`) enforces `Restricted` or `AllSigned` machine-wide — this is the one execution-policy case Axiom cannot work around on its own (Axiom's own invocation always uses `-ExecutionPolicy Bypass`, but real Group Policy always overrides that, by design). Ask your Windows administrator to allow `RemoteSigned` via GPO, or Authenticode-sign your capability scripts if the GPO is `AllSigned`. See `docs/windows-security.md`. |
| job stuck `running` past its timeout | should self-resolve; the Job Object is terminated at the deadline — if not, check `axiom.log` for executor errors |
| `401` on every request, including with a cert you expect to work | client cert not signed by the CA in `security.mtls.ca_file`, or clock skew (not-yet-valid/expired) — `openssl verify -CAfile ca.crt client.crt` |
| `403` on a request you expect to work | client cert CN not listed under `authorization.identities`, or not for that action — `certutil -dump client.crt` for the CN |
| `404` on `POST /v1/actions/{name}` | action name doesn't exist in config, or the name had disallowed characters — check `actions:` in `config.yaml` |
| `409` on a trigger | that action is `concurrency: exclusive` and already running — `GET /v1/jobs/{id}` on the in-flight job, or check `audit.log` |
| `GET /v1/jobs/{id}` returns `404` for a job you triggered earlier | the service restarted since (in-memory job state doesn't survive a restart), or the job aged out of the bounded history (`jobs.max_history`) — check `audit.log` for the durable record |
| action script fails but you can't see why | `GET /v1/jobs/{id}/logs` for its captured stdout/stderr |

---

## 11. Upgrade

**MSI:** install the newer version's MSI over the existing one —
`msiexec /i axiom-<new-version>-windows-amd64.msi /qn`. The fixed
`UpgradeCode` (see `packaging/windows/msi/Axiom.wxs`) makes this an in-place
major upgrade: the old version is removed and the new one installed
automatically, in one `msiexec` call. `config.yaml`, certificates, and
capability scripts are never touched by an upgrade.

**Zip + script:**

```powershell
Stop-Service axiom
Copy-Item "$env:ProgramFiles\Axiom\axiom.exe" "$env:ProgramFiles\Axiom\axiom.exe.previous"   # keep a rollback copy first
Copy-Item .\axiom.exe "$env:ProgramFiles\Axiom\axiom.exe" -Force
Start-Service axiom
```

Either way: if the new release changes the config schema, update
`config.yaml` accordingly before starting — check the changelog/spec diff
first. In-flight jobs at the moment of `Stop-Service` do not survive the
restart — schedule upgrades for a quiet window, the same way you would for
any deploy-triggering control plane.

---

## 12. Rollback

**MSI:** install the previous version's MSI the same way — the fixed
`UpgradeCode` handles downgrading the same as upgrading (a plain reinstall
of the older MSI). Keep the previous version's MSI file around for exactly
this reason.

**Zip + script:**

```powershell
Stop-Service axiom
Copy-Item "$env:ProgramFiles\Axiom\axiom.exe.previous" "$env:ProgramFiles\Axiom\axiom.exe" -Force
Start-Service axiom
```

Config: keep `config.yaml` under version control (outside this repository,
since it's environment-specific) so a rollback is "redeploy the previous
file," the same as any other config-as-code rollback — Axiom itself keeps
no "previous config" on disk.

---

## 13. Uninstall

**MSI:** uninstall from Add/Remove Programs, or
`msiexec /x axiom-<version>-windows-amd64.msi /qn`.

**Zip + script:**

```powershell
.\Uninstall-Axiom.ps1            # removes the service + %ProgramFiles%\Axiom, keeps %ProgramData%\Axiom
.\Uninstall-Axiom.ps1 -PurgeData # also removes %ProgramData%\Axiom (config, certs, capabilities, audit log)
```

Either path leaves `%ProgramData%\Axiom` in place by default — matching
`dnf remove` on Linux, not `dnf remove --purge`. Pass `-PurgeData` (zip
path) or manually remove `%ProgramData%\Axiom` after an MSI uninstall if
you want the config/certs/audit log gone too.
