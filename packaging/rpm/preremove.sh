#!/bin/sh
# %preun — RPM passes $1 == 0 on a genuine final removal, and $1 >= 1 when
# this is really an upgrade (the new version's %pre/%post run right after
# this). Only stop/disable the service on a real removal — an upgrade
# should leave a running instance running (it keeps executing the old
# binary's already-open inode until something restarts it; the new binary
# just waits on disk until the operator chooses to `systemctl restart`,
# same non-disruptive default scripts/install.sh already has for
# upgrades).
set -e

if [ "$1" = "0" ]; then
	systemctl stop axiom >/dev/null 2>&1 || true
	systemctl disable axiom >/dev/null 2>&1 || true
fi

exit 0
