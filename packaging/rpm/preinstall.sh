#!/bin/sh
# %pre — runs before any package files are laid down, on both a fresh
# install ($1 == 1) and an upgrade ($1 >= 2). Same account-creation logic
# as scripts/install.sh, kept idempotent for the same reason: safe to
# re-run on every upgrade without side effects if the account already
# exists.
set -e

AXIOM_USER="axiom"
AXIOM_GROUP="axiom"
STATE_DIR="/var/lib/axiom"

if ! getent group "$AXIOM_GROUP" >/dev/null 2>&1; then
	groupadd --system "$AXIOM_GROUP"
fi

if ! id "$AXIOM_USER" >/dev/null 2>&1; then
	# --home-dir sets the passwd home-directory field explicitly (not just
	# $HOME in the environment) — see scripts/install.sh for why this
	# matters: some tools an action script invokes resolve "home" via the
	# user database, not the environment.
	useradd --system --no-create-home --home-dir "$STATE_DIR" --shell /usr/sbin/nologin \
		--gid "$AXIOM_GROUP" --comment "Axiom automation agent" "$AXIOM_USER"
fi

exit 0
