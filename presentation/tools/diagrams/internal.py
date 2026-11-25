from ax import Svg, write

W, H = 1580, 1070
OUT = "internal.svg"
s = Svg("intl", W, H, "Inside one Axiom agent: Go packages and request path",
        "The cmd/axiom lifecycle builds and wires the TLS listener, identity middleware, handlers, job manager, executor, "
        "configuration and audit subsystems. Build-tagged unix and windows files supply the platform-specific behaviour.")


def box(x, y, w, h, title, path, lines, cls="ax-box", lh=18):
    s.rect(x, y, w, h, cls, 8)
    s.text(x + 16, y + 26, title, "ax-tb")
    s.text(x + 16, y + 45, path, "ax-tm")
    for i, ln in enumerate(lines):
        s.text(x + 16, y + 70 + i * lh, ln, "ax-t")


# ---- layer A: lifecycle ----------------------------------------------------
s.rect(20, 52, 1540, 190, "ax-zone", 10)
s.text(40, 78, "APPLICATION LIFECYCLE \u00b7 cmd/axiom", "ax-th")
entries = [
    (40, "main_unix.go \u00b7 run()", ["flags, config.Load()", "SIGINT / SIGTERM \u2192 context"]),
    (360, "main_windows.go \u00b7 run()", ["console mode: Ctrl-C \u2192 context", "service mode: runAsService()"]),
    (680, "service_windows.go", ["SCM: Execute(), Stop / Shutdown", "-install-service, recovery"]),
]
for x, t, ls in entries:
    s.rect(x, 96, 300, 104, "ax-box", 8)
    s.text(x + 16, 122, t, "ax-tb")
    for i, ln in enumerate(ls):
        s.text(x + 16, 148 + i * 20, ln, "ax-t")
    s.path(f"M{x+150},200 V214", "ax-l-data")
s.path("M190,214 H1010", "ax-l-data", "data")
s.rect(1010, 90, 530, 138, "ax-box ax-box-agent", 8)
s.text(1026, 116, "serve(ctx, cfg, logger)", "ax-tb")
s.text(1026, 134, "cmd/axiom/main.go \u00b7 one lifecycle, every entrypoint", "ax-tm")
for i, ln in enumerate(["CheckPlatformPrerequisites \u00b7 audit.Open (+ sink)",
                        "jobs.NewManager \u00b7 api.NewServer \u00b7 tls.Listen",
                        "on stop: BeginShutdown \u2192 HTTP drain (30 s)",
                        "then Manager.Shutdown (20 s) \u2192 audit.Close"]):
    s.text(1026, 158 + i * 19, ln, "ax-t")
s.path("M1275,228 V262", "ax-l-ctl", "ctl")

# ---- layer B: request path -------------------------------------------------
s.rect(20, 262, 1540, 262, "ax-zone", 10)
s.text(40, 288, "REQUEST PATH \u00b7 internal/auth, internal/api, internal/jobs", "ax-th")
s1 = (40, 304, 350, 200)
s2 = (420, 304, 350, 200)
s3 = (800, 304, 350, 200)
s4 = (1180, 304, 360, 200)
box(*s1, "TLS listener", "internal/auth/auth.go \u00b7 TLSConfig",
    ["RequireAndVerifyClientCert", "(RequestClientCert only when /health", "is opted in as anonymous)",
     "TLS 1.2+, ECDHE + AEAD suites", "X25519 / P-256 \u00b7 HTTP/1.1 only"], "ax-box ax-box-agent")
box(*s2, "Identity middleware", "internal/api/middleware.go \u00b7 withIdentity",
    ["auth.Verify on every request:", "chain to CA pool, EKU clientAuth", "identity = leaf Common Name",
     "failure: audit rejected, then 401", "no cert + anonymous /health: pass"], "ax-box ax-box-agent")
box(*s3, "Handlers", "internal/api/handlers.go \u00b7 server.go",
    ["name regex, then IsAllowed (default-deny)", "body \u2264 64 KiB, strict JSON, \u2264 64 params",
     "owner-only job status and logs", "POST /v1/actions/{action}", "GET /v1/jobs/{id}[/logs] \u00b7 GET /health"], "ax-box ax-box-agent")
