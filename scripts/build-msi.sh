#!/usr/bin/env bash
# Builds the Windows MSI for an already-built release.
#
# WiX only supports Windows (its Linux dotnet-tool build mis-resolves the
# source paths in Axiom.wxs), so this runs on a Windows host with `wix`
# on PATH (Git Bash on the windows-latest release job, or a developer machine).
# It wraps the exact same Install-Axiom.ps1 / Uninstall-Axiom.ps1 as custom
# actions — see packaging/windows/msi/Axiom.wxs.
#
# Usage: VERSION=v1.2.0 bash scripts/build-msi.sh
# Expects dist/$VERSION/axiom-$VERSION-windows-amd64.exe (from build-release.sh)
# and writes dist/$VERSION/axiom-$VERSION-windows-amd64.msi next to it.
set -euo pipefail

VERSION="${VERSION:?set VERSION, e.g. VERSION=v1.0.0}"
OUT_DIR="dist/${VERSION}"
EXE="${OUT_DIR}/axiom-${VERSION}-windows-amd64.exe"
[[ -f "$EXE" ]] || { echo "missing ${EXE} — run scripts/build-release.sh first" >&2; exit 1; }
command -v wix >/dev/null 2>&1 || { echo "'wix' not found — install: dotnet tool install --global wix --version 5.0.2 (pin v5; v7+ requires a paid OSMF EULA)" >&2; exit 1; }

# MSI ProductVersion is limited to 3 numeric fields (no "v" prefix, no
# pre-release suffix): v1.2.0-rc1 -> 1.2.0
MSI_VERSION="$(echo "${VERSION#v}" | sed -E 's/-.*$//')"
if [[ ! "$MSI_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  MSI_VERSION="0.0.0"
  echo "WARNING: VERSION=${VERSION} doesn't reduce to a plain X.Y.Z for the MSI; using ${MSI_VERSION}"
fi

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
cp "$EXE" "${STAGE}/axiom.exe"
cp packaging/windows/Install-Axiom.ps1 packaging/windows/Uninstall-Axiom.ps1 "${STAGE}/"
cp packaging/windows/README.md "${STAGE}/README.md"
cp configs/example-windows.yaml "${STAGE}/"
mkdir -p "${STAGE}/capability-examples"
cp scripts/examples/hello-world.ps1.sample "${STAGE}/capability-examples/"

# wix.exe is a native Windows program: hand it Windows-style paths.
if command -v cygpath >/dev/null 2>&1; then
  STAGE_ARG="$(cygpath -w "$STAGE")"
  MSI_ARG="$(cygpath -w "$(pwd)/${OUT_DIR}")\axiom-${VERSION}-windows-amd64.msi"
else
  STAGE_ARG="$STAGE"
  MSI_ARG="$(pwd)/${OUT_DIR}/axiom-${VERSION}-windows-amd64.msi"
fi

echo "packaging the Windows MSI (version ${MSI_VERSION})..."
wix build packaging/windows/msi/Axiom.wxs \
  -d "ProductVersion=${MSI_VERSION}" -d "StagingDir=${STAGE_ARG}" \
  -arch x64 -o "$MSI_ARG"
