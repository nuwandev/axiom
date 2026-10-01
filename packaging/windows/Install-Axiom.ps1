#Requires -RunAsAdministrator
<#
.SYNOPSIS
  Offline installer for the Axiom agent on Windows Server 2022.

.DESCRIPTION
  Idempotent. Creates the %ProgramFiles%\Axiom and %ProgramData%\Axiom
  layout with the NTFS ACLs Axiom's startup checks require, installs
  axiom.exe, and registers the Windows service under the virtual service
  account NT SERVICE\axiom.

  It deliberately does NOT:
    - generate, download or embed any certificate or key material;
    - overwrite an existing config.yaml;
    - start the service (pass -Start to do so once certs + config are in place).

  Requires no Internet access. Run from an elevated PowerShell in the
  directory containing axiom.exe (or pass -BinarySource).

.PARAMETER BinarySource
  Path to the axiom.exe to install. Defaults to .\axiom.exe next to this script.

.PARAMETER Start
  Start (and set to automatic) the service after installation. Only use this
  once certificates and config.yaml are already in place.
#>
[CmdletBinding()]
param(
    [string]$BinarySource = (Join-Path $PSScriptRoot 'axiom.exe'),
    [switch]$Start
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$ServiceName  = 'axiom'
$InstallDir   = Join-Path $env:ProgramFiles 'Axiom'
$DataDir      = Join-Path $env:ProgramData  'Axiom'
$CertsDir     = Join-Path $DataDir 'certs'
$CapsDir      = Join-Path $DataDir 'capabilities'
$LogsDir      = Join-Path $DataDir 'logs'
$ConfigPath   = Join-Path $DataDir 'config.yaml'
$ExePath      = Join-Path $InstallDir 'axiom.exe'

# Well-known SIDs, used instead of names so this works on non-English Windows.
$SID_ADMINS  = '*S-1-5-32-544'   # BUILTIN\Administrators
$SID_SYSTEM  = '*S-1-5-18'       # NT AUTHORITY\SYSTEM
$SID_SVC     = 'NT SERVICE\{0}' -f $ServiceName

function Write-Step($m) { Write-Host "[install] $m" }

function Write-AccessDeniedDiagnostics {
    <#
      VALIDATION-DISCOVERED: a locked/blocked axiom.exe under Program Files
      produces "Access is denied" for several different, unrelated reasons
      (a process still holding the file open, Windows Defender real-time
      scanning / Controlled Folder Access, AppLocker/WDAC, Mark-of-the-Web).
      Rather than making the operator run diagnostics by hand, gather and
      print the actual answers so the real cause is visible immediately.
    #>
    param([Parameter(Mandatory)] [string]$Path)

    Write-Host ''
    Write-Host "[install] DIAGNOSTICS for $Path -- automatically collected, no action needed to run these:"

    $procs = Get-Process -ErrorAction SilentlyContinue | Where-Object {
        $_.Path -and ($_.Path -ieq $Path -or $_.ProcessName -ieq 'axiom')
    }
    if ($procs) {
        Write-Host "  -> a process is RUNNING from this path or named 'axiom' -- this is very likely the cause:"
        $procs | Format-Table Id, ProcessName, Path -AutoSize | Out-String | Write-Host
        Write-Host "     Stop it first: Stop-Process -Id <Id> -Force   (or reboot), then re-run this script."
    } else {
        Write-Host "  - no running process matches this path or is named 'axiom'."
    }

    $svc = Get-Service -Name axiom -ErrorAction SilentlyContinue
    if ($svc) { Write-Host "  - service 'axiom' status: $($svc.Status), start type: $($svc.StartType)" }

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
        $motw = Get-Item -LiteralPath $Path -Stream Zone.Identifier -ErrorAction SilentlyContinue
        Write-Host "  - Mark-of-the-Web present: $([bool]$motw)"
        try {
            $acl = & icacls $Path 2>&1
            Write-Host '  - current icacls:'
            $acl | ForEach-Object { Write-Host "      $_" }
        } catch {}
    }

    $appLocker = Get-WinEvent -LogName 'Microsoft-Windows-AppLocker/EXE and DLL' -MaxEvents 10 -ErrorAction SilentlyContinue
    if ($appLocker) {
        Write-Host '  - recent AppLocker EXE/DLL events (may include unrelated entries):'
        $appLocker | Select-Object -First 5 TimeCreated, Id, Message | Format-List | Out-String | Write-Host
    }
    Write-Host ''
}

