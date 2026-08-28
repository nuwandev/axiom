#!/usr/bin/env bash
# Repeatable release build: linux/amd64 and linux/arm64 only (the only
# architectures actually built and tested for this project — no fake
# targets). Also packages each architecture as an RPM (RHEL/Rocky/Alma/
# CentOS Stream — the only OS family this project targets) if `nfpm` and
# `envsubst` are available; otherwise skips RPM packaging with a warning
# and still produces the raw binaries, so this script keeps working for
# anyone who hasn't set up RPM tooling. Run from the repository root.
#
# Usage: VERSION=v1.0.0 ./scripts/build-release.sh
set -euo pipefail

VERSION="${VERSION:?set VERSION, e.g. VERSION=v1.0.0}"
OUT_DIR="dist/${VERSION}"
COMMIT="$(git rev-parse --short HEAD)"
RPM_VERSION="${VERSION#v}"   # RPM version fields don't use a leading "v"
REPO_ROOT="$(pwd)"

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

NFPM_BIN="${NFPM_BIN:-nfpm}"
BUILD_RPMS=1
if ! command -v "$NFPM_BIN" >/dev/null 2>&1; then
  echo "WARNING: '$NFPM_BIN' not found — skipping RPM packages (binaries/checksums still built)."
  echo "  install: download the prebuilt binary from"
  echo "  https://github.com/goreleaser/nfpm/releases/latest (nfpm_*_Linux_x86_64.tar.gz)"
  echo "  and put it on PATH. Avoid 'go install .../nfpm@latest' here — nfpm's"
  echo "  own go.mod can require a newer Go toolchain than this project's (it did,"
  echo "  once, in this project's CI); the prebuilt binary sidesteps that entirely."
  BUILD_RPMS=0
elif ! command -v envsubst >/dev/null 2>&1; then
  echo "WARNING: 'envsubst' not found — skipping RPM packages (binaries/checksums still built)."
  echo "  it's part of gettext; on RHEL-family: dnf install gettext"
  BUILD_RPMS=0
fi

for GOARCH in amd64 arm64; do
  NAME="axiom-${VERSION}-linux-${GOARCH}"
  echo "building ${NAME}..."
  CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build \
    -trimpath \
    -ldflags "-s -w -X github.com/nuwandev/axiom/internal/api.Version=${VERSION#v} -X main.commit=${COMMIT}" \
    -o "${OUT_DIR}/${NAME}" \
    ./cmd/axiom

  if [[ "$BUILD_RPMS" -eq 1 ]]; then
    echo "packaging ${NAME} as an RPM..."
    GENERATED_SPEC="$(mktemp)"
    VERSION="$RPM_VERSION" GOARCH="$GOARCH" BIN_PATH="${REPO_ROOT}/${OUT_DIR}/${NAME}" \
      envsubst '${VERSION} ${GOARCH} ${BIN_PATH}' < packaging/rpm/nfpm.yaml.tmpl > "$GENERATED_SPEC"
    (cd packaging/rpm && "$NFPM_BIN" package --config "$GENERATED_SPEC" --target "${REPO_ROOT}/${OUT_DIR}/" --packager rpm)
    rm -f "$GENERATED_SPEC"
  fi
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
