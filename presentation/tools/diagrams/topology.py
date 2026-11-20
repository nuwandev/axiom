from ax import Svg, write

W, H = 1580, 1180
OUT = "topology.svg"
s = Svg("topo", W, H, "Illustrative banking deployment topology for Axiom",
        "CI/CD zone with Jenkins and registry, a perimeter firewall allowing TCP 8443 with mutual TLS passed through unterminated, "
        "and a bank network zone with web, application and database tiers. An Axiom agent runs independently on each managed host. "
        "Customer application traffic, local action execution, audit forwarding and certificate issuance are drawn as separate paths.")

# ---- zones ---------------------------------------------------------------
s.comment("zones")
s.rect(20, 64, 450, 980, "ax-zone", 10)
s.text(40, 92, "CI/CD ENVIRONMENT", "ax-th")
s.text(40, 108, "engineering network zone", "ax-ts")
s.rect(630, 64, 930, 980, "ax-zone", 10)
s.text(650, 92, "BANK NETWORK ZONE", "ax-th")
s.text(650, 108, "production security boundary", "ax-ts")

# firewall wall
s.rect(543, 64, 14, 980, "ax-fw", 2)
s.text(550, 740, "PERIMETER FIREWALL", "ax-fwt", "middle", ' transform="rotate(-90 550 740)"')
s.rect(472, 118, 156, 152, "ax-callout", 6)
s.lines(484, 140, [("FIREWALL RULE", "ax-th"), ("allow TCP 8443", "ax-tb"), ("executor → agents", "ax-t"),
                   ("TLS not terminated:", "ax-t"), ("L4 passthrough only", "ax-t"),
                   ("No reverse proxy", "ax-tb"), ("or TLS-terminating", "ax-tb"), ("WAF in this path", "ax-tb")], 17)

# ---- CI/CD zone ----------------------------------------------------------
s.comment("ci/cd")
s.rect(40, 112, 110, 52, "ax-box", 6)
s.text(95, 143, "Developer", "ax-tb", "middle")
s.rect(200, 112, 250, 52, "ax-box", 6)
s.text(325, 134, "Jenkins controller", "ax-tb", "middle")
s.text(325, 152, "schedules the pipeline run", "ax-ts", "middle")
s.path("M150,138 H200", "ax-l-data", "data")
s.badge(175, 124, 1)
s.path("M325,164 V210", "ax-l-data", "data")

s.rect(40, 210, 262, 372, "ax-box ax-box-strong", 8)
s.text(56, 236, "Build executor \u00b7 pipeline run", "ax-tb")
stages = [("Source checkout", None), ("Build", None), ("Test", None), ("Publish artifact", None), ("Deployment request", "ctl")]
ys = [256, 320, 384, 448, 512]
for i, ((name, kind), y) in enumerate(zip(stages, ys)):
    cls = "ax-chip ax-chip-ctl" if kind else "ax-chip"
    s.rect(56, y, 230, 48, cls, 5)
    s.text(86, y + 29, name, "ax-tb" if kind else "ax-t")
    s.text(72, y + 29, str(i + 1), "ax-ts", "middle")
s.rect(318, 250, 132, 60, "ax-box", 6)
s.lines(384, 276, [("Source", "ax-t"), ("repository", "ax-t")], 17, "middle")
s.path("M286,280 H318", "ax-l-data", "data")
s.badge(302, 266, 2)
s.rect(318, 436, 132, 72, "ax-box", 6)
s.lines(384, 462, [("Artifact /", "ax-t"), ("image registry", "ax-t")], 17, "middle")
s.path("M286,472 H318", "ax-l-data", "data")
s.badge(302, 458, 3)

s.rect(40, 640, 262, 80, "ax-box", 6)
s.lines(56, 662, [("Jenkins credential store", "ax-tb"), ("client certificate (CN = identity)", "ax-ts"), ("private key, CA certificate", "ax-ts")], 17)
s.path("M171,640 V584", "ax-l-pki", "pki")
s.text(162, 616, "bound at the deploy step", "ax-ts", "end")
s.badge(264, 536, 4)

