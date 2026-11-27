from ax import Svg, write

W = 1340
OUT = "platforms.svg"

rows = [
    ("Service manager",
     ("systemd unit, User=axiom, Restart=on-failure", "install does not start it: enable --now is an operator step", "packaging/axiom.service"),
     ("SCM service \"axiom\", automatic start, 3 restarts at 5 s", "config.Load runs after StartPending: a bad config exits 13", "cmd/axiom/service_windows.go"), None),
    ("Service identity",
     ("dedicated axiom system user, no login shell, home /var/lib/axiom", "CapabilityBoundingSet empty, NoNewPrivileges=true", "preinstall.sh, packaging/axiom.service"),
     ("virtual account NT SERVICE\\axiom, no password", "not Administrators or SYSTEM; holds SeImpersonatePrivilege", "service_windows.go, validation report"), None),
    ("File integrity, startup and reload",
     ("root-owned, no group or world write, no symlinks, every ancestor", "private key at most 0640; otherwise start or reload is refused", "config/script_security_unix.go"),
     ("owner Administrators / SYSTEM / TrustedInstaller; no write DACL", "to anyone else, service account included; reparse points refused", "internal/winsec/winsec_windows.go"), None),
    ("How a script is launched",
     ("direct exec of the configured script, never through a shell", "no argument or executable field exists in the Spec", "executor/launch_unix.go"),
     ("fixed powershell.exe -NoProfile -NonInteractive", "-ExecutionPolicy Bypass -File <script.ps1>; interpreter ACL-checked", "executor/launch_windows.go"), None),
    ("Environment",
     ("PATH fixed to system directories, nothing inherited", "plus AXIOM_JOB_ID, AXIOM_ACTION, AXIOM_PARAM_<NAME>", "jobs/env_unix.go"),
     ("allowlist of system variables (SystemRoot, Path, TEMP ...)", "plus AXIOM_JOB_ID, AXIOM_ACTION, AXIOM_PARAM_<NAME>", "jobs/env_windows.go"), None),
    ("Containment and stop",
     ("own process group; SIGTERM to the group, 5 s grace, SIGKILL", "the script gets a bounded chance to clean up", "executor/process_unix.go"),
     ("Job Object; terminate equals kill, no cooperative grace", "the whole tree dies at once", "executor/process_windows.go"), None),
    ("If Axiom itself dies",
     ("Pdeathsig=SIGKILL reaches the direct child; systemd kills", "the rest of the control group when the unit stops", "process_unix.go, axiom.service"),
     ("the job handle closes with the process (KILL_ON_JOB_CLOSE)", "so every descendant is torn down", "executor/process_windows.go"), None),
    ("Sandbox",
     ("ProtectSystem=strict, ProtectHome, PrivateTmp, restricted address", "families; writable: /var/log/axiom and /var/lib/axiom only", "packaging/axiom.service"),
     ("NTFS ACLs: service reads and executes binary and scripts,", "reads config and certs; modify only on logs\\ and its own temp", "packaging/windows/README.md"), None),
    ("Audit storage",
     ("audit.log (JSONL, fsync) plus optional journald / syslog sink", "the central collector is the authoritative copy", "audit/syslog_unix.go"),
     ("audit.log (JSONL, fsync) plus optional Application event log sink", "events 101 to 106; the collector is the authoritative copy", "audit/eventlog_windows.go"), None),
    ("Packaging and trust",
     ("RPM (nfpm) and install.sh; RPMs and SHA256SUMS GPG-signed", "SELinux enforcing validated on one Rocky Linux 9.8 host", "packaging/rpm, scripts/sign-release.sh"),
     ("MSI (WiX) or zip with Install-Axiom.ps1; SHA256SUMS signed", "binaries and MSI are not Authenticode-signed", "packaging/windows, README.md"), "w"),
]

ROW0, STRIDE, RH = 92, 90, 82
H = ROW0 + STRIDE * len(rows) + 20
s = Svg("plat", W, H, "Linux and Windows implementations side by side",
        "Ten implementation concerns compared for the Linux (systemd) and Windows (service) builds, each with its source reference. "
        "The only concern where Windows trails Linux is binary signing: the exe and MSI are not Authenticode-signed.")

LX, LW = 236, 538
WX, WW = 790, 530
s.rect(20, 14, 1300, 62, "ax-hdrfill", 8)
s.text(40, 52, "CONCERN", "ax-th")
s.text(LX + 16, 44, "LINUX", "ax-tb")
s.text(LX + 16, 62, "systemd, RHEL-family; the primary target", "ax-ts")
s.text(WX + 16, 44, "WINDOWS", "ax-tb")
s.text(WX + 16, 62, "Server 2022, Windows 10/11 Pro; validated on real hosts", "ax-ts")

for i, (concern, lin, win, flag) in enumerate(rows):
    y = ROW0 + i * STRIDE
    s.text(40, y + 28, concern, "ax-tb")
    s.text(40, y + 47, f"{i+1:02d}", "ax-tm")
    s.rect(LX, y, LW, RH, "ax-box", 6)
    s.text(LX + 16, y + 26, lin[0], "ax-t")
    s.text(LX + 16, y + 46, lin[1], "ax-ts")
    s.text(LX + 16, y + 68, lin[2], "ax-tm")
    s.rect(WX, y, WW, RH, "ax-box ax-box-warn" if flag == "w" else "ax-box", 6)
    s.text(WX + 16, y + 26, win[0], "ax-t")
    s.text(WX + 16, y + 46, win[1], "ax-ts")
    s.text(WX + 16, y + 68, win[2], "ax-tm")
    if i < len(rows) - 1:
        s.path(f"M20,{y+STRIDE-4} H1320", "ax-rowline")

write(OUT, s.render())
print("ok", W, H)
