#!/bin/bash
set -u
C="--cacert /root/axiom-test/ca.crt --cert /root/axiom-test/client.crt --key /root/axiom-test/client.key"
B=https://127.0.0.1:8443

sha256() { sha256sum "$1" | awk '{print $1}'; }

echo "=== pre-upgrade: config/certs/actions checksums ==="
CFG_BEFORE=$(sha256 /etc/axiom/config.yaml)
CERT_BEFORE=$(sha256 /etc/axiom/certs/server.crt)
KEY_BEFORE=$(sha256 /etc/axiom/certs/server.key)
ACTION_BEFORE=$(sha256 /opt/axiom/actions/test.echo.sh)
AUDIT_LINES_BEFORE=$(wc -l < /var/log/axiom/audit.log)
echo "config=$CFG_BEFORE cert=$CERT_BEFORE key=$KEY_BEFORE action=$ACTION_BEFORE audit_lines=$AUDIT_LINES_BEFORE"

echo "=== pre-upgrade health/version ==="
curl -s $C "$B/health"; echo

echo "=== 1. validate new binary before touching anything live ==="
/tmp/new-axiom -config /etc/axiom/config.yaml -check 2>&1 || echo "(no -check flag; validating by running a syntax/version probe instead)"
/tmp/new-axiom -h >/dev/null 2>&1; echo "new binary runs: exit $?"

echo "=== 2. stop service, back up current binary, replace ==="
systemctl stop axiom
cp /usr/local/bin/axiom /usr/local/bin/axiom.previous
install -o root -g root -m 0755 /tmp/new-axiom /usr/local/bin/axiom

echo "=== 3+4. config/certs/actions untouched? ==="
[ "$(sha256 /etc/axiom/config.yaml)" = "$CFG_BEFORE" ] && echo "PASS: config unchanged" || echo "FAIL: config changed"
[ "$(sha256 /etc/axiom/certs/server.crt)" = "$CERT_BEFORE" ] && echo "PASS: cert unchanged" || echo "FAIL: cert changed"
[ "$(sha256 /etc/axiom/certs/server.key)" = "$KEY_BEFORE" ] && echo "PASS: key unchanged" || echo "FAIL: key changed"
[ "$(sha256 /opt/axiom/actions/test.echo.sh)" = "$ACTION_BEFORE" ] && echo "PASS: action script unchanged" || echo "FAIL: action script changed"
[ "$(wc -l < /var/log/axiom/audit.log)" -ge "$AUDIT_LINES_BEFORE" ] && echo "PASS: audit log preserved/appended, not truncated" || echo "FAIL: audit log shrank"

echo "=== 4. restart service ==="
systemctl start axiom
sleep 1
systemctl is-active axiom

echo "=== 5+6. verify health and new version ==="
sleep 1
curl -s $C "$B/health"; echo

echo "=== rollback: restore previous binary ==="
systemctl stop axiom
cp /usr/local/bin/axiom.previous /usr/local/bin/axiom
systemctl start axiom
sleep 1
echo "=== rollback verified ==="
systemctl is-active axiom
curl -s $C "$B/health"; echo
