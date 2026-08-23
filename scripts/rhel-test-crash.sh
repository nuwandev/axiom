#!/bin/bash
# Runs INSIDE axiom-rhel. Simulates a real crash (SIGKILL to axiom's main
# PID, not a graceful stop) while a job with a leaked grandchild is
# in-flight, and verifies: (1) the direct child dies instantly via
# Pdeathsig even though it ignores SIGTERM, (2) systemd's own
# KillMode=control-group cleans up the grandchild that Pdeathsig alone
# cannot reach, (3) systemd auto-restarts axiom (Restart=on-failure),
# (4) the service is healthy again afterward, (5) the audit log shows the
# in-flight job's accepted/started records but no finished record --
# exactly the documented restart-loses-in-flight-job-tracking behavior.
set -u
cd /root/axiom-test
C="--cacert ca.crt --cert client.crt --key client.key"
B=https://127.0.0.1:8443
job_id() { sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p'; }

rm -f /var/lib/axiom/leaked-child-marker 2>/dev/null

MAIN_PID=$(systemctl show axiom -p MainPID --value)
echo "axiom main pid before: $MAIN_PID"

echo "=== triggering test.hang-child (spawns a grandchild) ==="
R=$(curl -s $C -X POST "$B/v1/actions/test.hang-child")
JOB=$(echo "$R" | job_id)
echo "job: $JOB"
sleep 1

DIRECT_CHILD_PID=$(pgrep -f 'test.hang-child.sh' | head -1)
GRANDCHILD_PID=$(pgrep -f 'sleep 60' | head -1)
echo "direct child pid: $DIRECT_CHILD_PID  grandchild pid: $GRANDCHILD_PID"

echo "=== simulating a crash: SIGKILL directly to axiom's main pid ==="
kill -9 "$MAIN_PID"
sleep 0.3

echo "direct child alive right after crash? $(kill -0 $DIRECT_CHILD_PID 2>/dev/null && echo yes || echo no)"
echo "  (expect: no -- Pdeathsig should have killed it within milliseconds)"

echo "grandchild alive right after crash (before systemd's stop sequence completes)? $(kill -0 $GRANDCHILD_PID 2>/dev/null && echo yes || echo no)"

echo "=== waiting for systemd to notice, run its stop/cleanup sequence, and auto-restart ==="
for i in $(seq 1 20); do
  STATE=$(systemctl is-active axiom 2>/dev/null)
  [ "$STATE" = "active" ] && break
  sleep 0.5
done
systemctl status axiom --no-pager | head -8

echo "grandchild alive after systemd restart settled? $(kill -0 $GRANDCHILD_PID 2>/dev/null && echo yes || echo no)"
echo "  (expect: no -- systemd's KillMode=control-group cleans the whole cgroup on unit restart)"

echo "=== health check after auto-restart ==="
curl -s $C -o /dev/null -w '%{http_code}\n' "$B/health"

echo "=== audit record for the interrupted job (expect accepted+started, no finished) ==="
grep "\"job_id\":\"$JOB\"" /var/log/axiom/audit.log

echo "=== job status via API after restart (expect 404 -- in-memory history lost) ==="
curl -s $C -o /dev/null -w '%{http_code}\n' "$B/v1/jobs/$JOB"

echo "=== leaked-child-marker should NOT exist (grandchild never reached the touch) ==="
[ -f /var/lib/axiom/leaked-child-marker ] && echo "FAIL: marker exists" || echo "PASS: no marker"
