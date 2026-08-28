#!/bin/sh
# %postun — runs after package-owned files (binary, systemd unit) are
# actually removed from disk, on a genuine final removal ($1 == 0). On an
# upgrade ($1 >= 1) the new version's files already replaced these, so
# there's nothing to reload or announce here.
#
# Deliberately does NOT remove /etc/axiom, /opt/axiom, /var/log/axiom,
# /var/lib/axiom, or the axiom user/group — same reasoning
# docs/INSTALL.md's manual uninstall section already states: config,
# certificates, and the audit trail may be needed for compliance/records,
# and deleting them is a decision for the operator to make deliberately,
# not something a package removal does silently on their behalf.
set -e

if [ "$1" = "0" ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
	echo
	echo "axiom removed. Left in place on purpose (delete manually if you don't need them):"
	echo "  /etc/axiom       - config and certificates"
	echo "  /opt/axiom       - action scripts"
	echo "  /var/log/axiom   - audit log"
	echo "  /var/lib/axiom   - service account state"
	echo "  the 'axiom' user/group"
fi

exit 0