# legend (inside CI/CD zone, bottom)
s.comment("legend")
s.rect(40, 770, 410, 252, "ax-legend", 6)
s.text(56, 794, "LEGEND", "ax-th")
rows = [("ax-l-ctl", "ctl", "Axiom control plane \u00b7 HTTPS + mutual TLS"),
        ("ax-l-loc", "loc", "Local action execution on the host"),
        ("ax-l-data", "data", "Customer application / database traffic"),
        ("ax-l-art", "data", "Optional artifact / image pull by a script"),
        ("ax-l-aud", "aud", "Audit forwarding (Linux journald / syslog)"),
        ("ax-l-pki", "pki", "Certificate issuance (customer PKI)")]
for i, (cls, mk, label) in enumerate(rows):
    y = 820 + i * 28
    s.path(f"M56,{y} H112", cls, mk)
    s.text(124, y + 4.5, label, "ax-t")
s.rect(56, 990, 54, 22, "ax-chip ax-chip-agent", 4)
s.text(124, 1005, "Axiom component", "ax-t")
s.rect(290, 990, 54, 22, "ax-chip", 4)
s.text(356, 1005, "Customer", "ax-t")

# ---- Bank zone: customer lane -------------------------------------------
s.comment("customer lane")
s.text(1275, 112, "CUSTOMER TRAFFIC \u00b7 NOT AXIOM", "ax-th")
s.rect(1275, 124, 260, 36, "ax-box ax-box-ghost", 6)
s.text(1405, 147, "Customers / internet", "ax-t", "middle")
s.rect(1275, 180, 260, 52, "ax-box", 6)
s.text(1405, 202, "WAF / load balancer", "ax-tb", "middle")
s.text(1405, 220, "fronts the web tier only", "ax-ts", "middle")
s.path("M1405,160 V180", "ax-l-data", "data")

bands = [("WEB TIER", 130, ["Web server 01", "Web server 02"]),
         ("APPLICATION TIER", 372, ["Application server 01", "Application server 02"]),
         ("DATABASE TIER", 614, ["Database server", None])]
HOSTX = [688, 942]
BUS = 666
for bi, (label, B, hosts) in enumerate(bands):
    s.rect(650, B, 592, 216, "ax-band", 8)
    s.text(676, B + 22, label, "ax-th")
    s.text(1226, B + 22, "one agent per host", "ax-ts", "end")
    ports = []
    for hi, hname in enumerate(hosts):
        hx = HOSTX[hi]
        if bi == 2:   # database server on the right, explanatory note on the left
            hx = HOSTX[1] if hi == 0 else HOSTX[0]
        if hname is None:
            s.rect(hx, B + 62, 230, 140, "ax-note", 6)
            s.lines(hx + 14, B + 86, [("OPTIONAL AGENT", "ax-th"), ("Only when the bank needs", "ax-t"), ("local operations on the", "ax-t"),
                                       ("DB host. App-to-database", "ax-t"), ("SQL is unrelated to Axiom.", "ax-t")], 18)
            continue
        Y = B + 62
        opt = bi == 2
        s.rect(hx, Y, 230, 140, "ax-host", 6)
        s.text(hx + 14, Y + 20, hname, "ax-tb")
        if opt:
            s.text(hx + 216, Y + 20, "optional agent", "ax-ts", "end")
        s.rect(hx + 14, Y + 34, 202, 44, "ax-chip ax-chip-agent" + (" ax-dash" if opt else ""), 5)
        s.text(hx + 24, Y + 53, "Axiom agent \u00b7 :8443", "ax-tb")
        s.text(hx + 24, Y + 69, "own config, cert, allowlist", "ax-ts")
        s.rect(hx + 14, Y + 98, 98, 32, "ax-chip ax-chip-loc", 5)
        s.text(hx + 63, Y + 119, "action script", "ax-t", "middle")
        s.rect(hx + 122, Y + 98, 94, 32, "ax-chip", 5)
        s.text(hx + 169, Y + 119, "app / service", "ax-t", "middle")
        s.path(f"M{hx+63},{Y+78} V{Y+98}", "ax-l-loc", "loc")
        s.path(f"M{hx+112},{Y+114} H{hx+122}", "ax-l-loc", "loc")
        ports.append((hx + 115, Y))
        if (bi < 2 and hi == 1) or (bi == 2 and hi == 0):
            s.path(f"M{hx+216},{Y+56} H1198", "ax-l-aud")
    cy = B + 40
    if ports:
        xs = [p[0] for p in ports]
        s.path(f"M{BUS},{cy} H{max(xs)}", "ax-l-ctl")
        for (px, Y) in ports:
            s.path(f"M{px},{cy} V{Y+34}", "ax-l-ctl", "ctl")
