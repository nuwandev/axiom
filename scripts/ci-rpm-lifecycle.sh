#!/usr/bin/env bash
# Install / upgrade / rollback of the Axiom RPM on a RHEL-family system
# (Rocky Linux 9 container), using the real published v1.2.1 RPM as the
# baseline and the candidate RPM built by CI.
#
#   bash scripts/ci-rpm-lifecycle.sh /path/to/candidate.rpm
#
# Runs on a host with Docker; the real work happens inside the container
# (re-invoked with --inside). It exercises the RPM scriptlets (user, group,
# directory ownership/modes), real mTLS with throwaway certificates, the
# hello.world action, job polling, a v1.2.1 config under the new binary, the
# strict file-ownership refusal, and rollback to v1.2.1. systemd itself is
# NOT started (containers have no init): unit start-up remains a separate,
# manual RHEL check.
set -euo pipefail

BASELINE_VERSION="1.2.1"
BASELINE_URL="https://github.com/nuwandev/axiom/releases/download/v${BASELINE_VERSION}/axiom-${BASELINE_VERSION}-1.x86_64.rpm"
BASELINE_CFG_URL="https://raw.githubusercontent.com/nuwandev/axiom/v${BASELINE_VERSION}/configs/example.yaml"

if [[ "${1:-}" != "--inside" ]]; then
  candidate="${1:?usage: ci-rpm-lifecycle.sh /path/to/candidate.rpm}"
  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT
  cp "$candidate" "$work/candidate.rpm"
  curl -fsSL -o "$work/baseline.rpm" "$BASELINE_URL"
  curl -fsSL -o "$work/baseline-config.yaml" "$BASELINE_CFG_URL"
  docker run --rm \
    -v "$work":/work \
    -v "$repo_root/packaging/rpm/axiom-signing-public.asc":/work/signing-public.asc:ro \
    -v "$repo_root/scripts/ci-rpm-lifecycle.sh":/work/ci-rpm-lifecycle.sh:ro \
    rockylinux:9 bash /work/ci-rpm-lifecycle.sh --inside
  exit $?
fi

# ------------------------------------------------------------------ inside
cd /work
fails=0
ok()  { echo "  ok:   $*"; }
bad() { echo "  FAIL: $*"; fails=$((fails + 1)); }
expect() { # expect "desc" expected actual
  if [[ "$2" == "$3" ]]; then ok "$1 ($3)"; else bad "$1: expected '$2', got '$3'"; fi
}

# systemd is an RPM dependency (installed, not running: a container has no init).
dnf -y -q install systemd openssl procps-ng >/dev/null
cat /etc/os-release | sed -n 's/^PRETTY_NAME=//p'

echo "== baseline RPM v${BASELINE_VERSION}: signature and install"
rpm --import signing-public.asc
out="$(rpm -K baseline.rpm)"; echo "$out"
grep -q "digests signatures OK" <<<"$out" && ok "v${BASELINE_VERSION} RPM signature verifies against the committed public key" || bad "baseline RPM signature"
rpm -ivh baseline.rpm >/dev/null
expect "axiom user shell" /usr/sbin/nologin "$(getent passwd axiom | cut -d: -f7)"
expect "/etc/axiom"            "root:axiom 750"  "$(stat -c '%U:%G %a' /etc/axiom)"
expect "/etc/axiom/certs"      "root:axiom 750"  "$(stat -c '%U:%G %a' /etc/axiom/certs)"
expect "/opt/axiom/actions"    "root:axiom 750"  "$(stat -c '%U:%G %a' /opt/axiom/actions)"
expect "/var/log/axiom"        "axiom:axiom 750" "$(stat -c '%U:%G %a' /var/log/axiom)"
expect "hello-world.sh"        "root:axiom 750"  "$(stat -c '%U:%G %a' /opt/axiom/actions/hello-world.sh)"
if systemd-analyze verify /etc/systemd/system/axiom.service 2>&1 | tee /tmp/verify.out | grep -q .; then
  # verify prints only problems: any output is reported, but only an error line fails the check
  grep -qiE "error|failed|not found|invalid" /tmp/verify.out && bad "systemd-analyze verify: $(head -3 /tmp/verify.out)" || ok "systemd-analyze verify reports only warnings: $(head -1 /tmp/verify.out)"
else
  ok "systemd-analyze verify: unit is clean"
fi

echo "== configuration (v${BASELINE_VERSION} example config, unchanged apart from nothing)"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 -subj "/CN=ci-ca" \
  -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign" -keyout ca.key -out ca.crt 2>/dev/null
issue() { # name cn eku
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj "/CN=$2" -keyout "$1.key" -out "$1.csr" 2>/dev/null
  printf 'subjectAltName=IP:127.0.0.1\nextendedKeyUsage=%s\nkeyUsage=digitalSignature\n' "$3" > "$1.ext"
  openssl x509 -req -in "$1.csr" -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -extfile "$1.ext" -out "$1.crt" 2>/dev/null
}
issue server axiom-server serverAuth
issue client example-ci clientAuth
install -o root -g axiom -m 0640 ca.crt     /etc/axiom/certs/ca.crt
install -o root -g axiom -m 0640 server.crt /etc/axiom/certs/server.crt
install -o root -g axiom -m 0640 server.key /etc/axiom/certs/server.key
install -o root -g axiom -m 0640 baseline-config.yaml /etc/axiom/config.yaml

