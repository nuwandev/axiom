#!/usr/bin/env bash
# Repeatable release build: linux/amd64 and linux/arm64 only (the only
# architectures actually built and tested for this project — no fake
# targets). Run from the repository root.
#
# Usage: VERSION=v1.0.0 ./scripts/build-release.sh
set -euo pipefail

VERSION="${VERSION:?set VERSION, e.g. VERSION=v1.0.0}"
OUT_DIR="dist/${VERSION}"
COMMIT="$(git rev-parse --short HEAD)"

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

for GOARCH in amd64 arm64; do
  NAME="axiom-${VERSION}-linux-${GOARCH}"
  echo "building ${NAME}..."
  CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build \
    -trimpath \
    -ldflags "-s -w -X github.com/nuwandev/axiom/internal/api.Version=${VERSION#v} -X main.commit=${COMMIT}" \
    -o "${OUT_DIR}/${NAME}" \
    ./cmd/axiom
done

echo "generating checksums..."
(
  cd "$OUT_DIR"
  sha256sum axiom-* > SHA256SUMS
)

echo
echo "release artifacts in ${OUT_DIR}:"
ls -la "$OUT_DIR"
echo
cat "${OUT_DIR}/SHA256SUMS"
