#!/usr/bin/env bash
# Rewrites the release-pinned download URLs and artifact filenames in the
# docs to a given version. The only doc with version pins is
# docs/INSTALL.md §4/§6 (the copy-and-run RPM / binary / config-fetch
# blocks); getting-started.md and README.md deliberately use a `<version>`
# placeholder instead and are left alone.
#
# Run this while preparing a "Release vX.Y.Z" commit, before tagging, so
# the pinned commands point at the release being cut. build-release.sh
# calls this in --check mode and refuses to build a tagged release whose
# docs don't already match, so it can't be silently skipped — that drift
# is exactly what shipped in v1.1.0 (INSTALL.md still pointed at v1.0.1).
#
# Usage:
#   scripts/sync-doc-versions.sh v1.2.0            # rewrite in place
#   scripts/sync-doc-versions.sh --check v1.2.0    # exit 1 unless already synced
set -euo pipefail

CHECK=0
if [[ "${1:-}" == "--check" ]]; then
  CHECK=1
  shift
fi

VERSION="${1:?usage: sync-doc-versions.sh [--check] vX.Y.Z}"
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "error: version must look like v1.2.3, got: ${VERSION}" >&2
  exit 2
fi
BARE="${VERSION#v}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOC="${REPO_ROOT}/docs/INSTALL.md"

# Every pin is one of these four shapes. Keep this list in sync with the
# curl/dnf/BIN_SRC lines in docs/INSTALL.md §4 and §6.
render() {
  sed -E \
    -e "s#(releases/download/)v[0-9]+\.[0-9]+\.[0-9]+#\1${VERSION}#g" \
    -e "s#(raw\.githubusercontent\.com/nuwandev/axiom/)v[0-9]+\.[0-9]+\.[0-9]+#\1${VERSION}#g" \
    -e "s#axiom-v[0-9]+\.[0-9]+\.[0-9]+-linux-#axiom-${VERSION}-linux-#g" \
    -e "s#axiom-[0-9]+\.[0-9]+\.[0-9]+-1\.#axiom-${BARE}-1.#g" \
    "$DOC"
}

if [[ "$CHECK" -eq 1 ]]; then
  if ! diff -u "$DOC" <(render) >/tmp/sync-doc-versions.diff 2>/dev/null; then
    echo "error: ${DOC#"$REPO_ROOT/"} has release-pinned versions that don't match ${VERSION}:" >&2
    sed 's/^/  /' /tmp/sync-doc-versions.diff >&2
    echo "  fix: scripts/sync-doc-versions.sh ${VERSION}" >&2
    exit 1
  fi
  echo "${DOC#"$REPO_ROOT/"}: version pins already match ${VERSION}"
else
  rendered="$(render)"
  if [[ "$rendered" == "$(cat "$DOC")" ]]; then
    echo "${DOC#"$REPO_ROOT/"}: already at ${VERSION}, nothing to do"
  else
    printf '%s\n' "$rendered" > "$DOC"
    echo "${DOC#"$REPO_ROOT/"}: rewrote release pins to ${VERSION}"
  fi
fi
