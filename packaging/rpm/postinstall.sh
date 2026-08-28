#!/bin/sh
# %post — runs after package files (binary, systemd unit) are laid down,
# on both a fresh install ($1 == 1) and an upgrade ($1 >= 2). Creates the
# same filesystem layout as scripts/install.sh, with the same ownership
# model: config/certs/actions are root-owned and read-only to the axiom
# account; only the log and state directories are axiom-writable.
#
# Deliberately does NOT start or enable the service, and does NOT touch
# config.yaml or any certificate material — same reasoning as
# scripts/install.sh: this account needs certs and a config placed by the
# operator before it's safe to run, and this script has no way to know
# whether that's already been done.
set -e

AXIOM_USER="axiom"
AXIOM_GROUP="axiom"
ETC_DIR="/etc/axiom"
CERTS_DIR="$ETC_DIR/certs"
ACTIONS_DIR="/opt/axiom/actions"
LOG_DIR="/var/log/axiom"
STATE_DIR="/var/lib/axiom"

install -d -o root -g "$AXIOM_GROUP" -m 0750 "$ETC_DIR"
install -d -o root -g "$AXIOM_GROUP" -m 0750 "$CERTS_DIR"
install -d -o root -g "$AXIOM_GROUP" -m 0750 "$ACTIONS_DIR"
install -d -o "$AXIOM_USER" -g "$AXIOM_GROUP" -m 0750 "$LOG_DIR"
install -d -o "$AXIOM_USER" -g "$AXIOM_GROUP" -m 0750 "$STATE_DIR"

systemctl daemon-reload >/dev/null 2>&1 || true

# Only on a genuinely fresh install ($1 == 1) — an upgrade shouldn't repeat
# first-run guidance the operator has already acted on.
if [ "$1" = "1" ]; then
	echo
	echo "axiom installed. Filesystem layout ready:"
	echo "  $ETC_DIR        (root:$AXIOM_GROUP, 0750) - config"
	echo "  $CERTS_DIR  (root:$AXIOM_GROUP, 0750) - mTLS material (place manually)"
	echo "  $ACTIONS_DIR       (root:$AXIOM_GROUP, 0750) - action scripts (place manually)"
	echo "  $LOG_DIR        ($AXIOM_USER:$AXIOM_GROUP, 0750) - audit log"
	echo "  $STATE_DIR        ($AXIOM_USER:$AXIOM_GROUP, 0750) - \$HOME for the axiom account"
	echo
	missing=0
	for f in "$CERTS_DIR/ca.crt" "$CERTS_DIR/server.crt" "$CERTS_DIR/server.key"; do
		if [ ! -f "$f" ]; then
			echo "NOTE: $f does not exist yet - place your mTLS material there before starting axiom"
			missing=1
		fi
	done
	if [ ! -f "$ETC_DIR/config.yaml" ]; then
		echo "NOTE: $ETC_DIR/config.yaml does not exist yet - see /usr/share/doc/axiom or docs/INSTALL.md"
		missing=1
	fi
	if [ "$missing" = "1" ]; then
		echo "axiom is NOT ready to start: certificate and/or config material is still missing (see NOTE lines above)."
	else
		echo "Config and certificate files are present. Review them, then run:"
		echo "    systemctl enable --now axiom"
	fi
fi

exit 0
