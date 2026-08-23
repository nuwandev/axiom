# scripts/

- `install.sh` — the production installer (see [`docs/INSTALL.md`](../docs/INSTALL.md)).
- `examples/` — sample action scripts referenced from the docs; not installed by default.
- `wsl-build.sh` — dev helper for cross-building/testing from a Windows host via WSL.
- `rhel-test-*.sh` — the RHEL validation harness used to exercise a real
  install on a real RHEL-family host (see `docs/THREAT-MODEL.md` and
  `docs/INSTALL.md` for what they proved). Not part of the shipped product;
  useful for re-validating after a change to install.sh, packaging/axiom.service,
  or internal/config's security checks. Run in order on a disposable
  RHEL-family systemd host (a `--privileged --systemd=always` container
  works, e.g. `rockylinux/rockylinux:9-ubi-init`):
  1. `install.sh` (via the real installer, not one of these)
  2. `rhel-test-setup.sh` — generates a disposable test CA/certs, test-only
     action scripts, and a config
  3. `rhel-test-executor.sh` — process lifecycle: timeout, SIGTERM→grace→SIGKILL,
     cancellation, concurrency, parameter validation, audit
  4. `rhel-test-crash.sh` — kills axiom's main PID mid-job and verifies
     Pdeathsig + systemd's cgroup cleanup + auto-restart
  5. `rhel-test-fs-security.sh` — filesystem permission boundaries as a
     genuine low-privilege user and as the axiom account itself
  6. `rhel-test-docker-action-setup.sh` + `rhel-test-docker-action-run.sh` —
     a realistic deploy/rollback action pair (pull → up -d → real health
     check) against a harmless stand-in app
  7. `rhel-test-upgrade.sh` — binary upgrade/rollback without touching
     config/certs/actions/audit
