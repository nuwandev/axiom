# Validating the audit path on a real Linux host

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

## 1. Host logging path

```bash
logger -t axiom-audit '{"event":"validation-probe"}'
journalctl -t axiom-audit --since "5 minutes ago" -o verbose | grep -E 'MESSAGE=|_EXE=|_UID='
```

The probe must appear. Record which daemon owns `/dev/log`
(`ls -l /dev/log`; `systemctl status systemd-journald`, `rsyslog`).

## 2. Axiom's own records

With `audit.syslog: true` and the service running, trigger `hello.world`
with a valid client certificate, then:

```bash
journalctl -t axiom-audit --since "5 minutes ago" -o json | jq -r '.MESSAGE, ._EXE, ._SYSTEMD_UNIT'
```

Expect `accepted`, `started` and `finished` records as JSON, with `_EXE`
equal to `/usr/local/bin/axiom`. Confirm no record contains environment
values, job output, key or certificate material, or the value of any
parameter whose name looks secret (it must read `***redacted***`).

## 3. Forgery check

From a throwaway action script (or `sudo -u axiom`), run:

```bash
logger -t axiom-audit '{"event":"forged"}'
```

Confirm the forged entry has a different `_EXE` (the `logger` binary) than
Axiom's own records. Authoritative records are those with
`_EXE=/usr/local/bin/axiom`.

## 4. Central collector

Using your organization's approved forwarding path, confirm the collector receives
the records from step 2 and decide, with the collector's owner:

- whether `_EXE`/`_PID` (or an equivalent) reaches the collector, or is
  dropped by the transport; if dropped, the forgery in step 3 cannot be told
  apart there;
- retention, who can delete or alter records, and time synchronisation;
- the maximum message size the path accepts. Axiom caps its free-text fields
  at 512 bytes, but a record also carries every declared parameter value
  (up to 4096 bytes each); confirm a worst-case record is not dropped.

## 5. Fail-closed behaviour (non-production only)

Make the syslog socket unavailable (for example stop
`systemd-journald-dev-log.socket` on a test host), then:

- start Axiom with `audit.syslog: true`: it must refuse to start;
- with Axiom already running, trigger an action: it must be refused (HTTP
  `500`) and the action script must not run.

Restore the socket afterwards. Axiom's sink reconnects once on a failed
write; confirm triggers succeed again without a restart.

## 6. Record the result

Record the host OS and version, the Axiom version, the forwarding path, the
collector, the date and the outcome of each step in the release evidence.