B=https://127.0.0.1:8443
cc() { curl -s -o /tmp/body -w '%{http_code}' --cacert ca.crt --cert client.crt --key client.key "$@"; }
PID=""
start() {
  : > /tmp/axiom.out
  runuser -u axiom -- env HOME=/var/lib/axiom /usr/local/bin/axiom -config /etc/axiom/config.yaml > /tmp/axiom.out 2>&1 &
  PID=$!
  for _ in $(seq 1 50); do
    [[ "$(cc "$B/health")" == 200 ]] && return 0
    kill -0 "$PID" 2>/dev/null || return 1
    sleep 0.2
  done
  return 1
}
stop() { [[ -n "$PID" ]] && kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null || true; PID=""; }
flow() { # trigger hello.world, poll to completion, read logs
  [[ "$(cc -X POST "$B/v1/actions/hello.world")" == 202 ]] || return 1
  local job; job="$(sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p' /tmp/body)"; [[ -n "$job" ]] || return 1
  local st=""
  for _ in $(seq 1 50); do cc "$B/v1/jobs/$job" >/dev/null; st="$(sed -n 's/.*"status":"\([^"]*\)".*/\1/p' /tmp/body)"; [[ "$st" == succeeded ]] && break; sleep 0.2; done
  [[ "$st" == succeeded ]] || return 1
  [[ "$(cc "$B/v1/jobs/$job/logs")" == 200 ]]
}

echo "== v${BASELINE_VERSION} runs (baseline behaviour)"
if start; then ok "v${BASELINE_VERSION} starts as the axiom user with the RPM layout"; flow && ok "hello.world end to end on v${BASELINE_VERSION}" || bad "baseline flow"; else bad "v${BASELINE_VERSION} did not start: $(cat /tmp/axiom.out)"; fi
stop

echo "== UPGRADE to the candidate RPM"
rpm -Uvh candidate.rpm >/dev/null
rpm -q axiom
expect "config preserved across upgrade" "yes" "$(cmp -s baseline-config.yaml /etc/axiom/config.yaml && echo yes || echo no)"
expect "/opt/axiom/actions after upgrade" "root:axiom 750" "$(stat -c '%U:%G %a' /opt/axiom/actions)"
expect "/var/log/axiom after upgrade"     "axiom:axiom 750" "$(stat -c '%U:%G %a' /var/log/axiom)"
if start; then ok "candidate starts with the unchanged v${BASELINE_VERSION} config as user axiom"; flow && ok "hello.world end to end on the candidate" || bad "candidate flow"; else bad "candidate did not start: $(cat /tmp/axiom.out)"; fi
curl -s -o /dev/null --cacert ca.crt "$B/health" 2>/dev/null && bad "request without a client certificate was served" || ok "no client certificate: TLS handshake refused"
cc "$B/v1/jobs/01ARZ3NDEKTSV4RRFFQ69G5FAV" >/dev/null; expect "unknown job id" 404 "$(cc "$B/v1/jobs/01ARZ3NDEKTSV4RRFFQ69G5FAV")"
stop
grep -q '"event":"finished"' /var/log/axiom/audit.log && ok "audit log written by the axiom user" || bad "no audit records"

echo "== strict ownership: a script owned by the service account is refused"
chown axiom:axiom /opt/axiom/actions/hello-world.sh
if start; then bad "candidate started with a service-owned script"; stop; else ok "refused: $(grep -o 'owned by the Axiom service account[^;]*' /tmp/axiom.out | head -1)"; fi
chown root:axiom /opt/axiom/actions/hello-world.sh
echo "== strict key mode: a world-readable server key is refused"
chmod 0644 /etc/axiom/certs/server.key
if start; then bad "candidate started with a world-readable key"; stop; else ok "refused: $(grep -o 'accessible by other users' /tmp/axiom.out | head -1)"; fi
chmod 0640 /etc/axiom/certs/server.key
start && ok "restored layout starts again" || bad "restored layout did not start"
stop

echo "== candidate config with v2.0.0-only keys is refused by v${BASELINE_VERSION} (rollback prerequisite)"
cp /etc/axiom/config.yaml config.v1
if grep -q '^jobs:' config.v1; then
  sed 's/^jobs:/jobs:\n  max_concurrent: 8/' config.v1 > config.v2
else
  { cat config.v1; printf '\njobs:\n  max_concurrent: 8\n'; } > config.v2
fi
install -o root -g axiom -m 0640 config.v2 /etc/axiom/config.yaml

echo "== ROLLBACK to v${BASELINE_VERSION}"
rpm -Uvh --oldpackage baseline.rpm >/dev/null
rpm -q axiom
if start; then bad "v${BASELINE_VERSION} accepted a config with jobs.max_concurrent"; stop; else ok "v${BASELINE_VERSION} refuses the v2.0.0 config key: $(grep -o 'field max_concurrent not found[^ ]*' /tmp/axiom.out | head -1)"; fi
install -o root -g axiom -m 0640 config.v1 /etc/axiom/config.yaml
if start; then ok "v${BASELINE_VERSION} starts again after the config key is removed"; flow && ok "hello.world end to end after rollback" || bad "rollback flow"; else bad "rollback start failed: $(cat /tmp/axiom.out)"; fi
stop

echo
if [[ $fails -eq 0 ]]; then echo "RPM LIFECYCLE RESULT: all checks passed"; else echo "RPM LIFECYCLE RESULT: $fails failure(s)"; exit 1; fi
