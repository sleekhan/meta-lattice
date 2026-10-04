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

# USE_DOCKER=1: cross-compile each target inside the reproducible Go builder
# container (bind-mounts host source; no host Go toolchain required).
# USE_NATIVE=1 (with USE_DOCKER=1): build the linux target matching the
# container arch with CGO + prebuilt liblattice.so; all other targets stay
# portable (CGO_ENABLED=0, -tags nolattice, embedded pure-Go engine).
USE_DOCKER="${USE_DOCKER:-0}"
USE_NATIVE="${USE_NATIVE:-0}"
BUILDER_IMAGE="${BUILDER_IMAGE:-meta-lattice-builder:latest}"
DOCKER_BUILD="${ROOT_DIR}/scripts/docker-build.sh"

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

  # 1. Compile binary (Docker container or host toolchain)
  if [ "${USE_DOCKER}" = "1" ]; then
    # Container arch decides whether a native CGO linux build is possible.
    CONTAINER_ARCH=""
    if [ "${USE_NATIVE}" = "1" ]; then
      CONTAINER_ARCH="$("${DOCKER_BUILD}" go env GOARCH 2>/dev/null || true)"
    fi
    if [ "${USE_NATIVE}" = "1" ] && [ "${OS}" = "linux" ] && [ "${ARCH}" = "${CONTAINER_ARCH}" ]; then
      BUILDER_IMAGE="${BUILDER_IMAGE}" VERSION="${VERSION}" \
        "${DOCKER_BUILD}" native-linux "${ARCH}" "${STAGING_DIR}/${BIN_NAME}"
    else
      # Portable build: CGO off, embedded pure-Go engine (runs everywhere).
      BUILDER_IMAGE="${BUILDER_IMAGE}" VERSION="${VERSION}" \
        "${DOCKER_BUILD}" cross "${OS}" "${ARCH}" "${STAGING_DIR}/${BIN_NAME}"
    fi
  else
    # Host toolchain: CGO off + nolattice tag keeps cross-builds portable.
    # (Native LatticeDB linking only works when GOOS/GOARCH match the host.)
    CGO_ENABLED=0 GOOS="${OS}" GOARCH="${ARCH}" \
      go build -tags nolattice -ldflags="${LDFLAGS}" -o "${STAGING_DIR}/${BIN_NAME}" "${SRC_DIR}"
  fi

  # 2. Bundle essential files & native libraries
  cp "${ROOT_DIR}/README.md" "${STAGING_DIR}/"
  cp "${ROOT_DIR}/LICENSE" "${STAGING_DIR}/"
  cp "${ROOT_DIR}/${INSTALLER}" "${STAGING_DIR}/"
  [ -f "${ROOT_DIR}/AGENTS.md" ] && cp "${ROOT_DIR}/AGENTS.md" "${STAGING_DIR}/"
  [ -f "${ROOT_DIR}/GEMINI.md" ] && cp "${ROOT_DIR}/GEMINI.md" "${STAGING_DIR}/"
  [ -f "${ROOT_DIR}/CLAUDE.md" ] && cp "${ROOT_DIR}/CLAUDE.md" "${STAGING_DIR}/"
  [ -f "${ROOT_DIR}/.mcp.json" ] && cp "${ROOT_DIR}/.mcp.json" "${STAGING_DIR}/"

  # Bundle native LatticeDB shared library if available.
  # (Portable CGO-off binaries ignore it and use the embedded pure-Go engine;
  #  native CGO binaries load it via RPATH. Kept for forward compatibility.)
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
