#!/usr/bin/env bash
# Signs the release artifacts in dist/<version>/ and verifies the result.
#
#   1. GPG-signs every *.rpm (rpmsign) so `rpm -K` / dnf can check them.
#   2. Regenerates SHA256SUMS over every axiom-* file (including artifacts
#      that were produced after build-release.sh ran, such as the MSI).
#   3. Detach-signs SHA256SUMS -> SHA256SUMS.asc.
#   4. Verifies both signatures against the PUBLIC key committed in the
#      repository, so a wrong or substituted signing key fails the release.
#
# This runs in the publish job only. The build job never receives the signing
# key, so compiling code and third-party build tools cannot read it.
#
# Environment:
#   VERSION                  e.g. v2.0.0 (required)
#   GPG_KEY_ID               expected signing key ID (required)
#   AXIOM_GPG_PRIVATE_KEY    armored secret key (required when REQUIRE_SIGNING=1)
#   REQUIRE_SIGNING          1 = fail if the key is missing (set for real releases)
#
# The key is imported into a private, temporary GNUPGHOME that is removed on
# exit; it is never written to a file outside that directory.
set -euo pipefail

: "${VERSION:?VERSION is required}"
: "${GPG_KEY_ID:?GPG_KEY_ID is required}"
REQUIRE_SIGNING="${REQUIRE_SIGNING:-0}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist="${DIST_DIR:-${repo_root}/dist/${VERSION}}"
pubkey="${PUBLIC_KEY_FILE:-${repo_root}/packaging/rpm/axiom-signing-public.asc}"

[[ -d "$dist" ]] || { echo "error: $dist does not exist" >&2; exit 1; }

regenerate_checksums() {
  (cd "$dist" && rm -f SHA256SUMS SHA256SUMS.asc && sha256sum axiom-* > SHA256SUMS)
}

if [[ -z "${AXIOM_GPG_PRIVATE_KEY:-}" ]]; then
  if [[ "$REQUIRE_SIGNING" == "1" ]]; then
    echo "error: REQUIRE_SIGNING=1 but AXIOM_GPG_PRIVATE_KEY is not available; refusing to produce an unsigned release" >&2
    exit 1
  fi
  echo "WARNING: no signing key available; artifacts are NOT signed (dry run only)." >&2
  regenerate_checksums
  exit 0
fi

work="$(mktemp -d)"
export GNUPGHOME="$work/gnupg"
mkdir -m 700 "$GNUPGHOME"
cleanup() {
  gpgconf --kill gpg-agent >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

printf '%s\n' "$AXIOM_GPG_PRIVATE_KEY" | gpg --batch --quiet --import
# The long key ID is the last 16 hex digits of the fingerprint.
if ! gpg --batch --list-secret-keys --with-colons | awk -F: '$1=="fpr"{print $10}' | grep -qi "${GPG_KEY_ID}\$"; then
  echo "error: imported secret key does not match GPG_KEY_ID ${GPG_KEY_ID}" >&2
  exit 1
fi

shopt -s nullglob
rpms=("$dist"/*.rpm)
for f in "${rpms[@]}"; do
  rpmsign --define "__gpg $(command -v gpg)" --define "_gpg_name ${GPG_KEY_ID}" --addsign "$f"
done

regenerate_checksums
(cd "$dist" && gpg --batch --yes --armor --local-user "${GPG_KEY_ID}" --detach-sign -o SHA256SUMS.asc SHA256SUMS)

# Verify with the committed public key only, in a fresh keyring and a fresh
# rpm database, so success proves the published key matches what was used.
vhome="$work/verify-gnupg"
mkdir -m 700 "$vhome"
GNUPGHOME="$vhome" gpg --batch --quiet --import "$pubkey"
GNUPGHOME="$vhome" gpg --batch --verify "$dist/SHA256SUMS.asc" "$dist/SHA256SUMS"
(cd "$dist" && sha256sum -c SHA256SUMS)

if ((${#rpms[@]})); then
  rpmdb="$work/rpmdb"
  mkdir -p "$rpmdb"
  rpm --dbpath "$rpmdb" --initdb
  rpm --dbpath "$rpmdb" --import "$pubkey"
  for f in "${rpms[@]}"; do
    out="$(rpm --dbpath "$rpmdb" -K "$f")"
    echo "$out"
    grep -q "digests signatures OK" <<<"$out" || { echo "error: $f failed signature verification" >&2; exit 1; }
  done
fi

echo "release artifacts signed and verified (key ${GPG_KEY_ID})"
