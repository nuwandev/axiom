# Axiom on Windows Server 2022 / Windows 10+ Pro — install bundle

This directory is the offline install bundle for the Axiom agent on Windows.
It contains:

| File | Purpose |
|---|---|
| `axiom.exe` | the agent (single static binary, no runtime dependencies) |
| `Install-Axiom.ps1` | idempotent installer: layout, ACLs, service registration |
| `Uninstall-Axiom.ps1` | stop + remove the service and binary |
| `example-windows.yaml` | starter `config.yaml` |
| `capability-examples/` | sample PowerShell capability scripts |

No Internet access is required at install time or at runtime.

**There is also an MSI** (`msi/Axiom.wxs` → `axiom-<version>-windows-amd64.msi`
in a release, built by `scripts/build-release.sh` when `wix` is on the build
machine — `dotnet tool install --global wix --version 5.0.2`; pin to v5, v7+
requires accepting a paid license just to build). It is a thin,
double-clickable / Add-Remove-Programs / `msiexec`-silent-installable wrapper
around the **same** `Install-Axiom.ps1` / `Uninstall-Axiom.ps1` in this
directory, run as MSI custom actions — it does not reimplement the ACL or
service logic, so a fix here fixes both install paths. See
[`docs/INSTALL-WINDOWS.md`](../../docs/INSTALL-WINDOWS.md) §3 for both.

## Install (script)

From an **elevated** PowerShell, in this directory:

```powershell
.\Install-Axiom.ps1
```

This creates:

```
%ProgramFiles%\Axiom\axiom.exe
%ProgramData%\Axiom\config.yaml            (from example-windows.yaml if absent)
%ProgramData%\Axiom\certs\                 (you place ca.crt / server.crt / server.key)
%ProgramData%\Axiom\capabilities\          (you place reviewed *.ps1 scripts)
%ProgramData%\Axiom\logs\                  (audit.log + axiom.log)
```

and registers the `axiom` Windows service under the virtual service account
`NT SERVICE\axiom` with automatic restart on failure. It does **not**
generate certificates, overwrite an existing `config.yaml`, or start the
service.

Then:

1. Place your mTLS material in `%ProgramData%\Axiom\certs\` as `ca.crt`,
   `server.crt`, `server.key`.
2. Place your reviewed capability `.ps1` scripts in
   `%ProgramData%\Axiom\capabilities\` and re-run `Install-Axiom.ps1` (or
   `icacls`) so they inherit the protected ACL.
3. Edit `%ProgramData%\Axiom\config.yaml`.
4. Start it:

   ```powershell
   Set-Service -Name axiom -StartupType Automatic
   Start-Service -Name axiom
   ```

   Or `.\Install-Axiom.ps1 -Start` once certs and config are in place.

## NTFS ACL model

`Install-Axiom.ps1` disables inheritance and sets an explicit protected DACL
on `%ProgramData%\Axiom` (and `%ProgramFiles%\Axiom`). The weak default
`%ProgramData%` ACL — which lets any user create files — therefore cannot
reach Axiom's tree. Axiom verifies this at startup and refuses to run if it
is not true.

| Path | Owner | Administrators / SYSTEM | `NT SERVICE\axiom` | Everyone else |
|---|---|---|---|---|
| `%ProgramFiles%\Axiom\` (+ `axiom.exe`) | Administrators | Full | Read + Execute | none |
| `%ProgramData%\Axiom\` | Administrators | Full | Read + Execute | none |
| `…\config.yaml` | Administrators | Full | Read | none |
| `…\certs\` (+ `ca.crt`, `server.crt`, `server.key`) | Administrators | Full | Read | none |
| `…\capabilities\` (+ `*.ps1`) | Administrators | Full | Read + Execute (**no write**) | none |
| `…\logs\` (+ `audit.log`, `axiom.log`) | Administrators | Full | Modify | none |

The service account can execute its capability scripts but never rewrite
them; the only writable tree is `logs\`.

## Service account

`NT SERVICE\axiom` is a per-service virtual account: no password, SCM-managed,
its own SID for ACLs, cannot log on interactively. It is **not** an
Administrator and **not** LocalSystem.

If a capability needs to reach a resource the account cannot (the Docker
named pipe, a kubeconfig, a specific deploy directory, start/stop rights on
one service), grant exactly that — e.g. add `NT SERVICE\axiom` to the
`docker-users` group, `icacls` a target directory, or `sc.exe sdset` one
service — and record why. Do not add the account to `Administrators`.

## Upgrade / rollback

```powershell
Stop-Service axiom
Copy-Item .\axiom.exe "$env:ProgramFiles\Axiom\axiom.exe.previous"   # keep a rollback copy first
Copy-Item .\axiom-<new>.exe "$env:ProgramFiles\Axiom\axiom.exe" -Force
Start-Service axiom
```

Rollback: swap `axiom.exe.previous` back and `Restart-Service axiom`.
`config.yaml`, certificates and capability scripts are never touched by an
upgrade.

## Uninstall

```powershell
.\Uninstall-Axiom.ps1            # removes the service + %ProgramFiles%\Axiom, keeps %ProgramData%\Axiom
.\Uninstall-Axiom.ps1 -PurgeData # also removes %ProgramData%\Axiom
```
