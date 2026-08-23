#!/bin/bash
set -u
cd /root/axiom-test
C="--cacert ca.crt --cert client.crt --key client.key"
B=https://127.0.0.1:8443
job_id() { sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p'; }
wait_terminal() {
  local jid=$1
  for i in $(seq 1 20); do
    S=$(curl -s $C "$B/v1/jobs/$jid")
    echo "$S" | grep -q '"status":"succeeded"\|"status":"failed"' && { echo "$S"; return; }
    sleep 1
  done
  echo "$S (did not reach terminal state)"
}

echo "=== deploy: trigger backend.deploy (pull + up -d + real health check) ==="
R=$(curl -s $C -X POST "$B/v1/actions/backend.deploy" -H 'Content-Type: application/json' -d '{"parameters":{"image_tag":"alpine"}}')
echo "$R"
JOB=$(echo "$R" | job_id)
wait_terminal "$JOB"
echo "--- logs ---"
curl -s $C "$B/v1/jobs/$JOB/logs"; echo
echo "--- app actually answering HTTP? ---"
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18080/

echo
echo "=== unhealthy deploy: health check must fail the job, not just the launch command ==="
R=$(curl -s $C -X POST "$B/v1/actions/backend.deploy-unhealthy")
JOB=$(echo "$R" | job_id)
wait_terminal "$JOB"

echo
echo "=== rollback: missing required image_tag parameter is rejected ==="
curl -s -o /dev/null -w '%{http_code}\n' $C -X POST "$B/v1/actions/backend.rollback" -H 'Content-Type: application/json' -d '{"parameters":{}}'

echo "=== rollback: parameter injection attempt rejected ==="
curl -s -o /dev/null -w '%{http_code}\n' $C -X POST "$B/v1/actions/backend.rollback" -H 'Content-Type: application/json' \
  -d '{"parameters":{"image_tag":"latest; podman rm -f axiom-test-app #"}}'

echo "=== rollback: valid parameter succeeds ==="
R=$(curl -s $C -X POST "$B/v1/actions/backend.rollback" -H 'Content-Type: application/json' -d '{"parameters":{"image_tag":"alpine"}}')
JOB=$(echo "$R" | job_id)
wait_terminal "$JOB"
curl -s $C "$B/v1/jobs/$JOB/logs"; echo

echo
echo "=== unauthorized identity cannot trigger backend.deploy ==="
curl -s -o /dev/null -w '%{http_code}\n' --cacert ca.crt --cert unauth-client.crt --key unauth-client.key \
  -X POST "$B/v1/actions/backend.deploy"

echo "=== audit trail for this run ==="
tail -25 /var/log/axiom/audit.log

podman rm -f axiom-test-app axiom-test-app-broken >/dev/null 2>&1 || true
