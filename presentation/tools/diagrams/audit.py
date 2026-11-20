from ax import Svg, write

W, H = 1580, 850
OUT = "audit.svg"
s = Svg("aud", W, H, "Audit data flow",
        "Four event types enter audit.Logger.Write, which stamps, truncates, redacts, marshals, locks, appends and fsyncs one JSON line, "
        "then hands it to any sink. The local file is an operational copy, the Linux syslog sink feeds the authoritative central collector, "
        "and Windows has no sink. Failed TLS handshakes never reach the audit log.")

# ---- events -------------------------------------------------------------------
s.text(20, 40, "EVENTS", "ax-th")
events = [("accepted", "Manager.Trigger"), ("started", "Manager.run, before exec"),
          ("finished", "Manager.run, after the outcome"), ("rejected", "withIdentity, handleTriggerAction")]
for i, (e, src) in enumerate(events):
    y = 56 + i * 76
    s.rect(20, y, 290, 60, "ax-box ax-box-aud", 6)
    s.text(36, y + 26, e, "ax-tb")
    s.text(36, y + 46, src, "ax-ts")
    s.path(f"M310,{y+30} H350", "ax-l-aud", "aud")
s.rect(20, 366, 290, 104, "ax-note", 6)
s.lines(34, 388, [("RECORD FIELDS", "ax-th"), ("timestamp, agent, event, job_id,", "ax-tm"), ("identity, action, parameters,", "ax-tm"),
                  ("result, reason, duration_ms", "ax-tm")], 19)

# ---- Logger.Write ----------------------------------------------------------------
s.rect(350, 40, 660, 430, "ax-zone", 10)
s.text(368, 68, "audit.Logger.Write(Record)", "ax-tb")
s.text(368, 86, "internal/audit/audit.go", "ax-tm")
steps = ["stamp the agent ID and a UTC timestamp",
         "truncate identity, action, reason to 512 bytes",
         "redact values of secret-named parameters",
         "marshal one JSON object",
         "take the writer mutex: records never interleave",
         "append the line to the file, then fsync",
         "hand the same JSON to every Sink"]
for i, t in enumerate(steps):
    y = 102 + i * 52
    cls = "ax-chip ax-chip-agent" if i in (5, 6) else "ax-chip"
    s.rect(368, y, 624, 42, cls, 5)
    s.text(392, y + 27, str(i + 1), "ax-tmb", "middle")
    s.text(412, y + 27, t, "ax-t")
s.text(992, 102 + 2 * 52 + 27, "token, pass, key, cert, ...", "ax-ts", "end")
s.text(992, 102 + 6 * 52 + 27, "any error is returned", "ax-ts", "end")

# ---- outputs -----------------------------------------------------------------------
s.text(1070, 40, "DESTINATIONS", "ax-th")
s.rect(1070, 56, 490, 124, "ax-box", 8)
s.lines(1086, 82, [("Local audit file", "ax-tb"), ("JSONL, O_APPEND, mode 0640", "ax-tm"),
                    ("operational copy: the account that runs", "ax-t"), ("scripts owns it and can alter it", "ax-t")], 19)
s.rect(1070, 200, 490, 150, "ax-box ax-box-aud", 8)
s.lines(1086, 226, [("Linux: syslog sink", "ax-tb"), ("facility AUTH, tag axiom-audit", "ax-tm"),
                     ("journald, then the bank's forwarder,", "ax-t"), ("then the central collector: the", "ax-t"),
                     ("authoritative, tamper-resistant copy", "ax-t")], 19)
s.rect(1070, 370, 490, 100, "ax-box ax-box-warn", 8)
s.lines(1086, 396, [("Windows: no sink", "ax-tb"), ("audit.syslog is rejected at config load;", "ax-t"),
                     ("the local file is the only audit record", "ax-t")], 19)
s.path("M1010,358 H1030 V118 H1070", "ax-l-data", "data")
s.path("M1010,410 H1050 V275 H1070", "ax-l-aud", "aud")

# ---- failure behaviour ---------------------------------------------------------------
s.text(20, 520, "IF THE WRITE OR THE FORWARD FAILS", "ax-th")
outs = [("accepted fails", ["job removed, slot and lock released", "API answers 500; the action never runs"], "ax-box ax-box-agent"),
        ("started fails", ["job marked failed: audit log unavailable", "the script is not executed"], "ax-box ax-box-agent"),
        ("finished fails", ["logged to the operational log only", "outcome already decided; record may be missing"], "ax-box"),
        ("rejected fails", ["logged to the operational log only", "the 401 or 403 is still returned"], "ax-box")]
for i, (t, ls, cls) in enumerate(outs):
    x = 20 + i * 390
    s.rect(x, 536, 370, 104, cls, 8)
    s.text(x + 16, 562, t, "ax-tb")
    for j, ln in enumerate(ls):
        s.text(x + 16, 588 + j * 20, ln, "ax-t")
s.text(20, 666, "accepted and started fail closed: an action is never run without its record having been written and handed to every sink.", "ax-ts")

# ---- what is not an audit record -----------------------------------------------------
s.text(20, 716, "NOT AN AUDIT RECORD", "ax-th")
s.rect(20, 732, 420, 76, "ax-box ax-box-warn", 8)
s.lines(36, 758, [("TLS handshake fails", "ax-tb"), ("no valid client certificate (default mode)", "ax-ts")], 20)
s.path("M440,770 H520", "ax-l-warn", "aud")
s.rect(520, 732, 480, 76, "ax-box", 8)
s.lines(536, 758, [("net/http default logger", "ax-tb"), ("\"http: TLS handshake error\" on stderr", "ax-tm")], 20)
s.path("M1000,770 H1080", "ax-l-warn", "aud")
s.rect(1080, 732, 480, 76, "ax-box ax-box-warn", 8)
s.lines(1096, 758, [("No audit record, no HTTP response", "ax-tb"), ("monitor failed auth at the network layer", "ax-ts")], 20)

write(OUT, s.render())
print("ok", W, H)
