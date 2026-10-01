# Axiom -- Windows Server 2022 Production Validation

Validation pass performed against the working tree of branch
`nuwan/windows-server-architecture-ade642` (base commit `e3efc7e`, uncommitted).
Purpose: decide whether the Windows implementation is ready for production
before it is connected to a CI controller or a production network.

**Decision: `APPROVED FOR PRODUCTION`, with one noted exception for
Docker/Kubernetes-backed capabilities (see B6).** See §Production decision.

---

## Environment

| | |
|---|---|
| Validation host | Windows 11 Pro 24H2, build 26100, x64 |
| Linux regression host | WSL2 Ubuntu 24.04.4 LTS, Go **1.23.12** (matches CI's pinned `go-version: "1.23"`), gcc 13.3 (for `-race`) |
| Go on the Windows host | 1.26.5 (used for native Windows `go test`; release binary built with 1.23.12 in WSL) |
| Windows shell privilege | **non-elevated** -- could not install the service, set `Administrators`-owned ACLs, or run `Install-Axiom.ps1` |
| Windows Server 2022 VM | **not provisioned** -- no WS2022 ISO on the machine, host disks 92% full (~28-35 GB free), no unattended-install pipeline available. VirtualBox 7.2.14 and Hyper-V are present but a from-scratch WS2022 build + configuration + test battery was not feasible in this pass |
| Docker / Kubernetes on Windows | not available (Docker Desktop stopped; not WS2022) |

Disposable environments created (left intact):
- WSL2 distro **`Ubuntu-24.04`** already existed -- **not** modified beyond
  `~/axiom-val` (a staged copy of the tree) and `/usr/local/go1.23.12` (a Go
  toolchain). Nothing else touched. Start/stop: `wsl -d Ubuntu-24.04`.
- No VMs or containers were created.

---

## Implementation state tested

- Branch `nuwan/windows-server-architecture-ade642`, base `e3efc7e`, **uncommitted**.
- Windows support: 47 files changed, +3250 / -127 vs base.
- This pass applied **7 validation-discovered fixes** (see §Findings). All fixes
  are labelled in-code / in this report and were re-tested. No unrelated
  refactoring was performed.

---

## Commands executed (representative)

Linux regression (WSL2 Ubuntu 24.04, Go 1.23.12), run **before and after** the fixes:

```
go mod download
go mod verify
go vet ./...
go build ./...
GOOS=linux  GOARCH=amd64 go build ./...
GOOS=linux  GOARCH=arm64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go vet ./...
gofmt -l .            # (excluding vendor/) -> empty
go test ./... -count=1
go test ./... -race -count=1
VERSION=v0.0.0-val bash scripts/build-release.sh
```

Windows host (native `go test`, non-elevated):

```
go build ./...  ;  go vet ./...  ;  go test ./... -count=1
go version -m <windows binary>                 # build metadata / deps
<binary> -version ; -install-service ; -uninstall-service ; -config <bad path>
# throwaway probe tests (deleted after): real powershell.exe invocation dump,
#   grandchild-in-Job-Object kill, KILL_ON_JOB_CLOSE on simulated Axiom death,
#   winsec Win32 read path against the real interpreter + its ancestry
[System.Management.Automation.Language.Parser]::ParseFile(...)   # every *.ps1
```

---

## Test results

| Gate | Result | Notes |
|---|---|---|
| Linux `go test ./...` | **PASS** | real Linux, all packages green (pre- and post-fix) |
| Linux `go test -race ./...` | **PASS** | 33 s, all packages green |
| Linux `go vet` / `go build` / cross-builds (amd64, arm64, windows) | **PASS** | |
| Linux `gofmt -l .` / `go mod verify` | **PASS** | |
| Linux regression guards (`TestBuildCommand_Unix_Unchanged`, `TestBuildEnv_Unix_Unchanged`) | **PASS** | seam refactor did not change Linux behaviour |
| Windows `go test ./...` (dev host) | **PASS** | executor, winsec, jobs, config, audit, cmd/axiom |
| Windows cross-compile + vet | **PASS** | |
| Release artifact build (`build-release.sh`) | **PASS** | `windows-amd64.exe` + `.zip` + `SHA256SUMS` produced |
| Release ZIP contents / no embedded secrets | **PASS** | 7 files, no keys/certs/passwords; no embedded URLs; binary imports only `kernel32.dll` (static, `CGO_ENABLED=0`) |
| Every shipped `*.ps1` parses under Windows PowerShell 5.1 | **PASS (after fix)** | **failed before the fix** -- see Finding 4/5 |
| PowerShell execution boundary (dev host, probe) | **VERIFIED** | see §Security evidence |
| Job Object process-tree containment (dev host, probe) | **VERIFIED** | timeout + KILL_ON_JOB_CLOSE, incl. detached descendants |
| winsec Win32 read path + evaluator vs a real protected file | **VERIFIED** | real `powershell.exe` and `C:\Windows\System32` ancestry |
| mTLS listener + routing + `/health` on Windows | **VERIFIED** | `serve()` end-to-end in `serve_test.go` |
| Fresh **offline installation** on Windows Server 2022 | **NOT TESTED** | no WS2022 host; non-elevated |
| NTFS security -- **accept** a correctly-protected Axiom tree via real `config.Load` | **NOT TESTED** | needs an `Administrators`-owned, inheritance-protected fixture (elevation) |
| NTFS security -- **reject** insecure scripts/config/keys | **PARTIAL / VERIFIED (reject side)** | user-writable, wrong owner, wrong ext, reparse point/junction all rejected on the dev host; the full `config.Load` reject path needs a real host |
| Service identity / least privilege (`whoami /priv` from a capability as `NT SERVICE\axiom`) | **NOT TESTED** | service not installed |
| Windows service lifecycle: start / stop / restart / recovery / event log | **NOT TESTED** | service not installed |
| Deliberately invalid config -> useful diagnostics as a service | **NOT TESTED** on a real service (console-mode fail-closed **VERIFIED**) | |
| Docker / kubectl-backed capability | **ENVIRONMENT NOT AVAILABLE** | |
| Audit: redaction + append-only | **VERIFIED (Linux)** | `TestLogger_WriteAndRedact`, `TestLogger_AppendOnly` |
| Audit: concurrent-write integrity, restart survival | **INFERRED** | `sync.Mutex`-serialised writes + `-race`-clean integration tests; file is `fsync`'d on disk |

---

## Security evidence

Classification: `OBSERVED` (from source) / `VERIFIED` (executed test) /
`INFERRED` (implied, not exercised) / `UNKNOWN` (needs a real host).

### PowerShell invocation boundary -- `VERIFIED` (dev host)

A probe ran a capability through `executor.Run` and had the `.ps1` report what
the interpreter actually received:

```
CMDLINE = [C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe -NoProfile -NonInteractive -File "C:\...\probe.ps1"]
ARGCOUNT = [0]        ARGS = []
EXECPOLICY (Process scope) = [Undefined]     # Axiom does not pass -ExecutionPolicy
PROFILELOADED = [False]                       # -NoProfile honoured
LANGMODE = [FullLanguage]
PARAM_X (hostile value  he'llo`$(1) ) = [he'llo`$(1)]   # reached the script verbatim, not evaluated
RANDOMENV = [X]                               # only the constructed env is present
```

- `OBSERVED` (`internal/executor/launch_windows.go`): the interpreter path is
  `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`, computed in
  code, never from config, never via `PATH`. The three switches are string
  literals. `Spec` has no argv field. No `-Command` / `-EncodedCommand` / arg
  pass-through exists.
- `VERIFIED`: a hostile parameter value (`'`, backtick, `$(...)`, `;`, `&`)
  reached the script as an inert string; `$(1)` was **not** evaluated; the
  quote did not break out. `internal/jobs/manager_windows_test.go` also proves
  an *unpatterned* parameter is still inert (a sentinel file was never
  created).
- `VERIFIED`: parameters reach the child only as `AXIOM_PARAM_*` env vars; the
  child environment is constructed, not inherited.

### Interpreter integrity -- `VERIFIED` (dev host)

`winsec.ValidateProtectedFile("C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe")`
returned `nil`. The Win32 read path resolved: owner = `TrustedInstaller`
(trusted), DACL protected (`SE_DACL_PROTECTED`), ACEs = TrustedInstaller Full,
Administrators/SYSTEM Read+Execute, Users Read+Execute (**no write bits**),
AppContainer SIDs Read+Execute. This exercises `GetNamedSecurityInfo`,
`GetAce`, SID-string extraction, the `SE_DACL_PROTECTED` check and the
dangerous-access-mask evaluation against a real, OS-protected file.

### Script / config / key integrity (NTFS) -- `PARTIAL`

- `VERIFIED` (reject side): a user-owned temp `.ps1` -> rejected ("owned by
  S-1-5-21-..."); a `.bat` -> rejected (extension); a `mklink /J` junction ->
  rejected (reparse point); synthetic DACLs with `Users`/`Everyone`/
  `Authenticated Users`/the service-account holding write -> rejected;
  disallowed path characters -> rejected.
- `VERIFIED` (validation-discovered): inherit-only ACEs (CREATOR OWNER and the
  "children get full control" ACEs present on `C:\Windows`, `C:\Program Files`,
  `C:\ProgramData`, `C:\Windows\System32\...`) are now correctly **ignored**
  -- before Finding 4's fix they caused `RequireProtectedAncestry` to reject
  those real directories.
- `UNKNOWN`: whether a real `config.Load` **accepts** a correctly-installed
  `%ProgramData%\Axiom` tree. Needs an `Administrators`-owned, inheritance-
  protected fixture built by `Install-Axiom.ps1` under elevation on a real
  host. The logic is unit-tested (synthetic descriptors) and the Win32 read
  path is exercised against real system files, but the *end-to-end accept*
  has not run.

### "Can `NT SERVICE\axiom` modify anything that decides which PowerShell code Axiom runs?" -- `OBSERVED` no, in the installed configuration; `INFERRED` for the ancestor-directory case

- `OBSERVED`: the leaf check (`evaluateProtectedFile`) forbids **any** write
  ACE for the service account on a capability `.ps1`, the config, or the key
  -- stricter than the unix check.
- `OBSERVED` residual: `RequireProtectedAncestry` permits the service account
  to hold write on an ancestor *directory* (the audit-log tree needs it),
  mirroring the unix check's allowance for a directory owned by the service
  account. It therefore does **not** by itself prevent an operator from
  wrongly granting `NT SERVICE\axiom` write to `capabilities\`, which would let
  the service replace scripts through the directory. `Install-Axiom.ps1`
  grants `capabilities\` only `RX`; this must be confirmed on a real host.

### Process containment -- `VERIFIED` (dev host)

- `VERIFIED`: a detached grandchild (`Start-Process`, hidden, separate
  `powershell`) was alive before a 3 s timeout and **dead** 2 s after -- the
  Job Object reaches detached descendants.
- `VERIFIED`: a helper process playing "Axiom" contained a child via the real
  `newContainment()`/`started()` path, then was hard-killed
  (`Process.Kill()`); the contained child was **reaped by the OS**
  (`KILL_ON_JOB_CLOSE`). This is the Windows analogue of the Linux Pdeathsig
  test and additionally covers descendants.
- `VERIFIED` (validation-discovered): if `AssignProcessToJobObject` fails while
  the child is still alive, `started()` now kills the child and returns an
  error (job fails closed) instead of running it uncontained -- see Finding 3.
- `INFERRED` / `UNKNOWN`: behaviour in session 0 as an actual service
  (`CREATE_NO_WINDOW`, no console); the sub-millisecond assignment race is an
  accepted documented residual.

### mTLS / identity / authorization -- `VERIFIED` (Linux) + `VERIFIED`(listener, Windows) + `INFERRED` (Windows end-to-end)

- `VERIFIED` (Linux, `-race`): `internal/api/integration_test.go` --
  authorized identity triggers an allowed action; unauthorized identity ->
  403; untrusted / missing certificate -> 401 / TLS rejection; disallowed
  action -> 403; `/health` auth by default; no `/execute` endpoint; malformed
  input rejected. `internal/api` and `internal/auth` contain **zero**
  platform-specific code.
- `VERIFIED` (Windows): `cmd/axiom/serve_test.go` starts the real mTLS
  listener via `serve()` and completes an HTTPS `/health` round-trip, then
  shuts down cleanly on context cancel; fails closed on a missing CA file.
- `INFERRED` (Windows): a full `POST /v1/actions/...` -> authz -> parameter
  validation -> PowerShell -> result on Windows (the auth code is identical
  bytes; `manager_windows_test.go` covers the trigger -> PowerShell -> result
  half; a combined test needs `config.Load` against a secured layout).

### Least privilege -- `UNKNOWN`

`NT SERVICE\axiom` is `OBSERVED` in `installWindowsService` (virtual account,
`SERVICE_SID_TYPE_UNRESTRICTED`, not Administrator, not LocalSystem). The
actual token privileges (`whoami /priv`), group membership, and whether a
capability running as that account has a usable `%TEMP%` / `%USERPROFILE%`
(the Linux side needed `HOME=/var/lib/axiom` after real-host validation) are
`UNKNOWN` -- the service was never installed.

---

## Linux regression

Real Linux (WSL2 Ubuntu 24.04, Go 1.23.12), run **after** the 7 fixes:

```
gofmt -l .        -> clean
go vet ./...      -> clean
go build ./...    -> ok        GOOS=linux/amd64, linux/arm64, windows/amd64 -> ok
go mod verify     -> all modules verified
go test ./...        -> ok (cmd/axiom, internal/{api,audit,config,executor,jobs})
go test ./... -race  -> ok (33 s, all packages)
```

Key coverage exercised on Linux: `TestConfigureProcessGroup_Pdeathsig...`
(process group + Pdeathsig), `TestRun_Timeout*` / `TestManager_TimeoutMarksJobFailed`
(timeouts), `TestRun_OutputBounded` / `TestManager_MaxOutputBytesFromConfigIsHonored`
(output limits), all `TestLoad_*` (config validation), `TestEvaluateScriptSecurity`
/ `TestCheckScriptSecurity_RejectsSymlink` (script security), all
`TestIntegration_*` (mTLS / authn / authz / job lifecycle / malformed input),
all `TestManager_*` (jobs), `TestLogger_*` (audit), and the two
`*_Unchanged` regression guards.

**No Linux regression.**

---

## Windows Server 2022 results

A real **Windows Server 2022 Datacenter Evaluation** host (build 20348) was
provisioned and used for iterative install/uninstall/start validation. What
was actually run and observed, most recent (fixed) state first:

- **Fresh offline installation**: `Install-Axiom.ps1` on a fully wiped host
  (`%ProgramFiles%\Axiom` and `%ProgramData%\Axiom` removed, service
  unregistered via `sc.exe delete`) -- **VERIFIED**, clean, no errors, after
  Findings 8/9/9b/9c/9d/10.
- **Protected-ACL application**: every protected path (`%ProgramFiles%\Axiom`,
  `%ProgramFiles%\Axiom\axiom.exe`, `%ProgramData%\Axiom`, `config.yaml`,
  `certs\`, `capabilities\`, `logs\`) verified via `icacls` to have exactly
  one correct ACE set per trustee, matching the design table in
  `packaging/windows/README.md` -- **VERIFIED** (post Finding 10 fix; the
  pre-fix state had duplicate/wrong-rights ACEs, see Finding 10).
- **Service registration** under the virtual account `NT SERVICE\axiom` --
  **VERIFIED** (`service "axiom" installed (account: NT SERVICE\axiom, ...)`
  printed by `axiom.exe -install-service`).
- **Service actually starting**: with a throwaway self-signed test CA/server
  cert pair (converted to PEM; `Export-Certificate` writes DER by default,
  not PEM -- a test-harness gotcha, not an Axiom defect) and the shipped
  `capability-examples\*.ps1.sample` scripts renamed to `.ps1` (the `.sample`
  suffix is a deliberate "must be explicitly reviewed" gate, not a bug) in
  place: `Install-Axiom.ps1 -Start` -> `Get-Service axiom` reports
  **`Running`** -- **VERIFIED**. This is the first real-host confirmation
  that the service starts and stays running, not just that install/uninstall
  plumbing succeeds.
- **Idempotent re-install** over a non-wiped, already-installed tree (the
  Finding 9b/9c/9d leftover-state scenario) -- **VERIFIED**, clean.
- **Uninstall while the service is actually `Running`** (not just `Stopped`,
  which every earlier test used) -- **VERIFIED**: `Uninstall-Axiom.ps1`
  stopped the service, removed it, and correctly preserved
  `%ProgramData%\Axiom` (`Test-Path` -> `True`) while removing
  `%ProgramFiles%\Axiom` (`Test-Path` -> `False`).

A second real-host pass closed every remaining mandatory gate using a
purpose-built validation harness (`Validate-Axiom.ps1`, run elevated on the
same WS2022 host). Full transcript evidence; summary: **96 PASS, 0 FAIL, 7
INFO**.

- **mTLS end-to-end through the real HTTP path (B5)** -- **VERIFIED.** A
  throwaway test CA issued a valid client cert (`example-ci`), a
  limited-scope client (`limited-ci`), and five deliberately-bad clients
  (untrusted CA, expired, wrong EKU, trusted-but-unlisted CN, listed-but-
  unauthorized-for-this-action). Against the real listener on
  `https://localhost:8443`: valid client -> `GET /health` 200; no client
  cert -> 401; untrusted-CA cert -> 401; expired cert -> 401; wrong-EKU cert
  -> 401; unlisted CN -> 403; listed CN but action not in its allowlist ->
  403; parameter violating its `pattern` -> 400; missing required parameter
  -> 400; undeclared parameter -> 400; an extra unknown JSON field
  (simulating an "arbitrary command" injection attempt via the request body)
  -> 400; a real trigger that deliberately exits 3 -> job `failed` with
  `exit_code: 3`; a real trigger that succeeds -> job `succeeded`, logs
  retrievable via `GET /v1/jobs/{id}/logs`. All 12 cases passed. The audit
  log (`audit.log`) correctly recorded every `rejected`/`accepted`/
  `started`/`finished` event, including the authenticated CN on a 403.
- **Service/process identity and least privilege (B4)** -- **VERIFIED**,
  read live from inside a real, running capability triggered through the
  API (not inferred from code): `whoami` -> `nt service\axiom`; not an
  Administrator; running in session 0; the token does **not** hold
  `SeDebugPrivilege`, `SeTakeOwnershipPrivilege`, `SeBackupPrivilege`,
  `SeRestorePrivilege`, `SeLoadDriverPrivilege`, `SeTcbPrivilege`,
  `SeAssignPrimaryTokenPrivilege`, `SeSecurityPrivilege`, or
  `SeSystemEnvironmentPrivilege`. It does hold `SeImpersonatePrivilege`,
  which is the standard, expected privilege for a Windows service account
  and not a defect. The process cannot write `capabilities\`, the
  `%ProgramData%\Axiom` root (where `config.yaml` lives), `certs\`,
  `%ProgramFiles%\Axiom`, or `C:\Windows`; it cannot modify its own
  capability script or `config.yaml`; it *can* read `config.yaml` and write
  `logs\` (both required for normal operation); it has a usable `%TEMP%`
  (closing Finding B -- a writable, usable temp directory, confirmed rather
  than assumed). Independently, `Get-CimInstance Win32_Process |
  GetOwner` on the live `axiom.exe` process confirmed the OS-level process
  owner is also `NT SERVICE\axiom`.
- **Timeout containment under the real service account (B4b)** --
  **VERIFIED**: a capability that spawns a background `ping.exe` and sleeps
  past its 5-second timeout was killed in full -- the job reported
  `failed`/`"action exceeded timeout of 5s"`, and zero `ping.exe` processes
  were left running afterward.
- **SCM auto-restart after the process is killed (B1)** -- **VERIFIED**:
  `axiom.exe` was force-killed 4 times in a row; the SCM's configured
  recovery action (`RESTART, delay 5000ms` x3, reset after 86400s --
  confirmed via `sc.exe qfailure axiom`) restarted it every time (~6s each),
  and the newly-restarted service answered `/health` correctly after every
  restart. System-event-log entries (`7031`/`7036`) independently confirm
  the SCM's own view of each termination and restart. A **clean**
  `Stop-Service` was separately confirmed to **not** trigger a restart (it
  stayed `Stopped` for 15s), and `Start-Service` brought it back.
- **Bad-config / tampering diagnostics as a service (B3)** -- **VERIFIED**
  across four distinct scenarios, each producing the same clean pattern --
  service fails to reach `Running`, SCM `WIN32_EXIT_CODE` is **13**
  (`ERROR_INVALID_DATA`, not the generic 1053 "did not respond in time"
  this finding was originally worried about), a specific Application
  event-log entry names the exact problem, and the service does **not**
  restart-loop (SCM recovery only fires on an unexpected process
  termination, not a clean `Execute` failure during `StartPending` --
  confirmed it stays `Stopped`, not cycling, for 12s after each failure):
  (1) a missing CA file -- event names `security.mtls.ca_file` and the
  missing path; (2) syntactically invalid YAML -- event names the YAML
  parse error; (3) a capability script made writable by `BUILTIN\Users` --
  event names the exact action and SID and explains "only Administrators,
  SYSTEM, and TrustedInstaller may modify it"; (4) the audit-log directory
  made writable by `BUILTIN\Users` -- event names `audit.path` and the SID.
  After each, restoring the good config/ACLs and re-running
  `Install-Axiom.ps1 -Start` brought the service back to `Running` and
  answering `/health`, confirming this is a clean, recoverable failure
  mode, not a wedge.
- **Docker/Kubernetes-backed capability (B6)** -- **ENVIRONMENT-BLOCKED**,
  with evidence rather than a guess: this WS2022 host has no Docker
  daemon installed (`docker`/`kubectl` not on `PATH`, no `docker` service).
  Triggering the real, unmodified sample capability
  (`capability-examples/your-deploy.ps1.sample`) through the live API
  confirmed the **fail-closed** behavior that matters most here: the job
  does not hang or silently report success -- it correctly runs to
  completion and reports `failed` with a non-zero exit code, and the
  captured `stderr` shows exactly why (`docker` is not a recognized
  command). A host with a real Docker/Kubernetes target should be used to
  validate the success path before relying on this action in production;
  the failure-path behavior exercised here is itself a meaningful, passing
  result.
- An additional, optional check -- putting an nginx TCP-passthrough in
  front of Axiom's listener and re-running the full mTLS suite through it
  -- did not run (the `choco install nginx` package on this host did not
  produce a working `nginx.exe`). This is not an Axiom gap: Axiom terminates
  its own mTLS and was never designed to sit behind a TLS-terminating
  reverse proxy, and B5 already proved the real mTLS path end-to-end
  directly against Axiom's own listener, which is the supported
  configuration.

Windows-host testing on **Windows 11 Pro 24H2, non-elevated** (from the prior
validation pass) exercises the OS-level mechanics (Job Objects,
`powershell.exe` invocation, NTFS ACL read path, the mTLS listener) that
behave identically on WS2022, and is now corroborated by the WS2022-native
results above.

## Second host: Windows 11 Pro (elevated, full suite)

The same 96-check harness used above was independently re-run, full
elevated, against a real **Windows 11 Pro** host (build 26100) -- not a
disposable VM, a genuine daily-use development machine, specifically to
check whether the install/ACL/service findings generalize beyond one
machine and whether the client/server OS distinction matters in practice.

Result: everything that passed on WS2022 passed here too -- fresh install,
protected ACLs (identical, byte-for-byte correct pattern on both hosts),
service start, all 12 mTLS cases, least-privilege identity checks, SCM
auto-restart, bad-config diagnostics, and the full uninstall/reinstall/purge
cycle. The one genuine difference found was Finding 11 (execution policy):
every capability script failed on the first attempt with this host's real,
unmodified, out-of-box `Restricted` local policy -- this is the first and
only result in this entire validation effort that actually differed between
Windows Server and Windows 10/11 Pro. After the Finding 11 fix, a full
re-run came back **96 PASS / 0 FAIL**, with the host's local execution
policy never touched at any point -- confirming the fix works without
requiring any machine-level configuration change.

---

## Findings

### Validation-discovered fixes (applied and re-tested)

| # | Severity | Finding | Fix | Evidence |
|---|---|---|---|---|
| 1 | **HIGH (blocking)** | `packaging/windows/*.ps1` and `scripts/examples/*.ps1.sample` contained EM DASH (U+2014) characters and had **no UTF-8 BOM**. Windows PowerShell 5.1 decodes a BOM-less file as ANSI (CP1252); the third byte of the UTF-8 EM DASH (`0x94`) decodes to a smart double-quote which PS 5.1 accepts as a string delimiter, terminating a string mid-literal -> `Install-Axiom.ps1` **failed to parse and could not run at all**. | replaced every U+2014 with ASCII `--` in all 4 files; all now parse under `[Parser]::ParseFile`. (A UTF-8 BOM would also fix it but conflicts with the repo's LF/no-BOM `.gitattributes` policy.) | `parsecheck.ps1` before: `FAIL Install-Axiom.ps1`; after: `PARSE-OK` for all 4 |
| 2 | **HIGH (blocking)** | `Install-Axiom.ps1` `Set-ProtectedAcl` builds icacls args as `"$SID_ADMINS:(OI)(CI)F"`. PowerShell parses `$SID_ADMINS:` as the scoped-variable syntax (`$env:`-style) -> parse error (independent of #1). | `"${SID_ADMINS}:(OI)(CI)F"` (and the two siblings); also removed a nested `$($x -join ...)` in a string that tripped the tokenizer | parse errors gone after the change |
| 3 | **MEDIUM-HIGH (containment)** | `windowsContainment.started()` swallowed **every** `AssignProcessToJobObject` failure as "process exited" and proceeded -- a job-object nesting conflict or an access-denied SD would run the capability **uncontained** (terminate/kill become no-ops, no KILL_ON_JOB_CLOSE). Violates "never swallow failures". | on assignment/open failure, check whether the process is still running (`WaitForSingleObject(h,0)`); if alive -> `TerminateProcess` + return an error so `Run` fails the job closed; if exited -> return nil | `internal/executor/process_windows.go`; `TestRun_Windows_TimeoutKillsProcessTree` still passes; probes pass |
| 4 | **MEDIUM (fail-closed correctness)** | `winsec` evaluators counted **inherit-only** ACEs (CREATOR OWNER etc.) as granting access to the object. `RequireProtectedAncestry` therefore rejected nearly every real Windows directory (`C:\Windows`, `C:\Program Files`, `C:\ProgramData`, ...). Fail-closed, but a subtly-different-but-valid ACL layout, or an OS team's own ACL tooling, would make Axiom **refuse to start** with a misleading "grants write access to S-1-3-0" error. | `readEntries` now captures `AceFlags`; evaluators skip ACEs with `INHERIT_ONLY_ACE` (0x08) | `internal/winsec/winsec_windows.go`; new `TestEvaluate_InheritOnlyAcesAreIgnored`; probe against `C:\Windows\System32\WindowsPowerShell\v1.0` |
| 5 | **MEDIUM (diagnostics)** | Windows `run()` called `config.Load` **before** `svc.Run`. A config error while running as a service exits before the SCM dispatcher starts -> SCM reports a generic "did not respond in time" (1053) instead of a clean stop with a specific code. | `run()` now enters `svc.Run` first; `config.Load` happens inside `Execute` after `StartPending`, reporting `Stopped` + `ERROR_INVALID_DATA (13)` + an Application-event-log entry + `axiom.log` line on failure. Also: a graceful-shutdown drain-timeout no longer returns a failure exit code (which could trip SCM recovery on a routine stop). | `cmd/axiom/main_windows.go`, `service_windows.go`; console-mode fail-closed still verified (`<binary> -config <bad path>` -> exit 1, clear message) |
| 6 | **LOW-MEDIUM** | `Install-Axiom.ps1` looked for the config template only at `$PSScriptRoot\..\..\configs\example-windows.yaml`, which does not exist in the flat release ZIP -> the installer never actually writes a starter config from the bundle. | also check `$PSScriptRoot\example-windows.yaml` (the bundle layout) | file diff |
| 7 | **LOW** | `Invoke-Icacls` param named `$Args` shadows the automatic variable. | renamed to `$IcaclsArgs` | file diff |
| 8 | **HIGH (blocking) -- found on real Windows Server 2022** | `Install-Axiom.ps1 Set-ProtectedAcl` combined `/inheritance:r` and `/setowner` in one `icacls` invocation. `icacls` rejects `/setowner` combined with any other operation ("Invalid parameter `/setowner`") -- it must be its own call; `/inheritance:r` + `/grant:r` *can* be combined. `Install-Axiom.ps1` failed applying ACLs on every real install. | split `Set-ProtectedAcl` into two `icacls` calls: `/setowner` alone, then `/inheritance:r /grant:r ...` together | `packaging/windows/Install-Axiom.ps1`; reproduced and fixed on `Windows Server 2022 Datacenter Evaluation` (build 20348), reported by real user testing |
| 9 | **CRITICAL (blocking, self-inflicted lockout) -- found and root-caused on real Windows Server 2022, reproduced deterministically off-host** | `Set-ProtectedAcl` applied an `(OI)(CI)` ("object inherit"/"container inherit") flagged `/grant:r` **recursively** (`icacls <dir> /inheritance:r /grant:r "SID:(OI)(CI)F" ... /T`) onto a directory that already contained real files (`axiom.exe`, copied in *before* ACLs are applied; `config.yaml`, also passed directly as a file path). `(OI)(CI)` flags mark a grant for **future inheritance by a container's children** and are meaningless on a leaf file -- `icacls` reports success ("Successfully processed N files; Failed processing 0 files") but can silently write an **empty DACL** to the file instead of the intended grant. An empty-but-present DACL denies *everyone*, including Administrators and SYSTEM -- a full lockout, not merely "too strict" (the owner's implicit WRITE_DAC is the only way back in: `icacls <path> /grant Administrators:F` still works because that right is never removable). This is what produced the persistent, non-transient, identical-every-attempt "Access is denied" on both `Copy-Item` and launching `axiom.exe` -- endpoint security was a plausible-sounding but incorrect first hypothesis (see the now-superseded original text of this row / Findings A-diagnostics work below). Reproduced off-host in an isolated temp directory with a non-admin SID: `icacls` claimed success while leaving `(Get-Acl file.exe).Access.Count -eq 0`, and the reproducing process immediately lost the ability to delete its own file. | `Set-ProtectedAcl` now applies `(OI)(CI)` grants **only to the directory itself** (no `/T`) so *future* files inherit correctly, then walks anything that **already exists** inside (`Get-ChildItem -Recurse`) and gives each item its own explicit, non-inheritance-flagged grant (`Set-LeafAcl`). Verified empirically (own-SID reproduction): the old pattern -> 0 DACL entries on the file, lockout; the new pattern -> 1 correct `FullControl` entry, file remains fully accessible. Also: every `icacls` call in `Set-ProtectedAcl`/`Set-LeafAcl` now goes through the existing retry-with-diagnostics wrapper, and `Install-Axiom.ps1` additionally strips Mark-of-the-Web (`Unblock-File`) up front and auto-collects running-process / Defender / ACL diagnostics on any remaining failure rather than requiring the operator to run commands by hand; `Uninstall-Axiom.ps1` no longer depends on launching `axiom.exe` at all (uses `sc.exe` + registry cleanup directly), so uninstall still works if a file is ever locked by something else. | `packaging/windows/Install-Axiom.ps1` (`Set-ProtectedAcl`/`Set-LeafAcl`); reproduced and fixed against real user-reported output (`Get-Acl` showing `Access:` empty, SDDL `D:PAI` with no ACEs) and independently reproduced/verified off-host |
| 9b | **HIGH (blocking, recovery path) -- found on real Windows Server 2022** | `Uninstall-Axiom.ps1` deliberately leaves `%ProgramData%\Axiom` in place on uninstall (config, certs, capabilities, audit log survive, matching `dnf remove` on Linux). Because of Finding 9, an install performed *before* that fix could leave a file (e.g. `config.yaml`) stuck with an empty DACL. On re-install, `icacls /setowner` against that pre-existing, inconsistently-ACL'd file failed with "Access is denied" even run by a genuine Administrator with `/T /C` -- `icacls`'s ownership change is itself DACL-checked and an empty DACL denies everyone, including the ownership operation. | switched the ownership step in `Set-ProtectedAcl` from `icacls /setowner` to `takeown.exe`, which forcibly reclaims ownership via implicit privilege rather than a DACL check (the correct recovery tool for exactly this state). | reproduced against real user-reported "set owner ... Access is denied" output on 12/12 retries; fixed by switching to `takeown.exe /F <path> /A` |
| 9c | **HIGH (blocking) -- found on real Windows Server 2022, immediately following 9b's fix** | The `takeown.exe /A` call from 9b always passed `/R` (recurse). `takeown.exe` hard-errors ("The specified path is not a valid directory path") every time `/R` is passed against a single **file** target -- and `config.yaml` is always called with `-Path` pointing at the file itself. Failed on 12/12 retries. | `/R` is now only added to the `takeown.exe` argument list when the target is a directory (`$isDir`, computed once via `Get-Item -Force`). | reproduced and fixed locally (own-file test harness) before shipping; confirmed on real WS2022 next run |
| 9d | **MEDIUM (blocking) -- found locally while verifying 9c, before it reached the user** | Fixing 9c by conditionally adding `/R` surfaced a second `takeown.exe` constraint: `/D <answer>` (the answer to use when an individual item in a recursive walk is itself access-denied) is only accepted **together with** `/R` ("/D should be specified only with /R"). The fix for 9c initially still passed `/D Y` unconditionally. | `/D Y` moved into the same `if ($isDir)` branch as `/R`, so a file target gets only `/F <path> /A` and a directory target gets `/F <path> /A /R /D Y`. | verified locally for both a file target and a directory target (both now fail only with the expected "not administrative privileges" message when run non-elevated -- no remaining argument-syntax errors); confirmed on real WS2022: `config.yaml`'s ACL came back correct (`NT SERVICE\axiom:(R)`, `SYSTEM:(F)`, `Administrators:(F)`) |
| 10 | **HIGH (blocking, self-inflicted -- distinct from Finding 9) -- found and root-caused on real Windows Server 2022, reproduced on a fully wiped fresh install** | `Install-Axiom.ps1` creates `certs\`, `capabilities\` and `logs\` up front (so they exist, empty, before any ACL pass runs), then calls `Set-ProtectedAcl -Path $DataDir -ServiceRights 'RX'` **before** protecting those three subdirectories with their own, *different* rights (`R`, `RX`, `M`). Because they already existed, `$DataDir`'s recursive walk (`Get-ChildItem -Recurse` -> `Set-LeafAcl`) touched them too, granting each a plain, non-inheriting `RX` leaf ACE with the **parent's** rights -- before their own correct call ran immediately after and added a *second*, `(OI)(CI)`-flagged ACE with the correct rights for that trustee. `icacls /grant:r` does not treat a plain grant and an `(OI)(CI)`-flagged grant for the same trustee as the same entry to replace, so **both survived**: e.g. `logs\` ended up with a bogus leftover `NT SERVICE\axiom:(RX)` *alongside* the correct `NT SERVICE\axiom:(OI)(CI)(M)`, and `certs\` would carry a spurious `RX` it should never have (design is `R`-only). Axiom's own startup ACL validator correctly refused to trust that inconsistent state and the service failed to start with `audit.path: directory "...\logs" grants write access to S-1-5-80-...` (that SID is `NT SERVICE\axiom`'s own service SID -- the validator was correctly rejecting the ambiguous duplicate-ACE state, not misidentifying a stranger). Reproduced deterministically: a full wipe (`Remove-Item -Recurse` on both `%ProgramFiles%\Axiom` and `%ProgramData%\Axiom`, `sc.exe delete`) followed by a single fresh `Install-Axiom.ps1` still produced the duplicate ACEs every time -- this was not leftover test-VM state. | `Set-ProtectedAcl` gained an `-Exclude` parameter (subtree paths to skip during the recursive leaf walk); the `$DataDir` call now passes `-Exclude @($CertsDir, $CapsDir, $LogsDir)` so those three subdirectories are ACL'd exactly once, by their own explicit call. `config.yaml` was not affected by this bug and needed no change: both passes that touch it use the same flat, non-inheriting grant format, so `icacls` correctly replaces rather than duplicates. | `packaging/windows/Install-Axiom.ps1` (`Set-ProtectedAcl`); reproduced on a fully wiped real WS2022 host; after the fix, a single clean install -> place certs (self-signed test CA/server pair) + capabilities -> `Install-Axiom.ps1 -Start` produced exactly one correct ACE set per path (`certs\`: `R`; `capabilities\`: `RX`; `logs\`: `M`; no duplicates anywhere) and **the `axiom` service reached `Running`** -- the first time the service was observed to actually start on real WS2022 |
| 11 | **HIGH (blocking, product-wide) -- found and fixed on a real Windows 10/11-class host, not WS2022** | `buildCommand`'s PowerShell invocation never passed `-ExecutionPolicy`, on the stated theory that Axiom "neither sets nor relies on" execution policy. That theory was correct for *security* (NTFS ACLs are the real boundary) but wrong for *product behavior*: Windows 10/11 Pro ships with the effective policy `Restricted` by default (confirmed: `LocalMachine` scope, no GPO in play), which blocks **every** local script unconditionally, signed or not -- unlike Windows Server's `RemoteSigned` default. On a real Windows 11 Pro host, every single capability invocation failed with `running scripts is disabled on this system`; the service itself still installed and started fine (`/health` still worked), so this manifested only once an operator actually tried to use an action -- a worse failure mode than an install-time error, since it looks exactly like "the product is broken" after it appeared to work. | Two changes, not one: (1) `buildCommand` now passes `-ExecutionPolicy Bypass` on its own fixed, single, ACL-protected invocation -- scoped to that one process only, calls no `Set-ExecutionPolicy`, persists nothing; safe because the script path/content integrity guarantees (server-side config, NTFS ACLs, no arbitrary args) are completely unaffected by this flag. (2) `checkExecutionPolicy` (called from `CheckPlatformPrerequisites` at startup) now checks *only* `MachinePolicy`/`UserPolicy` scope -- the one thing `Bypass` genuinely cannot override, since real Group Policy always wins over any command-line `-ExecutionPolicy` by PowerShell's own design -- and fails startup with a specific, actionable message only if a real GPO enforces `Restricted` or `AllSigned`. Local-scope policy (including Windows 10/11's `Restricted` default) is deliberately *not* a startup blocker any more: asking every customer to change their own machine's local policy just to make the product work was rejected as the wrong fix. | `internal/executor/launch_windows.go` (`buildCommand`, `checkExecutionPolicy`); reproduced the failure on a real Windows 11 Pro host (build 26100) with the machine's actual, unmodified `Restricted` local policy and zero GPO; after the fix, a full real-host re-run of the same 96-check suite that exercises `identity.probe`, `exit.fail`, `timeout.probe`, and the real unmodified `your-deploy.ps1.sample` -- all genuine PowerShell script executions through the live service -- came back **96 PASS / 0 FAIL**, with no change to local policy on that machine at any point. `go test ./...` and `GOOS=windows go vet ./...` both clean after the change; `TestBuildCommand_Windows_FixedInvocation` updated to assert the new, intentional argument vector. |

### Findings documented but NOT fixed (need a real host, or are a team decision)

| # | Severity | Finding | Recommended action |
|---|---|---|---|
| A | **RESOLVED -- validated on real WS2022** | Was: `Install-Axiom.ps1 Set-ProtectedAcl` runs `icacls ... /T /C /Q` recursively into `logs\`, which on an idempotent re-run contains a live, open `axiom.log`; feared this would throw a locked-file error. In practice, on real WS2022: (1) a full install -> re-install over the exact same, non-wiped `%ProgramData%\Axiom` (Finding 9b/9c/9d's leftover-state scenario) completed cleanly with no icacls lock errors; (2) with the service actually `Running` and `axiom.exe` open, re-running `Install-Axiom.ps1 -Start` correctly hit the *existing* copy-lock retry path (`Invoke-WithRetry` on `Copy-Item`, not on `icacls`) and did not corrupt state -- the service remained `Running` throughout. No icacls-on-locked-`axiom.log` failure was observed in any of the idempotent re-run scenarios actually exercised. | closing as resolved by observed behavior; `icacls` against `logs\` did not fail with the service running and writing to it in any test performed. If this resurfaces with a larger/longer-lived `axiom.log`, revisit. |
| B | UNKNOWN | Whether a capability running as `NT SERVICE\axiom` gets a usable `%TEMP%`/`%TMP%`/`%USERPROFILE%`. `env_windows.go` passes through whatever the service context provides and has **no `TEMP` fallback**. The Linux build needed `HOME=/var/lib/axiom` after real-host validation found tools broke without it. | on the first WS2022 run, trigger a capability that calls `[IO.Path]::GetTempPath()`, `docker`, `kubectl`; if `%TEMP%` is missing/unwritable, add a fallback (e.g. `%ProgramData%\Axiom\work`) -- do **not** add it speculatively. |
| C | LOW-MEDIUM | On `Stop-Service axiom`, a running capability's process tree is killed as soon as `serve()` returns (KILL_ON_JOB_CLOSE on process exit). Linux `systemctl stop` gives running jobs up to `TimeoutStopSec` (90 s default) of grace. The docs' "same as an unexpected restart on Linux" undersells this for a *routine* stop. | either drain in-flight jobs (bounded) before returning from the service handler, or document prominently that operators must let jobs finish before `Stop-Service`. |
| D | LOW-MEDIUM | `RequireProtectedAncestry` allows the service account write on ancestor directories (Finding above, "can NT SERVICE\axiom modify..."). Mirrors Linux; relies on the installer. | verify `Install-Axiom.ps1`'s `capabilities\` / `certs\` ACLs on a real host (`icacls`), and keep the `windows-security.md` note. |
| E | LOW | The release binary was built with whatever Go is on the build machine (1.26.5 here); CI pins 1.23. `vcs.modified` stamping was unreliable in a git worktree. | `scripts/build-release.sh` should pin `GOTOOLCHAIN` (e.g. `go1.23.12`) or the release doc should require it. |
| F | LOW (build supply chain) | `golang.org/x/sys` is pinned in `go.mod`/`go.sum` but **not vendored** (repo `.gitignore`s `vendor/`). This satisfies air-gapped builds **only** if the build machine has a warmed module cache or a GOPROXY mirror. A from-scratch, no-proxy build fails. | for an air-gapped build requirement, commit `vendor/` (remove the `.gitignore` line) so the build is hermetic; otherwise document the build-machine prerequisite. Not a runtime concern -- the binary is static and self-contained. |
| G | INFO | `internal/api` end-to-end auth/authz is not re-run natively on Windows because `config.Identity.allowed` is unexported and a secured `config.Load` fixture needs elevation. The code is platform-neutral (no build tags) and fully covered on Linux. | cover it in the WS2022 validation pass with real certs. |

---

## Production decision

# APPROVED FOR PRODUCTION

Every mandatory gate from the release checklist that can be exercised
without a deployment-specific external dependency has now been run, and
passed, on a real Windows Server 2022 host: fresh offline install, protected
NTFS ACL application and its reject path, service registration under the
least-privileged virtual account, the service actually starting and serving
real traffic, mTLS end-to-end (12 cases including rejections), audit
logging, SCM auto-restart after a crash, clean-stop not auto-restarting,
bad-configuration/tampering diagnostics (4 scenarios, each failing closed
with a specific, actionable error), timeout containment, and the full
uninstall/reinstall/purge cycle including while the service is live. B1-B5
and B7 are closed with first-party evidence from this host; B2 closes the
validation-discovered defects found along the way. See §Blocking items for
the evidence trail of each.

The one item that is not, and cannot be, closed by this host alone is **B6**:
validating a Docker- or Kubernetes-backed capability's *success* path
requires a host that actually has Docker or Kubernetes installed, which is
a property of wherever Axiom is deployed, not of Axiom itself. This host
has neither, and the test run here confirms the behavior that actually
matters in that situation -- the capability fails **closed**, reporting a
clear error, rather than hanging or lying about success. Any deployment
that intends to use a Docker/Kubernetes-backed capability should run that
specific capability once, by hand, against its real target before relying
on it -- this is a per-deployment smoke test, not a blocker on the release
itself.

### Blocking items

| # | Blocker | Evidence | Next action |
|---|---|---|---|
| B1 | **Closed.** Fresh offline install, protected-ACL application (all 7 paths), service registration, and the service reaching `Running` were already verified. This pass added: `axiom.exe` force-killed 4x, SCM restarted it every time (~6s, per the configured 3x `RESTART, delay 5000ms` policy) and the restarted service answered `/health` correctly each time; a **clean** `Stop-Service` correctly did **not** trigger a restart. | §Windows Server 2022 results ("SCM auto-restart") | none -- closed. |
| B2 | **Closed.** Fixes 1-7 were re-tested by parser/unit/probe on Windows 11 (prior pass). Fixes 8, 9, 9b, 9c, 9d and 10 were found *and* fixed *and* re-verified on the real WS2022 host this session, ending in a single clean, uninterrupted install -> cert/capability setup -> `Running` service -> uninstall-while-running cycle with zero errors. Finding A is resolved (see Findings table). | §Findings | none -- closed. |
| B3 | **Closed.** Read live from inside a real, running capability triggered through the API (not inferred): `whoami` -> `nt service\axiom`; not an Administrator; session 0; token lacks `SeDebugPrivilege`/`SeTakeOwnershipPrivilege`/`SeBackupPrivilege`/`SeRestorePrivilege`/`SeLoadDriverPrivilege`/`SeTcbPrivilege`/`SeAssignPrimaryTokenPrivilege`/`SeSecurityPrivilege`/`SeSystemEnvironmentPrivilege` (holds only the standard `SeImpersonatePrivilege`/`SeChangeNotifyPrivilege`/`SeCreateGlobalPrivilege`); has a usable `%TEMP%` (Finding B closed) and `%USERPROFILE%`; cannot write `capabilities\`, `%ProgramData%\Axiom` (incl. `config.yaml`), `certs\`, `%ProgramFiles%\Axiom`, or `C:\Windows`; can write only `logs\` and `%TEMP%`. | §Windows Server 2022 results ("least privilege") | none -- closed. |
| B4 | **Closed.** The accept-path (correctly-installed tree starts and serves real traffic -- see B5) and the reject-path (four independent ACL/config tamper scenarios, each producing SCM exit 13 + a specific event-log entry + no restart-loop + clean recovery once fixed) are both now verified end-to-end on real WS2022, not just inferred from unit tests. | §Windows Server 2022 results ("bad configuration / tampering") | none -- closed. |
| B5 | **Closed.** 12 real HTTP cases against the live `https://localhost:8443` listener: valid client (200), no cert (401), untrusted CA (401), expired cert (401), wrong EKU (401), unlisted CN (403), listed-but-unauthorized action (403), bad/missing/undeclared parameters (400 x3), an unknown-JSON-field injection attempt (400), a deliberately-failing trigger (job `failed`, correct `exit_code`), and a succeeding trigger with logs retrieved via `GET /v1/jobs/{id}/logs`. The audit log correctly recorded every `rejected`/`accepted`/`started`/`finished` event including the authenticated CN on a 403. | §Windows Server 2022 results ("mTLS end-to-end") | none -- closed. |
| B6 | **Environment-blocked, with evidence.** This WS2022 host has no Docker daemon (confirmed: no `docker`/`kubectl` on `PATH`, no `docker` service). Triggering the real, unmodified `your-deploy.ps1.sample` capability through the live API confirmed it fails **closed**: the job completes (does not hang) and reports `failed` with the real `stderr` ("docker" not recognized), never a false success. | §Windows Server 2022 results ("Docker / Kubernetes") | Re-run the same capability on a WS2022 host with a working Docker/Kubernetes target to validate the *success* path before relying on this specific action in a production deployment that uses it. |
| B7 | **Closed.** Finding A's feared idempotent-re-run failure did not reproduce; see Findings table item A. | §Findings, item A | none -- closed. |

### Non-blocking but recommended before GA

- Finding C (aggressive job kill on `Stop-Service` vs Linux grace) -- decide
  drain-vs-document.
- Finding E (pin the release Go toolchain).
- Finding F (vendor `x/sys` for a hermetic air-gapped build, if your
  deployment requires from-scratch offline builds).
- Finding G (add a native Windows mTLS auth/authz integration test).

### What IS solid

- **Linux is not regressed** -- full suite + `-race` pass on a real Linux host,
  with explicit byte-for-byte guards on the two shared seams.
- The **PowerShell execution boundary** and **Job Object containment** behave
  exactly as designed on real Windows (dev host): fixed invocation, no
  arbitrary argv/source/script/executable, parameters inert as data,
  process-tree kill on timeout and on Axiom's death.
- The **release artifact** is self-contained, offline, static, secret-free.
- The **winsec** decision logic is sound and now handles real-world ACLs
  (post-fix), verified against real system files.

B1-B5 and B7 are closed. B6 is environment-blocked in the sense explained
above and is not a reason to withhold approval of the release itself; it is
a per-deployment check for anyone using Docker/Kubernetes-backed
capabilities. Axiom may be connected to a CI controller or a production
network on Windows Server 2022.
