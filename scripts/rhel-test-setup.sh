#!/bin/bash
# Runs INSIDE the axiom-rhel test container as root. Generates a disposable
# test CA + server/client certs, a test config, and a set of test-only
# action scripts (none of which touch a real deployment) for RHEL
# validation. Not part of the shipped product.
set -euo pipefail

W=/root/axiom-test
rm -rf "$W" && mkdir -p "$W"
cd "$W"

echo "=== generating test CA ==="
openssl ecparam -genkey -name prime256v1 -out ca.key 2>/dev/null
openssl req -x509 -new -key ca.key -days 2 -out ca.crt -subj "/CN=axiom-test-ca" 2>/dev/null

echo "=== generating server cert (CN=axiom-server, SAN=localhost/127.0.0.1) ==="
openssl ecparam -genkey -name prime256v1 -out server.key 2>/dev/null
openssl req -new -key server.key -out server.csr -subj "/CN=axiom-server" 2>/dev/null
cat > san.cnf <<'EOF'
subjectAltName=IP:127.0.0.1,DNS:localhost
EOF
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -out server.crt -extfile san.cnf 2>/dev/null

echo "=== generating authorized client cert (CN=ci-jenkins) ==="
openssl ecparam -genkey -name prime256v1 -out client.key 2>/dev/null
openssl req -new -key client.key -out client.csr -subj "/CN=ci-jenkins" 2>/dev/null
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -out client.crt 2>/dev/null

echo "=== generating unauthorized-but-CA-trusted client cert (CN=someone-else) ==="
openssl ecparam -genkey -name prime256v1 -out unauth-client.key 2>/dev/null
openssl req -new -key unauth-client.key -out unauth-client.csr -subj "/CN=someone-else" 2>/dev/null
openssl x509 -req -in unauth-client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -out unauth-client.crt 2>/dev/null

echo "=== generating untrusted (self-signed, different CA) client cert (CN=ci-jenkins) ==="
openssl ecparam -genkey -name prime256v1 -out rogue-client.key 2>/dev/null
openssl req -x509 -new -key rogue-client.key -days 2 -out rogue-client.crt -subj "/CN=ci-jenkins" 2>/dev/null

echo "=== generating an EXPIRED client cert (CN=ci-jenkins, already expired) ==="
faketime -f '-3d' openssl ecparam -genkey -name prime256v1 -out expired-client.key 2>/dev/null || \
  openssl ecparam -genkey -name prime256v1 -out expired-client.key 2>/dev/null
openssl req -new -key expired-client.key -out expired-client.csr -subj "/CN=ci-jenkins" 2>/dev/null
# -not_before/-not_after in the past relative to "now": use -days -2 window via -startdate/-enddate
openssl x509 -req -in expired-client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out expired-client.crt \
  -extfile <(echo "basicConstraints=CA:FALSE") \
  -set_serial 100 \
  -days -1 2>/dev/null || echo "  (expired cert generation via -days -1 not supported by this openssl; will note as untested)"

echo "=== installing into /etc/axiom ==="
install -o root -g axiom -m 0640 ca.crt /etc/axiom/certs/ca.crt
install -o root -g axiom -m 0640 server.crt /etc/axiom/certs/server.crt
install -o root -g axiom -m 0640 server.key /etc/axiom/certs/server.key

echo "=== installing test action scripts ==="
cat > /opt/axiom/actions/test.echo.sh <<'EOF'
#!/bin/sh
set -eu
echo "ok: $AXIOM_JOB_ID $AXIOM_ACTION"
exit 0
EOF

cat > /opt/axiom/actions/test.fail.sh <<'EOF'
#!/bin/sh
echo "failing on purpose" 1>&2
exit 3
EOF

cat > /opt/axiom/actions/test.hang.sh <<'EOF'
#!/bin/sh
echo "hanging, ignoring TERM"
trap '' TERM
sleep 60
EOF

cat > /opt/axiom/actions/test.hang-child.sh <<'EOF'
#!/bin/sh
# Spawns a grandchild that would keep running if only the direct child were
# reaped -- proves process-GROUP kill, not just direct-child kill.
(sleep 60; touch /var/lib/axiom/leaked-child-marker) &
sleep 60
EOF

cat > /opt/axiom/actions/test.slow.sh <<'EOF'
#!/bin/sh
sleep 3
exit 0
EOF

cat > /opt/axiom/actions/test.param.sh <<'EOF'
#!/bin/sh
set -eu
echo "image_tag=${AXIOM_PARAM_IMAGE_TAG:-<unset>}"
exit 0
EOF

chown root:axiom /opt/axiom/actions/test.*.sh
chmod 0750 /opt/axiom/actions/test.*.sh

echo "=== writing config.yaml ==="
cat > /etc/axiom/config.yaml <<EOF
agent:
  id: rhel-test-agent
  name: RHEL Validation Agent
  listen:
    address: 0.0.0.0
    port: 8443

security:
  mtls:
    ca_file: /etc/axiom/certs/ca.crt
    cert_file: /etc/axiom/certs/server.crt
    key_file: /etc/axiom/certs/server.key

audit:
  path: /var/log/axiom/audit.log

actions:
  test.echo:
    command: /opt/axiom/actions/test.echo.sh
    timeout: 5s
    concurrency: shared

  test.fail:
    command: /opt/axiom/actions/test.fail.sh
    timeout: 5s

  test.hang:
    command: /opt/axiom/actions/test.hang.sh
    timeout: 2s

  test.hang-child:
    command: /opt/axiom/actions/test.hang-child.sh
    timeout: 2s

  test.slow:
    command: /opt/axiom/actions/test.slow.sh
    timeout: 10s
    concurrency: exclusive

  test.param:
    command: /opt/axiom/actions/test.param.sh
    timeout: 5s
    parameters:
      image_tag:
        type: string
        pattern: '^[a-zA-Z0-9._-]{1,128}\$'
        required: true

authorization:
  identities:
    ci-jenkins:
      actions:
        - test.echo
        - test.fail
        - test.hang
        - test.hang-child
        - test.slow
        - test.param
EOF
chown root:axiom /etc/axiom/config.yaml
chmod 0640 /etc/axiom/config.yaml

echo "=== done ==="
