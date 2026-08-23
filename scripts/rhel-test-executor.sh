#!/bin/bash
# Runs INSIDE the axiom-rhel test container. Exercises executor/API
# scenarios against the real running axiom.service on real RHEL.
set -u
cd /root/axiom-test
C="--cacert ca.crt --cert client.crt --key client.key"
B=https://127.0.0.1:8443

job_id() { sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p'; }

echo "=== successful action ==="
R=$(curl -s $C -X POST "$B/v1/actions/test.echo")
echo "$R"
JOB=$(echo "$R" | job_id)
sleep 1
curl -s $C "$B/v1/jobs/$JOB"; echo

echo "=== non-zero exit action ==="
R=$(curl -s $C -X POST "$B/v1/actions/test.fail")
JOB=$(echo "$R" | job_id)
sleep 1
curl -s $C "$B/v1/jobs/$JOB"; echo
curl -s $C "$B/v1/jobs/$JOB/logs"; echo

echo "=== undeclared parameter rejected ==="
curl -s -o /dev/null -w '%{http_code}\n' $C -X POST "$B/v1/actions/test.echo" \
  -H 'Content-Type: application/json' -d '{"parameters":{"bogus":"x"}}'

echo "=== shell-metacharacter parameter value rejected ==="
curl -s -o /dev/null -w '%{http_code}\n' $C -X POST "$B/v1/actions/test.param" \
  -H 'Content-Type: application/json' -d '{"parameters":{"image_tag":"; rm -rf / #"}}'

echo "=== valid parameter passed through to env ==="
R=$(curl -s $C -X POST "$B/v1/actions/test.param" \
  -H 'Content-Type: application/json' -d '{"parameters":{"image_tag":"uat-20260823-abc123"}}')
JOB=$(echo "$R" | job_id)
sleep 1
curl -s $C "$B/v1/jobs/$JOB/logs"; echo

echo "=== timeout + SIGTERM-ignoring script escalates to SIGKILL ==="
START=$(date +%s)
R=$(curl -s $C -X POST "$B/v1/actions/test.hang")
JOB=$(echo "$R" | job_id)
sleep 8
END=$(date +%s)
curl -s $C "$B/v1/jobs/$JOB"; echo
echo "elapsed: $((END-START))s (expect ~timeout(2s)+grace(5s)=~7s, bounded)"

echo "=== process-GROUP kill: grandchild does not leak past timeout ==="
rm -f /var/lib/axiom/leaked-child-marker 2>/dev/null
R=$(curl -s $C -X POST "$B/v1/actions/test.hang-child")
JOB=$(echo "$R" | job_id)
sleep 8
curl -s $C "$B/v1/jobs/$JOB"; echo
sleep 2
if [ -f /var/lib/axiom/leaked-child-marker ]; then
  echo "FAIL: grandchild leaked and created the marker"
else
  echo "PASS: no leaked-child marker; process group was fully killed"
fi

echo "=== exclusive concurrency: second trigger rejected while first runs ==="
R1=$(curl -s $C -X POST "$B/v1/actions/test.slow")
JOB1=$(echo "$R1" | job_id)
sleep 0.3
CODE2=$(curl -s -o /tmp/resp2.json -w '%{http_code}' $C -X POST "$B/v1/actions/test.slow")
echo "first job: $JOB1 ; second trigger while running: HTTP $CODE2 ($(cat /tmp/resp2.json))"
sleep 4
curl -s $C "$B/v1/jobs/$JOB1"; echo
echo "=== lock released: third trigger after first finished ==="
CODE3=$(curl -s -o /tmp/resp3.json -w '%{http_code}' $C -X POST "$B/v1/actions/test.slow")
echo "third trigger after completion: HTTP $CODE3 ($(cat /tmp/resp3.json))"

echo "=== audit log tail ==="
tail -20 /var/log/axiom/audit.log
