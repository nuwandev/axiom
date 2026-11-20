# Validating the audit path on a real host

This page has a Linux procedure (journald) and a Windows procedure (Application
event log, at the end).

## Linux

Status: the journald path was verified on Rocky Linux 9.8 with v2.0.1:
`accepted`, `started` and `finished` records arrive in journald with
`_EXE=/usr/local/bin/axiom`, and a record forged with `logger` is
distinguishable by its sender metadata. Run the procedure below against your
own collector to confirm delivery and retention in your environment, and
record the results.

> **Local audit file: operational copy only.**
> **Central collector: authoritative audit record.**

Run this on a non-production host first. Do not run the fault-injection step
on a production host.

### 1. Host logging path

```bash
logger -t axiom-audit '{"event":"validation-probe"}'
journalctl -t axiom-audit --since "5 minutes ago" -o verbose | grep -E 'MESSAGE=|_EXE=|_UID='
```

The probe must appear. Record which daemon owns `/dev/log`
(`ls -l /dev/log`; `systemctl status systemd-journald`, `rsyslog`).

### 2. Axiom's own records

With `audit.syslog: true` and the service running, trigger `hello.world`
with a valid client certificate, then:

```bash
journalctl -t axiom-audit --since "5 minutes ago" -o json | jq -r '.MESSAGE, ._EXE, ._SYSTEMD_UNIT'
```

Expect `accepted`, `started` and `finished` records as JSON, with `_EXE`
equal to `/usr/local/bin/axiom`. Confirm no record contains environment
values, job output, key or certificate material, or the value of any
parameter whose name looks secret (it must read `***redacted***`).

### 3. Forgery check

From a throwaway action script (or `sudo -u axiom`), run:

```bash
logger -t axiom-audit '{"event":"forged"}'
```

Confirm the forged entry has a different `_EXE` (the `logger` binary) than
Axiom's own records. Authoritative records are those with
`_EXE=/usr/local/bin/axiom`.

### 4. Central collector

Using your organization's approved forwarding path, confirm the collector receives
the records from step 2 and decide, with the collector's owner:

- whether `_EXE`/`_PID` (or an equivalent) reaches the collector, or is
  dropped by the transport; if dropped, the forgery in step 3 cannot be told
  apart there;
- retention, who can delete or alter records, and time synchronisation;
- the maximum message size the path accepts. Axiom caps its free-text fields
  at 512 bytes, but a record also carries every declared parameter value
  (up to 4096 bytes each); confirm a worst-case record is not dropped.

### 5. Fail-closed behaviour (non-production only)

Make the syslog socket unavailable (for example stop
`systemd-journald-dev-log.socket` on a test host), then:

- start Axiom with `audit.syslog: true`: it must refuse to start;
- with Axiom already running, trigger an action: it must be refused (HTTP
  `500`) and the action script must not run.

Restore the socket afterwards. Axiom's sink reconnects once on a failed
write; confirm triggers succeed again without a restart.

### 6. Record the result

Record the host OS and version, the Axiom version, the forwarding path, the
collector, the date and the outcome of each step in the release evidence.

## Windows

Status: the Event Log sink is covered by unit tests and by an automated test
that registers a throwaway source on a Windows runner, writes through the real
Application event log and reads the entry back. It has **not** been run on a
Windows Server 2022 host. Run the procedure below on a non-production host
first and record the results; do not run the fault-injection step in
production.

### 1. Source and sink

With `audit.syslog: true` in `config.yaml`, start the service. It must start;
if the `axiom` event source is not registered it refuses to, naming the
installer step. Confirm the source exists:

```powershell
Test-Path 'HKLM:\SYSTEM\CurrentControlSet\Services\EventLog\Application\axiom'
```

### 2. Axiom's own records

Trigger `hello.world` with a valid client certificate, then:

```powershell
Get-WinEvent -FilterHashtable @{LogName='Application'; ProviderName='axiom'} -MaxEvents 10 |
  Select-Object Id, LevelDisplayName, TimeCreated, Message
```

Expect events 101 (`accepted`), 102 (`started`) and 103 (`finished`) as
informational entries whose message is the JSON record, and a warning with ID
104 for a rejected request (for example a certificate for an identity that is
not allowlisted). Confirm no message contains environment values, job output,
key or certificate material, or the value of a parameter whose name looks
secret (it must read `***redacted***`).

### 3. Forgery check

An entry written under the same source by another process is not
distinguishable by source (the event log has no equivalent of journald's
`_EXE`). Decide, with the collector's owner, how you will reconcile forwarded
events with the local `audit.log`.

### 4. Central collector

Using your organization's approved forwarding path (for example Windows Event
Forwarding or your logging agent), confirm the collector receives the events
from step 2, and decide with its owner: retention, who can clear or alter the
log, time synchronisation, and the maximum event size the path accepts. One
event is limited to 31,839 characters, and Axiom fails the write instead of
truncating a longer record, which refuses the action on the accept and start
paths. A record carries every declared parameter value (up to 4096 bytes
each); confirm a worst-case record is delivered.

### 5. Fail-closed behaviour (non-production only)

Unregister the source and restart the service (`Remove-Item` on the registry
key above, from an elevated shell, on a test host): the service must refuse to
start with `audit.syslog: true`. Restore it by running
`Uninstall-Axiom.ps1` and then `Install-Axiom.ps1` (configuration, certificates
and scripts are kept). With the service running, fill the Application log to its
limit with retention set to *do not overwrite* on a test host, trigger an
action, and confirm it is refused (HTTP `500`) and the script does not run.

### 6. Record the result

Record the Windows version, the Axiom version, the forwarding path, the
collector, the date and the outcome of each step in the release evidence.