# control bus and audit bus
s.path("M666,170 V654", "ax-l-ctl")
s.path("M1198,248 V880", "ax-l-aud", "aud")

# customer trunk (application data plane)
s.path("M1405,232 V250 H1242", "ax-l-data", "data")
s.path("M1242,300 H1405 V492 H1242", "ax-l-data", "data")
s.path("M1242,542 H1405 V734 H1242", "ax-l-data", "data")
s.text(1417, 276, "HTTPS", "ax-ts")
s.text(1417, 410, "application protocol", "ax-ts")
s.text(1417, 654, "SQL / database protocol", "ax-ts")
s.rect(1275, 800, 260, 168, "ax-note", 6)
s.lines(1289, 826, [("APPLICATION DATA PLANE", "ax-th"), ("Axiom does not proxy,", "ax-t"), ("inspect or manage this", "ax-t"),
                    ("traffic and never touches", "ax-t"), ("database connections.", "ax-t"), ("Scripts may do their own", "ax-t"),
                    ("deploys, restarts or pulls.", "ax-t")], 18)

# audit collector
s.rect(872, 880, 370, 88, "ax-box", 6)
s.lines(886, 904, [("Log collector / SIEM", "ax-tb"), ("Linux: journald / syslog forwarding", "ax-ts"), ("Windows: local audit.log only", "ax-ts")], 17)

# ---- control plane across the firewall ---------------------------------
s.comment("control plane")
s.path("M286,536 H666", "ax-l-ctl-b")
s.rect(524, 524, 52, 24, "ax-pill", 12)
s.text(550, 541, "8443", "ax-tb", "middle")
s.badge(470, 536, 5)
s.badge(610, 536, 6)
s.lines(310, 566, [("HTTPS \u00b7 mutual TLS", "ax-tb"), ("TCP 8443", "ax-tb"), ("trigger POST \u00b7 poll GET", "ax-ts"), ("no callback to Jenkins", "ax-ts")], 18)
s.badge(778, 280, 9)

# registry pull (aggregate)
s.path("M450,490 H630", "ax-l-art", "data")
s.text(476, 484, "script pull", "ax-ts")
s.text(476, 508, "(optional)", "ax-ts")

# ---- PKI strip ------------------------------------------------------------
s.rect(690, 1086, 320, 56, "ax-box", 6)
s.lines(850, 1108, [("Bank PKI / internal CA", "ax-tb"), ("customer-owned, outside Axiom", "ax-ts")], 17, "middle")
s.path("M690,1114 H245 V1044", "ax-l-pki", "pki")
s.text(262, 1106, "client certificate for the pipeline identity", "ax-ts")
s.path("M1010,1114 H1100 V1044", "ax-l-pki", "pki")
s.text(1116, 1106, "server cert + ca.crt for each agent", "ax-ts")

write(OUT, s.render())
print("ok", W, H)