function Invoke-WithRetry {
    <#
      VALIDATION-DISCOVERED: a freshly-arrived, unrecognized axiom.exe can be
      held or blocked for anywhere from a fraction of a second to tens of
      seconds by Windows Defender real-time protection / Controlled Folder
      Access while it is scanned, or genuinely locked by a running process --
      both copying onto it and launching it can fail with "Access is denied"
      during that window. Retry with backoff before treating it as fatal, and
      auto-collect diagnostics (Write-AccessDeniedDiagnostics) rather than
      making the operator chase this by hand.
    #>
    param(
        [Parameter(Mandatory)] [scriptblock]$Action,
        [Parameter(Mandatory)] [string]$Description,
        [string]$DiagnosePath,
        [int]$Attempts = 12,
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
                      '(stop the process holding the file, add a narrow Defender/AppLocker exclusion for ' +
                      "'$InstallDir', or reboot if nothing above explains it) and re-run this script; it is idempotent."
            }
            Write-Step "$Description failed (attempt $i/$Attempts): $($_.Exception.Message) -- retrying in ${DelayMs}ms"
            Start-Sleep -Milliseconds $DelayMs
        }
    }
}

function Invoke-Icacls {
    param([string[]]$IcaclsArgs)
    $out = & icacls @IcaclsArgs 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "icacls $($IcaclsArgs -join ' ') failed: $out"
    }
}

