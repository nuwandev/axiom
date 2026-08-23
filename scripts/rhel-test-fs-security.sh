#!/bin/bash
# Runs INSIDE axiom-rhel as root. Creates a genuine low-privilege local
# user (not axiom, not root) and confirms it cannot read/write anything it
# shouldn't, including via ancestor-directory permissions.
set -u

if ! id lowpriv >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin lowpriv
fi

run_as() { runuser -u lowpriv -- "$@"; }

echo "=== low-priv user cannot read the private key ==="
run_as cat /etc/axiom/certs/server.key >/dev/null 2>&1 && echo "FAIL: could read" || echo "PASS: denied"

echo "=== low-priv user cannot read config ==="
run_as cat /etc/axiom/config.yaml >/dev/null 2>&1 && echo "FAIL: could read" || echo "PASS: denied"

echo "=== low-priv user cannot read action scripts ==="
run_as cat /opt/axiom/actions/test.echo.sh >/dev/null 2>&1 && echo "FAIL: could read" || echo "PASS: denied"

echo "=== low-priv user cannot execute the binary as itself and then write it ==="
run_as test -w /usr/local/bin/axiom && echo "FAIL: binary writable" || echo "PASS: binary not writable"

echo "=== low-priv user cannot write into /etc/axiom (ancestor-dir check, not just files) ==="
run_as touch /etc/axiom/newfile 2>/dev/null && echo "FAIL: could create file in /etc/axiom" || echo "PASS: denied"

echo "=== low-priv user cannot write into /opt/axiom/actions ==="
run_as touch /opt/axiom/actions/evil.sh 2>/dev/null && echo "FAIL: could create file" || echo "PASS: denied"

echo "=== low-priv user cannot write into /var/log/axiom (axiom-only) ==="
run_as touch /var/log/axiom/evil.log 2>/dev/null && echo "FAIL: could create file" || echo "PASS: denied"

echo "=== axiom's OWN account cannot modify its own binary/config/certs/actions ==="
runuser -u axiom -- test -w /usr/local/bin/axiom && echo "FAIL: axiom can write its own binary" || echo "PASS: axiom cannot write its own binary"
runuser -u axiom -- test -w /etc/axiom/config.yaml && echo "FAIL: axiom can write config" || echo "PASS: axiom cannot write config"
runuser -u axiom -- test -w /etc/axiom/certs/server.key && echo "FAIL: axiom can write its own key" || echo "PASS: axiom cannot write its own key"
runuser -u axiom -- test -w /opt/axiom/actions/test.echo.sh && echo "FAIL: axiom can write action scripts" || echo "PASS: axiom cannot write action scripts"
runuser -u axiom -- test -r /etc/axiom/certs/server.key && echo "PASS: axiom CAN read its own key (needed to serve TLS)" || echo "FAIL: axiom cannot read its own key"

echo "=== axiom CAN write only where it should ==="
runuser -u axiom -- test -w /var/log/axiom && echo "PASS: axiom can write audit dir" || echo "FAIL: axiom cannot write audit dir"