box(*s4, "Job manager", "internal/jobs/manager.go \u00b7 Trigger, run",
    ["validate params, exclusive TryLock", "slot cap \u2192 429, shutdown \u2192 503",
     "accepted audit, then async run()", "started audit before executor.Run", "in-memory store, bounded history"], "ax-box ax-box-agent")
s.path("M390,404 H420", "ax-l-ctl", "ctl")
s.path("M770,404 H800", "ax-l-ctl", "ctl")
s.path("M1150,404 H1180", "ax-l-ctl", "ctl")

# ---- layer C: supporting subsystems ----------------------------------------
cfg = (40, 586, 470, 168)
aud = (560, 586, 480, 168)
exe = (1080, 586, 460, 168)
box(*cfg, "Configuration", "internal/config/config.go \u00b7 Load, validate",
    ["strict YAML, fail-closed at startup", "identities \u2192 allowlists, actions, limits",
     "parameter patterns compiled as ^(?:\u2026)$", "script, cert, key, ancestor integrity", "identities, certs reload on SIGHUP"])
box(*aud, "Audit subsystem", "internal/audit/audit.go \u00b7 Logger.Write",
    ["stamp, truncate to 512 B, redact secret names", "append JSONL line, fsync, then each Sink",
     "error returned: accepted, started fail closed", "Sink: syslog (unix), Event Log (Windows)"], "ax-box ax-box-aud")
box(*exe, "Executor", "internal/executor/executor.go \u00b7 Run",
    ["Spec: script path, env, timeout, output cap", "no argv or executable field in Spec",
     "timeout: terminate, 5 s grace, kill", "capped stdout / stderr buffers"], "ax-box ax-box-loc")

s.path("M215,586 V504", "ax-l-data", "data")
s.text(228, 552, "cert, key, CA paths", "ax-ts")
s.path("M595,504 V586", "ax-l-aud", "aud")
s.path("M930,504 V586", "ax-l-aud", "aud")
s.path("M1220,504 V552 H1000 V586", "ax-l-aud", "aud")
s.text(1010, 543, "accepted, started, finished", "ax-ts")
s.path("M1380,504 V586", "ax-l-loc", "loc")
s.text(1392, 552, "Run(ctx, Spec)", "ax-ts")

# ---- layer D: platform layer -------------------------------------------------
s.rect(20, 790, 1540, 262, "ax-zone", 10)
s.text(40, 816, "PLATFORM LAYER \u00b7 files selected by build tags (_unix.go / _windows.go)", "ax-th")
s.path("M275,754 V790", "ax-l-data", "data")
s.path("M800,754 V790", "ax-l-data", "data")
s.path("M1310,754 V790", "ax-l-data", "data")
unix = [("launch_unix.go", "direct exec of the script, no shell"),
        ("process_unix.go", "process group, Pdeathsig, SIGTERM then SIGKILL"),
        ("jobs/env_unix.go", "fixed PATH, nothing inherited"),
        ("config/script_security_unix.go", "root owner, no group/world write, no symlink"),
        ("audit/syslog_unix.go", "journald / syslog sink, facility AUTH"),
        ("cmd/axiom/main_unix.go", "signal-driven context")]
win = [("launch_windows.go", "fixed powershell.exe -File, GPO policy check"),
       ("process_windows.go", "Job Object, KILL_ON_JOB_CLOSE"),
       ("jobs/env_windows.go", "allowlisted system variables only"),
       ("winsec_windows.go", "owner, DACL and reparse-point checks"),
       ("audit/eventlog_windows.go", "Application event log sink, IDs 101-104"),
       ("service_windows.go", "SCM service, recovery, event log")]
for px, title, rows in ((40, "UNIX (Linux target)", unix), (800, "WINDOWS", win)):
    s.rect(px, 830, 740, 200, "ax-box", 8)
    s.text(px + 16, 854, title, "ax-tb")
    for i, (f, d) in enumerate(rows):
        y = 880 + i * 25
        s.text(px + 16, y, f, "ax-tmb")
        s.text(px + 290, y, d, "ax-t")

write(OUT, s.render())
print("ok", W, H)
