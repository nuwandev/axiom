#!/usr/bin/env bash
# Idempotent installation script for the Axiom agent on RHEL-family systems
# (RHEL, Rocky, AlmaLinux, CentOS Stream — anything with systemd + useradd).
#
# What this script does:
#   - creates the dedicated, unprivileged "axiom" service account/group
#   - creates /etc/axiom, /etc/axiom/certs, /opt/axiom/actions, /var/log/axiom
#     with restrictive ownership/permissions
#   - installs the axiom binary to /usr/local/bin/axiom
#   - installs the systemd unit and reloads systemd
#
# What this script deliberately does NOT do:
#   - generate, download, or embed any certificate or private key material.
#     Certificates are security-sensitive and environment-specific; provision
#     them out of band (your internal CA / PKI process) and place them at
#     /etc/axiom/certs/{ca,server}.{crt,key} before starting the service.
#   - write or modify /etc/axiom/config.yaml if one already exists.
#   - start the service. Review the installed config and certs first, then
#     `systemctl enable --now axiom` yourself (see docs/INSTALL.md).
#
# Safe to re-run: every step below is idempotent (create-if-missing /
# fix-permissions-if-present), and it never overwrites an existing config or
# certificate.
set -euo pipefail

AXIOM_USER="${AXIOM_USER:-axiom}"
AXIOM_GROUP="${AXIOM_GROUP:-axiom}"
BIN_SRC="${BIN_SRC:-./axiom}"
BIN_DEST="/usr/local/bin/axiom"
ETC_DIR="/etc/axiom"
CERTS_DIR="${ETC_DIR}/certs"
ACTIONS_DIR="/opt/axiom/actions"
LOG_DIR="/var/log/axiom"
STATE_DIR="/var/lib/axiom" # reserved for future use; not written to by v1
SYSTEMD_UNIT_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/packaging/axiom.service"
SYSTEMD_UNIT_DEST="/etc/systemd/system/axiom.service"

log() { printf '[install] %s\n' "$1"; }
fail() { printf '[install] ERROR: %s\n' "$1" >&2; exit 1; }

# --- Preconditions ----------------------------------------------------------

if [[ "$(id -u)" -ne 0 ]]; then
	fail "must be run as root (it creates a system account and writes under /etc, /opt, /var, /usr/local/bin)"
fi

if ! command -v systemctl >/dev/null 2>&1; then
	fail "systemctl not found; this script targets systemd-based RHEL-family systems"
fi

if [[ ! -f "$BIN_SRC" ]]; then
	fail "axiom binary not found at '$BIN_SRC' (set BIN_SRC=/path/to/axiom to override)"
fi

if [[ ! -f "$SYSTEMD_UNIT_SRC" ]]; then
	fail "systemd unit not found at '$SYSTEMD_UNIT_SRC'"
fi

# --- Service account ---------------------------------------------------------

if ! getent group "$AXIOM_GROUP" >/dev/null 2>&1; then
	log "creating group '$AXIOM_GROUP'"
	groupadd --system "$AXIOM_GROUP"
else
	log "group '$AXIOM_GROUP' already exists"
fi

if ! id "$AXIOM_USER" >/dev/null 2>&1; then
	log "creating system user '$AXIOM_USER' (no shell, no login, no home directory)"
	useradd --system --no-create-home --shell /usr/sbin/nologin \
		--gid "$AXIOM_GROUP" --comment "Axiom automation agent" "$AXIOM_USER"
else
	log "user '$AXIOM_USER' already exists"
fi

# --- Filesystem layout --------------------------------------------------------
# Ownership/permission model: config, certs, and action scripts are owned by
# root and NOT writable by the axiom account — the service can read and
# execute them, never modify them. Only the audit log directory is owned by
# and writable by axiom.

install -d -o root -g "$AXIOM_GROUP" -m 0750 "$ETC_DIR"
install -d -o root -g "$AXIOM_GROUP" -m 0750 "$CERTS_DIR"
install -d -o root -g "$AXIOM_GROUP" -m 0750 "$ACTIONS_DIR"
install -d -o "$AXIOM_USER" -g "$AXIOM_GROUP" -m 0750 "$LOG_DIR"
install -d -o "$AXIOM_USER" -g "$AXIOM_GROUP" -m 0750 "$STATE_DIR"

log "filesystem layout ready:"
log "  $ETC_DIR        (root:$AXIOM_GROUP, 0750) — config"
log "  $CERTS_DIR  (root:$AXIOM_GROUP, 0750) — mTLS material (place manually)"
log "  $ACTIONS_DIR       (root:$AXIOM_GROUP, 0750) — action scripts (place manually)"
log "  $LOG_DIR        ($AXIOM_USER:$AXIOM_GROUP, 0750) — audit log"
log "  $STATE_DIR        ($AXIOM_USER:$AXIOM_GROUP, 0750) — reserved, unused in v1"

# --- Binary ------------------------------------------------------------------

install -o root -g root -m 0755 "$BIN_SRC" "$BIN_DEST"
log "installed binary to $BIN_DEST"

# --- systemd unit --------------------------------------------------------------

install -o root -g root -m 0644 "$SYSTEMD_UNIT_SRC" "$SYSTEMD_UNIT_DEST"
systemctl daemon-reload
log "installed systemd unit to $SYSTEMD_UNIT_DEST and reloaded systemd"

# --- Fail-safe checks on what the operator still needs to provide ------------

missing=0
for f in "$CERTS_DIR/ca.crt" "$CERTS_DIR/server.crt" "$CERTS_DIR/server.key"; do
	if [[ ! -f "$f" ]]; then
		log "NOTE: $f does not exist yet — place your mTLS material there before starting axiom"
		missing=1
	fi
done
if [[ ! -f "$ETC_DIR/config.yaml" ]]; then
	log "NOTE: $ETC_DIR/config.yaml does not exist yet — see configs/example.yaml and docs/INSTALL.md"
	missing=1
fi

echo
log "install step complete."
if [[ "$missing" -eq 1 ]]; then
	log "Axiom is NOT ready to start: certificate and/or config material is still missing (see NOTE lines above)."
else
	log "Config and certificate files are present. Review them, then run:"
	log "    systemctl enable --now axiom"
	log "    systemctl status axiom"
	log "    journalctl -u axiom -n 50 --no-pager"
fi
