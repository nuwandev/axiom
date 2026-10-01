# Axiom on Windows — Security Model

This page explains how the Windows build preserves each security property of
the Linux build with a Windows-native mechanism. It is a companion to
[`security.md`](security.md) and [`THREAT-MODEL.md`](THREAT-MODEL.md), which
cover the platform-independent controls (mTLS, identity, authorization,
parameter validation, audit) — those are **unchanged** on Windows.

## Axiom is still not a remote shell

The core invariant holds identically:

```
authenticated identity → authorized capability → validated parameters → configured .ps1 → local operation
```

There is no path from an HTTP request to an arbitrary executable, arbitrary
PowerShell source, an arbitrary script path, or an arbitrary command-line
argument. The Windows executor builds one fixed argument vector:

```
powershell.exe  -NoProfile  -NonInteractive  -File  <config.Actions[name].command>
```

`powershell.exe` is resolved from `%SystemRoot%` (never `PATH`, never
config) and integrity-checked at startup. The three switches are compile-time
constants. The `.ps1` path is the only variable element, it comes from the
config registry (not the request), and `internal/config` validates it at
load: absolute, clean, `.ps1` extension, a restricted path character set (no
`" ' $ backtick ; & | < > % * ?`), not a reparse point, and protected by NTFS
ACLs (below). `-Command` / `-EncodedCommand` are never used.

## Execution policy is not a security boundary — and the product should not depend on an operator changing it

`-ExecutionPolicy Bypass` **is** passed on Axiom's own fixed invocation —
see `buildCommand` in `internal/executor/launch_windows.go` for the full
reasoning, summarized here.

**Why this is safe.** Execution policy was never Axiom's security boundary
in the first place: **the caller cannot name or supply a script** (the
name→`.ps1` map is server-side config), **the caller cannot supply code or
arguments** (no request data reaches the command line), and **the script
cannot be swapped** (NTFS ACLs, verified fail-closed at startup). None of
that depends on execution policy, and passing `Bypass` on Axiom's own
invocation changes none of it: the flag is scoped to this one process only,
calls no `Set-ExecutionPolicy`, and persists nothing on the machine.

**Why this is necessary, not just convenient.** Real-host validation found
that Windows 10/11 Pro ships with the local policy `Restricted` by default
(Windows Server ships `RemoteSigned`) — see
`docs/WINDOWS-SERVER-2022-VALIDATION.md`. `Restricted` blocks *every* local
script unconditionally, signed or not. Requiring every customer to run
`Set-ExecutionPolicy` themselves before the product works at all is exactly
the kind of friction this project does not accept — Axiom's own invocation
handles this by itself, the same way it handles everything else it controls.

**The one thing `Bypass` genuinely cannot override: real Group Policy.**
`MachinePolicy`/`UserPolicy` scope (set via a domain or local GPO) always
wins over any command-line `-ExecutionPolicy`, by PowerShell's own design —
no software fix changes that. `CheckPlatformPrerequisites` (same file)
checks specifically for this at startup and refuses to come up if a GPO
enforces `Restricted` or `AllSigned`, with a message naming the exact
administrative action needed (allow `RemoteSigned` via GPO, or sign
capability scripts if the GPO is `AllSigned`) — this is a genuine,
unavoidable precondition, not something Axiom is choosing not to solve.

## Parameter values are inert data

Validated parameters reach the capability only as `AXIOM_PARAM_<NAME>`
environment variables — identical to Linux. PowerShell does not parse,
expand, or execute the contents of an environment variable when a script
reads `$env:X`; the string is returned verbatim. A value containing
`' " $ backtick ; & | ( ) < > newline`, Unicode, or literal PowerShell is a
string, not code. It becomes code only if the *script itself* passes it to
`Invoke-Expression` / `iex` / `& ([scriptblock]::Create(...))` — a
script-authoring anti-pattern, the same as `eval "$VAR"` on Linux, documented
in the sample scripts. Declaring a regex `pattern` for every parameter that
reaches a script is the recommended belt to that suspenders.

## Filesystem integrity — NTFS ACLs instead of POSIX ownership/mode

The property: an unauthorized local principal must not be able to modify a
capability script, `config.yaml`, or the TLS key material and thereby obtain
Axiom's execution context. Startup fails closed if it is not met.

The Windows checks (`internal/winsec`), applied to each capability `.ps1`,
the interpreter, `config.yaml`, `ca.crt`, `server.crt`, `server.key` and
their directories:

