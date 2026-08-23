#!/bin/bash
# Runs INSIDE a systemd-enabled RHEL-family test container as root. Installs
# a realistic backend.deploy / backend.rollback action pair that mirrors the
# real deployment model (pull -> up -d -> real health check, explicit
# image_tag parameter for rollback) against a harmless, disposable stand-in
# app -- never a real deployment. Requires rhel-test-setup.sh to have been
# run first (base config/certs/test actions).
#
# Note on container engines: an earlier version of this script used podman
# (RHEL9's native, docker-CLI-shaped engine) to actually pull/run a
# throwaway container. That's the right approach on a real host, but hit
# unrelated user-namespace limitations (newuidmap) specific to running
# rootless podman nested two levels deep under a WSL2-backed test
# environment -- not an Axiom issue. This version uses a plain background
# process as the stand-in "app" instead, which still exercises everything
# Axiom itself is responsible for (launch, a REAL health check gating
# success, failure propagation, parameter handling) since Axiom has zero
# awareness of what's inside the script either way. Swap `run.sh`/
# `rollback.sh` below for real `docker compose`/`podman` commands when
# adapting this for an actual application.
set -euo pipefail

mkdir -p /opt/axiom-test-app/www
echo "ok" > /opt/axiom-test-app/www/index.html

cat > /opt/axiom-test-app/run.sh <<'EOF'
#!/bin/sh
# Stand-in for `docker compose -f ... pull && docker compose -f ... up -d`:
# launch a disposable HTTP listener, then wait for it to actually answer
# (real health check, not just "the launch command exited 0"), and only
# then report success.
set -eu
echo "pulling (simulated) image_tag=${AXIOM_PARAM_IMAGE_TAG:-latest}"
pkill -f 'axiom-test-app-server' 2>/dev/null || true
sleep 0.2
( cd /opt/axiom-test-app/www && exec -a axiom-test-app-server python3 -m http.server 18080 --bind 127.0.0.1 >/dev/null 2>&1 ) &
disown
echo "waiting for health check..."
for i in $(seq 1 15); do
  if curl -sf http://127.0.0.1:18080/ >/dev/null 2>&1; then
    echo "healthy"
    exit 0
  fi
  sleep 1
done
echo "app did not become healthy in time" >&2
exit 1
EOF

cat > /opt/axiom-test-app/run-unhealthy.sh <<'EOF'
#!/bin/sh
# Deliberately "deploys" something that never becomes reachable -- proves a
# failed health check produces a failed job, not a false success just
# because a launch command happened to exit 0.
set -u
echo "pulling (simulated) a deliberately broken build"
for i in $(seq 1 3); do
  curl -sf http://127.0.0.1:19999/ >/dev/null 2>&1 && exit 0
  sleep 1
done
echo "app did not become healthy in time" >&2
exit 1
EOF

cat > /opt/axiom-test-app/rollback.sh <<'EOF'
#!/bin/sh
set -eu
: "${AXIOM_PARAM_IMAGE_TAG:?AXIOM_PARAM_IMAGE_TAG is required}"
echo "rolling back to image_tag=${AXIOM_PARAM_IMAGE_TAG}"
pkill -f 'axiom-test-app-server' 2>/dev/null || true
sleep 0.2
( cd /opt/axiom-test-app/www && exec -a axiom-test-app-server python3 -m http.server 18080 --bind 127.0.0.1 >/dev/null 2>&1 ) &
disown
for i in $(seq 1 15); do
  curl -sf http://127.0.0.1:18080/ >/dev/null 2>&1 && { echo "healthy"; exit 0; }
  sleep 1
done
echo "rollback did not become healthy in time" >&2
exit 1
EOF

chown root:axiom /opt/axiom-test-app/run.sh /opt/axiom-test-app/run-unhealthy.sh /opt/axiom-test-app/rollback.sh
chmod 0750 /opt/axiom-test-app/run.sh /opt/axiom-test-app/run-unhealthy.sh /opt/axiom-test-app/rollback.sh

# Axiom rejects symlinks for actions.<name>.command (a real gap found and
# fixed during this validation pass -- a symlink's target directory isn't
# covered by the ancestor-directory security walk; see
# internal/config/script_security_unix.go's rejectSymlink). Install the
# scripts directly at their configured path instead of symlinking them in.
install -o root -g axiom -m 0750 /opt/axiom-test-app/run.sh /opt/axiom/actions/backend.deploy.sh
install -o root -g axiom -m 0750 /opt/axiom-test-app/run-unhealthy.sh /opt/axiom/actions/backend.deploy-unhealthy.sh
install -o root -g axiom -m 0750 /opt/axiom-test-app/rollback.sh /opt/axiom/actions/backend.rollback.sh

cat > /tmp/new-actions.yaml <<'EOF'
  backend.deploy:
    command: /opt/axiom/actions/backend.deploy.sh
    timeout: 20s
    concurrency: exclusive
    parameters:
      image_tag:
        type: string
        pattern: '^[a-zA-Z0-9._-]{1,128}$'

  backend.deploy-unhealthy:
    command: /opt/axiom/actions/backend.deploy-unhealthy.sh
    timeout: 10s
    concurrency: exclusive

  backend.rollback:
    command: /opt/axiom/actions/backend.rollback.sh
    timeout: 20s
    concurrency: exclusive
    parameters:
      image_tag:
        type: string
        pattern: '^[a-zA-Z0-9._-]{1,128}$'
        required: true
EOF

# Insert the new action definitions as children of the EXISTING top-level
# `actions:` map, immediately before the `authorization:` section starts
# (appending at end-of-file would make them new top-level keys instead,
# since YAML nesting is indentation-relative to what's still "open" at that
# point in the document, not to where a key of the same name appeared
# earlier).
awk '/^authorization:/{while((getline line < "/tmp/new-actions.yaml")>0) print line} {print}' \
  /etc/axiom/config.yaml > /tmp/config.yaml.new
mv /tmp/config.yaml.new /etc/axiom/config.yaml
chown root:axiom /etc/axiom/config.yaml
chmod 0640 /etc/axiom/config.yaml

# Append to this identity's allowlist.
sed -i '/- test.param/a\        - backend.deploy\n        - backend.deploy-unhealthy\n        - backend.rollback' /etc/axiom/config.yaml

echo "=== config around the new actions ==="
grep -A3 'backend\.' /etc/axiom/config.yaml

echo "=== script ownership/permissions ==="
ls -la /opt/axiom/actions/backend.*.sh
