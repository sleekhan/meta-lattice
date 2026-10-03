#!/usr/bin/env bash
set -euo pipefail

# Meta-Lattice Multi-Platform Release Packaging Script
# Builds for Windows (x86_64), Linux (amd64, arm64), and macOS (amd64, arm64)
# Packages binaries with essential files (README, LICENSE, installer, skills, commands)

VERSION="${1:-${VERSION:-v1.0.0}}"
# Normalize version (ensure 'v' prefix)
if [[ ! "$VERSION" =~ ^v ]]; then
  VERSION="v${VERSION}"
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${ROOT_DIR}/dist"
SRC_DIR="${ROOT_DIR}/src"

COMMIT="$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || echo "release")"
BUILD_DATE="$(date -u +"%Y-%m-%d")"
LDFLAGS="-s -w -X main.Version=${VERSION} -X main.GitCommit=${COMMIT} -X main.BuildDate=${BUILD_DATE}"

echo "=========================================================="
echo " Packaging Meta-Lattice ${VERSION}"
echo " Commit: ${COMMIT} | Date: ${BUILD_DATE}"
echo "=========================================================="

rm -rf "${DIST_DIR}"
mkdir -p "${DIST_DIR}"

TARGETS=(
  "windows:amd64:meta-lattice.exe:zip:install.ps1"
  "linux:amd64:meta-lattice:tar.gz:install.sh"
  "linux:arm64:meta-lattice:tar.gz:install.sh"
  "darwin:amd64:meta-lattice:tar.gz:install.sh"
  "darwin:arm64:meta-lattice:tar.gz:install.sh"
)

for target in "${TARGETS[@]}"; do
  IFS=":" read -r OS ARCH BIN_NAME PKG_FORMAT INSTALLER <<< "${target}"
  ARCHIVE_NAME="meta-lattice-${VERSION}-${OS}-${ARCH}"
  STAGING_DIR="${DIST_DIR}/${ARCHIVE_NAME}"

  echo "==> Building ${OS}/${ARCH}..."
  mkdir -p "${STAGING_DIR}"

  # 1. Compile binary
  CGO_ENABLED=0 GOOS="${OS}" GOARCH="${ARCH}" go build -ldflags="${LDFLAGS}" -o "${STAGING_DIR}/${BIN_NAME}" "${SRC_DIR}"

  # 2. Bundle essential files & native libraries
  cp "${ROOT_DIR}/README.md" "${STAGING_DIR}/"
  cp "${ROOT_DIR}/LICENSE" "${STAGING_DIR}/"
  cp "${ROOT_DIR}/${INSTALLER}" "${STAGING_DIR}/"
  [ -f "${ROOT_DIR}/AGENTS.md" ] && cp "${ROOT_DIR}/AGENTS.md" "${STAGING_DIR}/"
  [ -f "${ROOT_DIR}/GEMINI.md" ] && cp "${ROOT_DIR}/GEMINI.md" "${STAGING_DIR}/"
  [ -f "${ROOT_DIR}/.mcp.json" ] && cp "${ROOT_DIR}/.mcp.json" "${STAGING_DIR}/"

  # Bundle native LatticeDB shared library if available
  if [ -d "${ROOT_DIR}/deps/latticedb/lib/${OS}-${ARCH}" ]; then
    echo "    -> Bundling native LatticeDB shared library for ${OS}-${ARCH}..."
    cp -f "${ROOT_DIR}/deps/latticedb/lib/${OS}-${ARCH}/"* "${STAGING_DIR}/" 2>/dev/null || true
  fi
  
  if [ -d "${ROOT_DIR}/commands" ]; then
    cp -r "${ROOT_DIR}/commands" "${STAGING_DIR}/"
  fi
  if [ -d "${ROOT_DIR}/skills" ]; then
    cp -r "${ROOT_DIR}/skills" "${STAGING_DIR}/"
  fi
  if [ -d "${ROOT_DIR}/hooks" ]; then
    cp -r "${ROOT_DIR}/hooks" "${STAGING_DIR}/"
  fi

  # Bundle dot-directories for Google Antigravity, Claude Code, OpenAI Codex
  if [ -d "${ROOT_DIR}/.agents" ]; then
    cp -r "${ROOT_DIR}/.agents" "${STAGING_DIR}/"
  fi
  if [ -d "${ROOT_DIR}/.claude-plugin" ]; then
    cp -r "${ROOT_DIR}/.claude-plugin" "${STAGING_DIR}/"
  fi
  if [ -d "${ROOT_DIR}/.codex" ]; then
    cp -r "${ROOT_DIR}/.codex" "${STAGING_DIR}/"
  fi

  # 3. Create archive
  echo "==> Packaging ${ARCHIVE_NAME}.${PKG_FORMAT}..."
  if [ "${PKG_FORMAT}" = "zip" ]; then
    (cd "${DIST_DIR}" && zip -q -r "${ARCHIVE_NAME}.zip" "${ARCHIVE_NAME}")
  else
    (cd "${DIST_DIR}" && tar -czf "${ARCHIVE_NAME}.tar.gz" "${ARCHIVE_NAME}")
  fi

  # Clean up staging directory
  rm -rf "${STAGING_DIR}"
done

# 4. Generate SHA256 Checksums
echo "==> Generating checksums..."
(
  cd "${DIST_DIR}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum meta-lattice-* > checksums.txt
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 meta-lattice-* > checksums.txt
  fi
)

echo "=========================================================="
echo " Distribution artifacts ready in: ${DIST_DIR}"
ls -lh "${DIST_DIR}"
echo "=========================================================="