| Linux (POSIX) | Windows (NTFS) |
|---|---|
| leaf is not a symlink | leaf is not a **reparse point** (symlink or junction) |
| owner is root or the service account | owner is `Administrators`, `SYSTEM`, or `TrustedInstaller` — **not** the service account |
| not group/world-writable | DACL grants write / delete / take-ownership to **no** principal other than those three — the Axiom service account included (so it can execute its capabilities, never rewrite them) |
| executable bit | the file is a `.ps1` (for capability scripts) |
| every ancestor dir to `/` is root-owned and not group/world-writable | ancestor dirs are trusted-owned with no untrusted write, up to a directory whose DACL has **inheritance disabled** (`SE_DACL_PROTECTED`) — normally `%ProgramData%\Axiom`, set by the installer |

The ancestry walk stops at the protected directory rather than climbing to
the volume root, because the default `%ProgramData%` ACL lets any user create
files — an inheritance-protected `%ProgramData%\Axiom` is a complete cut, so
that weak ACL never reaches Axiom's tree.

Inherit-only ACEs (`INHERIT_ONLY_ACE`) are ignored: `CREATOR OWNER` /
`CREATOR GROUP` and the "children get full control" ACEs present on nearly
every real Windows directory do not grant access to the directory itself,
only to children — and each child is validated on its own when Axiom reads
it. An **effective** (non-inherit-only) write ACE for an untrusted principal
is still rejected.

The one deliberate looseness, matching the Linux check's allowance for a
service-account-owned directory: an ancestor **directory** may grant the
service account write (the `logs\` tree legitimately needs it). Do not grant
the service account write to `capabilities\` — the installer does not, and a
leaf `.ps1` that *is* service-writable is still rejected.

`docs/development.md` describes what of this is unit-tested versus verified on
a real host.

## Service identity and least privilege

`NT SERVICE\axiom` — a per-service virtual account: no password, its own SID,
cannot log on interactively, **not** in `Administrators`, **not** LocalSystem.
The Linux `CapabilityBoundingSet=` / `NoNewPrivileges=true` analogue is the
service's minimal `RequiredPrivileges`; notably `SeImpersonatePrivilege` and
`SeAssignPrimaryTokenPrivilege` are excluded.

Capability resource access (Docker pipe, kubeconfig, a deploy directory, one
service's start/stop) is granted narrowly and per deployment — see
[`INSTALL-WINDOWS.md` §5](INSTALL-WINDOWS.md#5-service-account-privileges).
Axiom runs every capability as this one account; it does not switch identity
per capability (that would be a privilege-management system and a new attack
surface).

## Process containment

A Windows **Job Object** with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` is the
analogue of the unix process group **and** of `PR_SET_PDEATHSIG` — and it
covers descendants the Linux Pdeathsig does not. `TerminateJobObject` kills
the whole tree on timeout or cancellation; closing the job handle (which
Axiom alone holds) kills it if Axiom itself dies uncontrolled.

## Accepted residual risks (Windows-specific)

1. **Job Object assignment race.** The child is assigned to the job
   immediately after `CreateProcess`. In the sub-millisecond window before
   assignment `powershell.exe` has executed no script line and spawned
   nothing, so nothing escapes; the residual is theoretical. If the
   assignment itself *fails* while the child is still running (a job-object
   nesting conflict, an access-denied security descriptor), the executor
   kills the child and fails the job closed rather than running the
   capability uncontained.
2. **No cooperative stop phase.** A service has no console, so a timed-out
   capability's tree is terminated immediately with no Ctrl-Break-equivalent
   grace period. Capability scripts must be safely re-runnable after
   interruption.
3. **Daemon-delegated work.** Killing a `docker` / `kubectl` process does not
   stop work already handed to `dockerd` or a remote API server — identical
   to Linux.
4. **Owner-rights on a service-account-owned file.** If an operator makes the
   service account the *owner* of a capability script (the installer sets
   owner `Administrators`), owner rights would let it change the DACL. The
   startup check requires owner ∈ {Administrators, SYSTEM, TrustedInstaller},
   so this is caught — but only if the check runs; keep the installer's
   ownership.
5. **Output encoding.** PowerShell 5.1 writes redirected output in the
   console code page, not UTF-8; a capability emitting non-ASCII should set
   `[Console]::OutputEncoding` (shown in the samples) or `/v1/jobs/{id}/logs`
   may show mojibake for those bytes. Not a security issue.
6. **Local administrators are trusted**, exactly as root is trusted on Linux.

## Pre-existing, cross-platform, out of scope for Windows support

`GET /v1/jobs/{id}` and `/logs` authenticate but do not authorize per
identity; production cancellation is plumbed but has no endpoint. Both are
platform-independent and tracked separately — Windows support neither
introduces nor fixes them.
