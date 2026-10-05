#!/usr/bin/env bash
# Repeatable release build:
#   - linux/amd64 and linux/arm64 (RHEL-family target), each also packaged as
#     an RPM when `nfpm` + `envsubst` are available;
#   - windows/amd64 (Windows Server 2022 / Windows 10+ Pro target), also
#     packaged as an offline install bundle (zip + Install-Axiom.ps1) when
#     `zip` is available. The MSI is built separately by scripts/build-msi.sh
#     (WiX is Windows-only); BUILD_MSI=1 runs it from here on a Windows host.
# Missing packaging tools are skipped with a warning; the raw binaries are
# always produced. Run from the repository root.
#
# Usage: VERSION=v1.0.0 ./scripts/build-release.sh
set -euo pipefail

VERSION="${VERSION:?set VERSION, e.g. VERSION=v1.0.0}"
OUT_DIR="dist/${VERSION}"
COMMIT="$(git rev-parse --short HEAD)"
RPM_VERSION="${VERSION#v}"   # RPM version fields don't use a leading "v"
REPO_ROOT="$(pwd)"

# For a real release tag (vX.Y.Z), the copy-and-run commands in
# docs/INSTALL.md must already point at this version — refuse to build
# otherwise. Non-tag builds (dev snapshots, throwaway versions) skip this.
if [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  bash scripts/sync-doc-versions.sh --check "$VERSION"
fi

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

# RPM signing is optional: set GPG_KEY_FILE (path to an armored secret key)
# and GPG_KEY_ID (its key ID) to sign; NFPM_RPM_PASSPHRASE too if that key
# has a passphrase. See docs/verifying-downloads.md.
if [[ -n "${GPG_KEY_FILE:-}" && -n "${GPG_KEY_ID:-}" ]]; then
  echo "RPM signing: enabled (key ${GPG_KEY_ID})"
else
  echo "WARNING: GPG_KEY_FILE/GPG_KEY_ID not set — RPMs will be built unsigned."
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
      GPG_KEY_FILE="${GPG_KEY_FILE:-}" GPG_KEY_ID="${GPG_KEY_ID:-}" \
      envsubst '${VERSION} ${GOARCH} ${BIN_PATH} ${GPG_KEY_FILE} ${GPG_KEY_ID}' \
      < packaging/rpm/nfpm.yaml.tmpl > "$GENERATED_SPEC"
    if [[ -z "${GPG_KEY_FILE:-}" || -z "${GPG_KEY_ID:-}" ]]; then
      # Strip the ===SIGNATURE=== block rather than hand nfpm an empty
      # key_file (undefined behavior) -- see the marker comment in
      # packaging/rpm/nfpm.yaml.tmpl.
      sed -i '/# ===SIGNATURE===/,/# ===END SIGNATURE===/d' "$GENERATED_SPEC"
    fi
    (cd packaging/rpm && "$NFPM_BIN" package --config "$GENERATED_SPEC" --target "${REPO_ROOT}/${OUT_DIR}/" --packager rpm)
    rm -f "$GENERATED_SPEC"
  fi
done

# --- Windows Server 2022 / Windows 10+ Pro (amd64) ---------------------------
# Native Windows binary, an offline install bundle (zip + Install-Axiom.ps1,
# auditable as plain text before you run it), and an MSI for a double-click /
# Add-Remove-Programs / `msiexec /qn` install. The MSI wraps the exact same
# Install-Axiom.ps1 / Uninstall-Axiom.ps1 as custom actions rather than
# reimplementing the ACL/service logic — see packaging/windows/msi/Axiom.wxs.
WIN_NAME="axiom-${VERSION}-windows-amd64.exe"
echo "building ${WIN_NAME}..."
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
  -trimpath \
  -ldflags "-s -w -X github.com/nuwandev/axiom/internal/api.Version=${VERSION#v} -X main.commit=${COMMIT}" \
  -o "${OUT_DIR}/${WIN_NAME}" \
  ./cmd/axiom

if command -v zip >/dev/null 2>&1; then
  WIN_STAGE="$(mktemp -d)"
  cp "${OUT_DIR}/${WIN_NAME}" "${WIN_STAGE}/axiom.exe"
  cp packaging/windows/Install-Axiom.ps1 packaging/windows/Uninstall-Axiom.ps1 "${WIN_STAGE}/"
  cp packaging/windows/README.md "${WIN_STAGE}/README.md"
  cp configs/example-windows.yaml "${WIN_STAGE}/"
  mkdir -p "${WIN_STAGE}/capability-examples"
  cp scripts/examples/hello-world.ps1.sample "${WIN_STAGE}/capability-examples/"
  echo "packaging the Windows install bundle (zip)..."
  (cd "${WIN_STAGE}" && zip -q -r "${REPO_ROOT}/${OUT_DIR}/axiom-${VERSION}-windows-amd64.zip" .)
  rm -rf "${WIN_STAGE}"
else
  echo "WARNING: 'zip' not found — skipping the Windows zip bundle (raw .exe still built)."
fi

if [[ "${BUILD_MSI:-0}" == "1" ]]; then
  bash scripts/build-msi.sh
else
  echo "NOTE: skipping the Windows MSI (WiX needs a Windows host); run scripts/build-msi.sh there, or set BUILD_MSI=1."
fi

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