function Set-ProtectedAcl {
    <#
      Reset $Path's ACL to: inheritance disabled, owner Administrators,
      Administrators + SYSTEM full control, and the service account granted
      $ServiceRights (RX for read/execute-only trees, M for writable trees,
      or "" for none). If $Path is a directory, everything already inside it
      (files and subdirectories, recursively) is reset the same way.

      VALIDATION-DISCOVERED (serious): (OI)(CI) ("object inherit"/"container
      inherit") flags mark a grant for *future* inheritance by a container's
      children -- they are meaningless on a leaf file. The previous version
      of this function applied an (OI)(CI)-flagged /grant recursively (/T)
      onto whatever already existed in the tree, including pre-existing
      files (axiom.exe is copied in before ACLs are ever applied; config.yaml
      is also passed to this function directly as a file path). Doing that
      can make icacls silently produce an EMPTY DACL on the file instead of
      the intended grant -- which denies *everyone*, including Administrators
      and SYSTEM, since an empty-but-present DACL is a default-deny, not the
      "no restriction" that a missing DACL would be. That is a full lockout,
      not merely "too strict": not even the owner's normal access works,
      though the owner's implicit WRITE_DAC still lets an admin repair it
      with a plain `icacls <path> /grant Administrators:F`.

      The fix: (OI)(CI) grants are applied only to the directory itself (so
      *new* files created later inherit correctly); anything that already
      exists inside is given its own explicit, non-inheritance-flagged grant.

      VALIDATION-DISCOVERED (2): Install-Axiom.ps1 is idempotent and
      Uninstall-Axiom.ps1 deliberately leaves %ProgramData%\Axiom in place
      (config, certs, capabilities, audit log survive an uninstall, matching
      "dnf remove" on Linux) -- so a re-install can encounter a file left
      over from an *earlier* run that ended up in a broken ACL state (for
      example, the empty-DACL defect above, from before this fix existed).
      `icacls /setowner` was found to fail with "Access is denied" against
      exactly that kind of pre-existing, inconsistently-ACL'd file, even run
      by a genuine Administrator with /T /C. `takeown.exe` is the tool
      actually built for this recovery case (forcibly reclaims ownership
      regardless of the object's current ACL state) and is used here
      instead.

      VALIDATION-DISCOVERED (3): certs/, capabilities/ and logs/ are created
      up front (so they exist, empty, before any ACL pass runs) and each
      needs DIFFERENT $ServiceRights than their parent %ProgramData%\Axiom
      (R, RX and M respectively, vs RX on the parent). Protecting the parent
      recursively walked into those already-existing, still-empty
      subdirectories and leaf-ACL'd them with the *parent's* rights, before
      their own correct call ran right after. icacls's /grant:r does not
      treat a plain grant and an (OI)(CI)-flagged grant for the same
      trustee as the same entry to replace, so both survived: e.g. logs\
      ended up with a bogus leftover `axiom:(RX)` alongside the correct
      `axiom:(OI)(CI)(M)`, and certs\ would carry a spurious RX it should
      never have (R-only). Axiom's own startup ACL validator correctly
      refused to trust that inconsistent state. Fixed by having the parent's
      pass skip any subtree that gets its own explicit Set-ProtectedAcl call.
    #>
    param(
        [Parameter(Mandatory)] [string]$Path,
        [ValidateSet('RX', 'R', 'M', '')] [string]$ServiceRights = 'RX',
        [string[]]$Exclude = @()
    )

    $isDir = (Get-Item -LiteralPath $Path -Force).PSIsContainer

    Invoke-WithRetry -Description "take ownership of $Path" -DiagnosePath $Path -Action {
        # /A: ownership goes to the Administrators group, not the invoking
        # user, so it matches $SID_ADMINS regardless of who runs the
        # installer.
        # VALIDATION-DISCOVERED: /R (recurse) is only valid when the target
        # is a directory -- takeown.exe hard-errors ("The specified path is
        # not a valid directory path") every time if passed against a single
        # file, which config.yaml (called with -Path pointing at the file
        # itself, not its directory) always is. /D (the answer to use when
        # an individual item during the recursive walk is access-denied) is
        # in turn only accepted together with /R ("/D should be specified
        # only with /R"). Both are conditional on the target actually being
        # a directory.
        $takeownArgs = @('/F', $Path, '/A')
        if ($isDir) { $takeownArgs += @('/R', '/D', 'Y') }
        $out = & takeown.exe @takeownArgs 2>&1
        if ($LASTEXITCODE -ne 0) { throw "takeown.exe exited $LASTEXITCODE`: $out" }
    }
    if ($isDir) {
        # ${var} delimiters are required: "$SID_ADMINS:..." would be parsed
        # as the scoped-variable syntax $scope:name and fail.
        $dirGrants = @("${SID_ADMINS}:(OI)(CI)F", "${SID_SYSTEM}:(OI)(CI)F")
        if ($ServiceRights -ne '') { $dirGrants += "${SID_SVC}:(OI)(CI)${ServiceRights}" }
        Invoke-WithRetry -Description "set ACL on directory $Path" -DiagnosePath $Path -Action {
            Invoke-Icacls (@($Path, '/inheritance:r', '/grant:r') + $dirGrants + @('/C', '/Q'))
        }
        $excludeNorm = $Exclude | ForEach-Object { $_.TrimEnd('\') }
        Get-ChildItem -LiteralPath $Path -Recurse -Force -ErrorAction SilentlyContinue |
            Where-Object {
                $full = $_.FullName
                -not ($excludeNorm | Where-Object {
                    $full -ieq $_ -or $full.StartsWith("$_\", [StringComparison]::OrdinalIgnoreCase)
                })
            } |
            ForEach-Object { Set-LeafAcl -Path $_.FullName -ServiceRights $ServiceRights }
    } else {
        Set-LeafAcl -Path $Path -ServiceRights $ServiceRights
    }
}

function Set-LeafAcl {
    <#
      Applies a non-inheritance-flagged grant directly to one file or
      already-existing subdirectory -- see Set-ProtectedAcl above for why
      (OI)(CI) must not be used here.
    #>
    param(
        [Parameter(Mandatory)] [string]$Path,
        [ValidateSet('RX', 'R', 'M', '')] [string]$ServiceRights = 'RX'
    )
    $grants = @("${SID_ADMINS}:F", "${SID_SYSTEM}:F")
    if ($ServiceRights -ne '') { $grants += "${SID_SVC}:${ServiceRights}" }
    Invoke-WithRetry -Description "set ACL on $Path" -DiagnosePath $Path -Action {
        Invoke-Icacls (@($Path, '/inheritance:r', '/grant:r') + $grants + @('/C', '/Q'))
    }
}

# --- preconditions --------------------------------------------------------
if (-not (Test-Path -LiteralPath $BinarySource -PathType Leaf)) {
    throw "axiom.exe not found at '$BinarySource' (use -BinarySource to point at it)"
}

# Strip Mark-of-the-Web from everything in the bundle. A zip downloaded
# through a browser (or transferred in a way Windows tags as internet-zone)
# marks every extracted file; PowerShell's RemoteSigned default policy and
# some endpoint-security products treat that as "untrusted", which can block
# both running the *.ps1 files and executing/overwriting axiom.exe. This is
# a local installer run by an administrator against files they already
# fetched deliberately -- unblocking them here is correct, not a bypass of
# anything Axiom's own security model relies on (see docs/windows-security.md
# -- execution policy / MOTW were never the boundary).
Get-ChildItem -LiteralPath $PSScriptRoot -Recurse -File -ErrorAction SilentlyContinue |
    Unblock-File -ErrorAction SilentlyContinue

# --- filesystem layout ---------------------------------------------------
Write-Step "creating layout under $InstallDir and $DataDir"
foreach ($d in @($InstallDir, $DataDir, $CertsDir, $CapsDir, $LogsDir)) {
    if (-not (Test-Path -LiteralPath $d)) { New-Item -ItemType Directory -Path $d -Force | Out-Null }
}

# --- binary ------------------------------------------------------------------
# The MSI installer places axiom.exe directly at its final path and invokes
# this script with -BinarySource pointing at that same path (so Windows
# Installer owns and tracks the file for its own uninstall/repair). Copy-Item
# onto an identical resolved path throws, so skip the copy in that case --
# the zip-based flow (BinarySource is always a different, downloaded path)
# is unaffected.
$resolvedSource = (Resolve-Path -LiteralPath $BinarySource).Path
$resolvedDest = if (Test-Path -LiteralPath $ExePath) { (Resolve-Path -LiteralPath $ExePath).Path } else { $null }
if ($resolvedSource -eq $resolvedDest) {
    Write-Step "$ExePath already in place (installed by the MSI) -- skipping copy"
} else {
    Write-Step "installing $ExePath"
    Invoke-WithRetry -Description "copy axiom.exe to $ExePath" -DiagnosePath $ExePath -Action {
        Copy-Item -LiteralPath $BinarySource -Destination $ExePath -Force
    }
}
# The copy can carry Zone.Identifier over from the source; unblock the
# installed copy too, not just the bundle it came from.
Unblock-File -LiteralPath $ExePath -ErrorAction SilentlyContinue

# --- service registration --------------------------------------------------
# axiom.exe owns the service definition (account, recovery actions, SID type).
$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($null -eq $existing) {
    Write-Step "registering the '$ServiceName' Windows service (account: $SID_SVC)"
    Invoke-WithRetry -Description "run axiom.exe -install-service" -DiagnosePath $ExePath -Action {
        & $ExePath -install-service -config $ConfigPath
        if ($LASTEXITCODE -ne 0) { throw "axiom.exe -install-service exited $LASTEXITCODE" }
    }
} else {
    Write-Step "service '$ServiceName' already registered -- leaving its definition untouched"
}

# --- ACLs (after the service exists, so NT SERVICE\axiom resolves) --------
Write-Step "applying protected NTFS ACLs"
Set-ProtectedAcl -Path $InstallDir -ServiceRights 'RX'
Set-ProtectedAcl -Path $DataDir    -ServiceRights 'RX' -Exclude @($CertsDir, $CapsDir, $LogsDir)
Set-ProtectedAcl -Path $CertsDir   -ServiceRights 'R'
Set-ProtectedAcl -Path $CapsDir    -ServiceRights 'RX'
Set-ProtectedAcl -Path $LogsDir    -ServiceRights 'M'

# --- config template (never overwrite an existing one) -------------------
if (-not (Test-Path -LiteralPath $ConfigPath)) {
    # The release bundle ships example-windows.yaml alongside this script; a
    # source checkout has it under configs/.
    $template = @(
        (Join-Path $PSScriptRoot 'example-windows.yaml'),
        (Join-Path $PSScriptRoot '..\..\configs\example-windows.yaml')
    ) | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
    if ($template) {
        Copy-Item -LiteralPath $template -Destination $ConfigPath
        Set-ProtectedAcl -Path $ConfigPath -ServiceRights 'R'
        Write-Step "wrote a starter config to $ConfigPath -- review and edit it"
    } else {
        Write-Step "NOTE: no config.yaml yet -- copy example-windows.yaml to $ConfigPath"
    }
} else {
    Set-ProtectedAcl -Path $ConfigPath -ServiceRights 'R'
}

# --- hello-world capability (auto-placed, unlike a real action you add) ---
# Any real action you configure needs its own script reviewed and placed by
# you -- Axiom has no opinion on what your actions do and ships no example
# of one. hello-world is different: it is authored and reviewed as part of
# Axiom itself, is the same fixed, side-effect-free content on every
# install (it only prints a line and exits 0), and exists purely so a fresh
# install has one safe, zero-dependency action to trigger immediately --
# see docs/getting-started.md. Forcing a manual copy/rename/review step
# onto a script that can only ever print a line would be the opposite of
# helpful, so it is the one case this installer places directly as a live
# .ps1, same as everything else it already installs on your behalf
# (axiom.exe itself included).
$helloWorldPath = Join-Path $CapsDir 'hello-world.ps1'
if (-not (Test-Path -LiteralPath $helloWorldPath)) {
    $helloWorldSource = @(
        (Join-Path $PSScriptRoot 'capability-examples\hello-world.ps1.sample'),
        (Join-Path $PSScriptRoot '..\..\scripts\examples\hello-world.ps1.sample')
    ) | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
    if ($helloWorldSource) {
        Copy-Item -LiteralPath $helloWorldSource -Destination $helloWorldPath
        Set-ProtectedAcl -Path $helloWorldPath -ServiceRights 'RX'
        Write-Step "wrote the hello-world capability to $helloWorldPath (ready to trigger immediately, see docs/getting-started.md)"
    }
}

# --- fail-safe check -------------------------------------------------------
$missing = @()
foreach ($f in @('ca.crt', 'server.crt', 'server.key')) {
    if (-not (Test-Path -LiteralPath (Join-Path $CertsDir $f))) { $missing += $f }
}
# VALIDATION-DISCOVERED: config.Load already fails startup with a precise,
# specific error naming the exact missing script when an action's command
# points at a .ps1 that was never reviewed (still sitting as the shipped
# .ps1.sample) -- that part was already correct. What was missing is this
# script's OWN pre-flight feedback: without it, running -Start in that state
# gets only the generic, unhelpful "TerminatingError(Start-Service): Failed
# to start service" from PowerShell itself, and the actual reason is visible
# only in the Application event log / axiom.log, not here where the operator
# is already looking. Catch it here too, the same way the cert check above
# already does, so -Start fails loud with the real reason before even
# attempting Start-Service.
$unreviewedSamples = @(Get-ChildItem -LiteralPath $CapsDir -Filter '*.ps1.sample' -File -ErrorAction SilentlyContinue |
    Where-Object { -not (Test-Path -LiteralPath (Join-Path $CapsDir ($_.BaseName))) })
Write-Host ''
Write-Step 'install step complete.'
if ($missing.Count -gt 0) {
    $missingList = $missing -join ', '
    Write-Step "Axiom is NOT ready to start: place your mTLS material in $CertsDir (missing: $missingList)."
    Write-Step "Also place your reviewed capability .ps1 scripts in $CapsDir, then run this script again (or icacls) so they inherit the protected ACL."
} elseif ($Start -and $unreviewedSamples.Count -gt 0) {
    $sampleNames = ($unreviewedSamples | ForEach-Object { $_.Name }) -join ', '
    Write-Step "Axiom is NOT ready to start: $CapsDir still has unreviewed sample script(s) ($sampleNames) that a configured action's command points at (by the equivalent .ps1 name, once reviewed and renamed)."
    Write-Step "Review each script, then save it without the .sample extension in the same folder, then run this script again (or icacls) so it inherits the protected ACL."
} elseif ($Start) {
    Write-Step "starting the '$ServiceName' service"
    Set-Service -Name $ServiceName -StartupType Automatic
    Start-Service -Name $ServiceName
    Get-Service -Name $ServiceName | Format-Table -AutoSize
} elseif ($unreviewedSamples.Count -gt 0) {
    $sampleNames = ($unreviewedSamples | ForEach-Object { $_.Name }) -join ', '
    Write-Step "Certificates present. $CapsDir still has unreviewed sample script(s) ($sampleNames) -- review each, save without the .sample extension, then review $ConfigPath and run:"
    Write-Step "    Set-Service -Name $ServiceName -StartupType Automatic; Start-Service -Name $ServiceName"
} else {
    Write-Step "Certificates present. Review $ConfigPath and your capability scripts, then run:"
    Write-Step "    Set-Service -Name $ServiceName -StartupType Automatic; Start-Service -Name $ServiceName"
}
