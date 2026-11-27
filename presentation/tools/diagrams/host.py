from ax import Svg, write

W, H = 1580, 660
OUT = "host.svg"
s = Svg("host", W, H, "Anatomy of one managed host",
        "What one Axiom agent reads, runs and writes on a managed host. Binary, configuration, certificates and scripts are read-only to the "
        "service account and verified at startup. The audit log directory is the writable area. Inbound TCP 8443 is the only network surface.")

# ---- network entry -----------------------------------------------------------------
s.rect(640, 20, 300, 56, "ax-box ax-box-ghost", 8)
s.text(790, 44, "Jenkins build executor", "ax-tb", "middle")
s.text(790, 62, "customer, outside the host", "ax-ts", "middle")
s.path("M790,76 V162", "ax-l-ctl-b", "ctl")
s.text(806, 98, "inbound TCP 8443 only", "ax-tb")
s.text(806, 116, "HTTPS, mutual TLS", "ax-ts")

# ---- read-only zone -----------------------------------------------------------------
s.rect(20, 60, 540, 560, "ax-zone", 10)
s.text(40, 86, "READ-ONLY TO THE SERVICE ACCOUNT", "ax-th")
s.text(40, 102, "checked at startup: the agent refuses to start otherwise", "ax-ts")
items = [
    ("Binary", "/usr/local/bin/axiom", "%ProgramFiles%\\Axiom\\axiom.exe", "static Go binary"),
    ("Configuration", "/etc/axiom/config.yaml", "%ProgramData%\\Axiom\\config.yaml", "identities reload; actions, limits: read once"),
    ("Certificates", "/etc/axiom/certs/*", "%ProgramData%\\Axiom\\certs\\*", "ca.crt, server.crt, server.key"),
    ("Action scripts", "/opt/axiom/actions/*.sh", "%ProgramData%\\Axiom\\capabilities\\*.ps1", "reviewed and installed by the bank"),
]
for i, (t, lp, wp, note) in enumerate(items):
    y = 118 + i * 124
    s.rect(40, y, 500, 108, "ax-box", 8)
    s.text(56, y + 26, t, "ax-tb")
    s.text(56, y + 52, "Linux", "ax-ts")
    s.text(116, y + 52, lp, "ax-tm")
    s.text(56, y + 72, "Windows", "ax-ts")
    s.text(116, y + 72, wp, "ax-tm")
    s.text(56, y + 95, note, "ax-ts")

# ---- host processes -------------------------------------------------------------------
s.rect(620, 130, 340, 490, "ax-zone", 10)
s.text(640, 150, "RUNNING ON THE HOST", "ax-th")
s.rect(640, 162, 300, 268, "ax-box ax-box-agent", 8)
s.text(656, 188, "axiom process", "ax-tb")
s.text(656, 206, "axiom user / NT SERVICE\\axiom", "ax-tm")
for i, t in enumerate(["TLS listener \u00b7 :8443", "identity and authorization", "job manager", "executor"]):
    s.rect(656, 226 + i * 46, 268, 38, "ax-chip", 5)
    s.text(672, 250 + i * 46, t, "ax-t")
s.rect(640, 452, 300, 150, "ax-box ax-box-loc", 8)
s.lines(656, 478, [("action script", "ax-tb"), ("child process, same account", "ax-t"), ("own process group / Job Object", "ax-t")], 20)
s.text(656, 574, "may touch the network and local services", "ax-ts")
s.text(656, 592, "Axiom controls the invocation only", "ax-ts")
s.path("M790,430 V452", "ax-l-loc", "loc")

# ---- writable zone ----------------------------------------------------------------------
s.rect(1040, 60, 520, 560, "ax-zone", 10)
s.text(1060, 86, "WRITABLE BY THE SERVICE ACCOUNT", "ax-th")
s.text(1060, 102, "the only places the account can change", "ax-ts")
s.rect(1060, 118, 480, 108, "ax-box", 8)
s.text(1076, 144, "Audit log", "ax-tb")
s.text(1076, 170, "Linux", "ax-ts")
s.text(1136, 170, "/var/log/axiom/audit.log", "ax-tm")
s.text(1076, 190, "Windows", "ax-ts")
s.text(1136, 190, "%ProgramData%\\Axiom\\logs\\audit.log", "ax-tm")
s.text(1076, 213, "operational copy, appended and fsynced", "ax-ts")
s.rect(1060, 242, 480, 108, "ax-box", 8)
s.text(1076, 268, "State and temp", "ax-tb")
s.text(1076, 294, "Linux", "ax-ts")
s.text(1136, 294, "/var/lib/axiom (HOME), private /tmp", "ax-tm")
s.text(1076, 314, "Windows", "ax-ts")
s.text(1136, 314, "service %TEMP%, logs\\axiom.log", "ax-tm")
s.text(1076, 337, "scripts and tools write here, nowhere else", "ax-ts")
s.rect(1060, 366, 480, 90, "ax-box ax-box-aud", 8)
s.lines(1076, 392, [("journald (Linux) / Event Log (Windows)", "ax-tb"), ("same audit JSON, handed to the system log", "ax-t")], 20)
s.rect(1060, 482, 480, 70, "ax-box ax-box-ghost", 8)
s.lines(1076, 508, [("Forwarder and central collector", "ax-tb"), ("customer-operated, outside this host", "ax-ts")], 20)
s.path("M1300,456 V482", "ax-l-aud", "aud")

# ---- arrows ---------------------------------------------------------------------------------
for i in range(3):
    s.path(f"M540,{172 + i*124} H640", "ax-l-data", "data")
s.text(562, 160, "read", "ax-ts")
s.path("M540,540 H640", "ax-l-loc", "loc")
s.text(564, 528, "executed", "ax-ts")
s.path("M940,172 H1060", "ax-l-data", "data")
s.path("M940,296 H1060", "ax-l-data", "data")
s.path("M940,410 H1060", "ax-l-aud", "aud")
s.text(968, 162, "append", "ax-ts")

write(OUT, s.render())
print("ok", W, H)
