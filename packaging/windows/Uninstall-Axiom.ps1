#Requires -RunAsAdministrator
<#
.SYNOPSIS
  Remove the Axiom Windows service and binary.

.DESCRIPTION
  Stops and deletes the service and removes %ProgramFiles%\Axiom. By design
  it leaves %ProgramData%\Axiom (config, certificates, capability scripts,
  audit log) in place -- pass -PurgeData to remove that too.

  Uninstall deliberately uses only OS-native tools (sc.exe, the registry) to
  stop and remove the service, rather than invoking axiom.exe -uninstall-
  service -- VALIDATION-DISCOVERED: a freshly-arrived, unrecognized
  axiom.exe can be held or blocked by Windows Defender real-time protection
  / Controlled Folder Access long enough that launching it fails with
  "Access is denied". Uninstall must still work in that situation.
#>
[CmdletBinding()]
param([switch]$PurgeData)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$ServiceName = 'axiom'
$InstallDir  = Join-Path $env:ProgramFiles 'Axiom'
$DataDir     = Join-Path $env:ProgramData  'Axiom'

function Write-Step($m) { Write-Host "[uninstall] $m" }

function Write-AccessDeniedDiagnostics {
    <#
      VALIDATION-DISCOVERED: "Access is denied" removing files under
      %ProgramFiles%\Axiom has several different, unrelated causes (a
      process still holding a file open -- most commonly axiom.exe itself
      still running -- Windows Defender real-time scanning / Controlled
      Folder Access, or AppLocker/WDAC). Gather and print the actual
      answers instead of making the operator chase this by hand.
    #>
    param([Parameter(Mandatory)] [string]$Path)

    Write-Host ''
    Write-Host "[uninstall] DIAGNOSTICS for $Path -- automatically collected, no action needed to run these:"

    $procs = Get-Process -ErrorAction SilentlyContinue | Where-Object {
        $_.Path -and ($_.Path -ilike "$Path*" -or $_.ProcessName -ieq 'axiom')
    }
    if ($procs) {
        Write-Host "  -> a process is RUNNING from under this path or named 'axiom' -- this is very likely the cause:"
        $procs | Format-Table Id, ProcessName, Path -AutoSize | Out-String | Write-Host
        Write-Host "     Stop it first: Stop-Process -Id <Id> -Force   (or reboot), then re-run this script."
    } else {
        Write-Host "  - no running process matches this path or is named 'axiom'."
    }

    try {
        $mp = Get-MpPreference -ErrorAction Stop
        Write-Host "  - Controlled Folder Access enabled: $($mp.EnableControlledFolderAccess -ne 0)"
    } catch { Write-Host '  - Get-MpPreference unavailable (Defender module not present or access denied to query it).' }

    try {
        $threats = Get-MpThreatDetection -ErrorAction Stop | Select-Object -First 5
        if ($threats) {
            Write-Host '  - recent Defender threat detections (top 5):'
            $threats | Format-Table -AutoSize | Out-String | Write-Host
        } else {
            Write-Host '  - no recent Defender threat detections.'
        }
    } catch { Write-Host '  - Get-MpThreatDetection unavailable.' }

    if (Test-Path -LiteralPath $Path) {
        try {
            $acl = & icacls $Path 2>&1
            Write-Host '  - current icacls:'
            $acl | ForEach-Object { Write-Host "      $_" }
        } catch {}
    }
    Write-Host ''
}

function Invoke-WithRetry {
    <#
      Retries $Action a few times with backoff, then auto-collects
      diagnostics (Write-AccessDeniedDiagnostics) instead of just giving up.
      A locked/blocked file can be transient (Defender scanning a binary
      that was just running) or persistent (the process is still up); the
      diagnostics distinguish the two.
    #>
    param(
        [Parameter(Mandatory)] [scriptblock]$Action,
        [Parameter(Mandatory)] [string]$Description,
        [string]$DiagnosePath,
        [int]$Attempts = 10,
        [int]$DelayMs = 2000
    )
    for ($i = 1; $i -le $Attempts; $i++) {
        try {
            & $Action
            return
        } catch {
            if ($i -eq $Attempts) {
                if ($DiagnosePath) { Write-AccessDeniedDiagnostics -Path $DiagnosePath }
                throw "failed to $Description after $Attempts attempts over ~$([int]($Attempts * $DelayMs / 1000))s: $($_.Exception.Message)`n" +
                      'See the DIAGNOSTICS printed above for the actual cause on this machine. ' +
                      'Do not disable real-time protection to work around it -- fix the specific cause shown ' +
                      "(stop the process holding the file, add a narrow exclusion for '$InstallDir' / '$DataDir', " +
                      'or reboot if nothing above explains it) and re-run this script; it is idempotent.'
            }
            Write-Step "$Description failed (attempt $i/$Attempts): $($_.Exception.Message) -- retrying in ${DelayMs}ms"
            Start-Sleep -Milliseconds $DelayMs
        }
    }
}

# --- service ---------------------------------------------------------------
$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -ne $svc) {
    if ($svc.Status -ne 'Stopped') {
        Write-Step "stopping the '$ServiceName' service"
        try { Stop-Service -Name $ServiceName -Force -ErrorAction Stop }
        catch { Write-Step "Stop-Service failed ($($_.Exception.Message)); falling back to sc.exe stop" }
        & sc.exe stop $ServiceName | Out-Null
        Start-Sleep -Seconds 1
    }
    Write-Step "removing the '$ServiceName' service"
    & sc.exe delete $ServiceName | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Step "WARNING: 'sc.exe delete $ServiceName' exited $LASTEXITCODE -- the service definition may still be present; check 'sc.exe query $ServiceName'"
    }
} else {
    Write-Step "service '$ServiceName' is not installed"
}

# Best-effort: remove the Application event log source axiom.exe registers
# (eventlog.InstallAsEventCreate in installWindowsService). sc.exe delete
# does not touch this; a stale source is harmless but worth cleaning up.
$eventLogKey = 'HKLM:\SYSTEM\CurrentControlSet\Services\EventLog\Application\axiom'
if (Test-Path -LiteralPath $eventLogKey) {
    Remove-Item -LiteralPath $eventLogKey -Force -ErrorAction SilentlyContinue
}

# --- files -------------------------------------------------------------------
if (Test-Path -LiteralPath $InstallDir) {
    Invoke-WithRetry -Description "remove $InstallDir" -Action {
        Remove-Item -LiteralPath $InstallDir -Recurse -Force
    }
}

if ($PurgeData) {
    if (Test-Path -LiteralPath $DataDir) {
        Write-Step "removing $DataDir (config, certs, capabilities, audit log)"
        Invoke-WithRetry -Description "remove $DataDir" -Action {
            Remove-Item -LiteralPath $DataDir -Recurse -Force
        }
    }
} else {
    Write-Step "left $DataDir in place (config, certs, capabilities, audit log). Use -PurgeData to remove."
}

Write-Step 'done.'
