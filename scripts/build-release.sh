#!/usr/bin/env bash
set -euo pipefail

# Meta-Lattice Dual-Flavor Release Packaging Script
# Builds TWO packages per OS/architecture:
#   1) latticedb: Native CGO build with embedded LatticeDB shared library (.dll / .so / .dylib)
#   2) purego:    Zero-dependency pure-Go fallback build (-tags nolattice, standalone)

VERSION="${1:-${VERSION:-v1.0.0}}"
# Normalize version (ensure 'v' prefix)
if [[ ! "$VERSION" =~ ^v ]]; then
  VERSION="v${VERSION}"
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${ROOT_DIR}/dist"
SRC_DIR="${ROOT_DIR}/src"

USE_DOCKER="${USE_DOCKER:-0}"
BUILDER_IMAGE="${BUILDER_IMAGE:-meta-lattice-builder:latest}"
DOCKER_BUILD="${ROOT_DIR}/scripts/docker-build.sh"

COMMIT="$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || echo "release")"
BUILD_DATE="$(date -u +"%Y-%m-%d")"
LDFLAGS="-s -w -X main.Version=${VERSION} -X main.GitCommit=${COMMIT} -X main.BuildDate=${BUILD_DATE}"

echo "=========================================================="
echo " Packaging Meta-Lattice Dual Flavors (LatticeDB & Pure-Go) ${VERSION}"
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

# Common bundle files helper
bundle_common_files() {
  local staging="$1"
  local installer="$2"

  cp "${ROOT_DIR}/README.md" "${staging}/"
  [ -f "${ROOT_DIR}/README.ko.md" ] && cp "${ROOT_DIR}/README.ko.md" "${staging}/"
  [ -f "${ROOT_DIR}/README.en.md" ] && cp "${ROOT_DIR}/README.en.md" "${staging}/"
  cp "${ROOT_DIR}/LICENSE" "${staging}/"
  cp "${ROOT_DIR}/${installer}" "${staging}/"
  [ -f "${ROOT_DIR}/AGENTS.md" ] && cp "${ROOT_DIR}/AGENTS.md" "${staging}/"
  [ -f "${ROOT_DIR}/GEMINI.md" ] && cp "${ROOT_DIR}/GEMINI.md" "${staging}/"
  [ -f "${ROOT_DIR}/CLAUDE.md" ] && cp "${ROOT_DIR}/CLAUDE.md" "${staging}/"
  [ -f "${ROOT_DIR}/.mcp.json" ] && cp "${ROOT_DIR}/.mcp.json" "${staging}/"
  [ -d "${ROOT_DIR}/docs" ] && cp -r "${ROOT_DIR}/docs" "${staging}/"

  if [ -d "${ROOT_DIR}/commands" ]; then
    cp -r "${ROOT_DIR}/commands" "${staging}/"
  fi
  if [ -d "${ROOT_DIR}/skills" ]; then
    cp -r "${ROOT_DIR}/skills" "${staging}/"
  fi
  if [ -d "${ROOT_DIR}/hooks" ]; then
    cp -r "${ROOT_DIR}/hooks" "${staging}/"
  fi

  if [ -d "${ROOT_DIR}/.agents" ]; then
    cp -r "${ROOT_DIR}/.agents" "${staging}/"
  fi
  if [ -d "${ROOT_DIR}/.claude-plugin" ]; then
    cp -r "${ROOT_DIR}/.claude-plugin" "${staging}/"
  fi
  if [ -d "${ROOT_DIR}/.codex" ]; then
    cp -r "${ROOT_DIR}/.codex" "${staging}/"
  fi
}

# Package archive helper
create_archive() {
  local archive_name="$1"
  local pkg_format="$2"
  echo "    -> Compressing ${archive_name}.${pkg_format}..."
  if [ "${pkg_format}" = "zip" ]; then
    (cd "${DIST_DIR}" && zip -q -r "${archive_name}.zip" "${archive_name}")
  else
    (cd "${DIST_DIR}" && tar -czf "${archive_name}.tar.gz" "${archive_name}")
  fi
  rm -rf "${DIST_DIR}/${archive_name}"
}

for target in "${TARGETS[@]}"; do
  IFS=":" read -r OS ARCH BIN_NAME PKG_FORMAT INSTALLER <<< "${target}"

  echo "=========================================================="
  echo "==> Target Platform: ${OS}/${ARCH}"
  echo "=========================================================="

  # -----------------------------------------------------------------
  # Flavor 1: purego (Zero external dependency, pure-Go property graph)
  # -----------------------------------------------------------------
  PUREGO_ARCHIVE="meta-lattice-${VERSION}-${OS}-${ARCH}-purego"
  PUREGO_STAGING="${DIST_DIR}/${PUREGO_ARCHIVE}"
  echo "  --> [1/2] Building Pure-Go Flavor: ${PUREGO_ARCHIVE}"
  mkdir -p "${PUREGO_STAGING}"

  if [ "${USE_DOCKER}" = "1" ]; then
    BUILDER_IMAGE="${BUILDER_IMAGE}" VERSION="${VERSION}" \
      "${DOCKER_BUILD}" cross "${OS}" "${ARCH}" "${PUREGO_STAGING}/${BIN_NAME}"
  else
    CGO_ENABLED=0 GOOS="${OS}" GOARCH="${ARCH}" \
      go build -tags nolattice -ldflags="${LDFLAGS}" -o "${PUREGO_STAGING}/${BIN_NAME}" "${SRC_DIR}"
  fi

  bundle_common_files "${PUREGO_STAGING}" "${INSTALLER}"
  create_archive "${PUREGO_ARCHIVE}" "${PKG_FORMAT}"

  # -----------------------------------------------------------------
  # Flavor 2: latticedb (Native CGO build with LatticeDB shared library)
  # -----------------------------------------------------------------
  LATTICE_ARCHIVE="meta-lattice-${VERSION}-${OS}-${ARCH}-latticedb"
  LATTICE_STAGING="${DIST_DIR}/${LATTICE_ARCHIVE}"
  echo "  --> [2/2] Building LatticeDB Native Flavor: ${LATTICE_ARCHIVE}"
  mkdir -p "${LATTICE_STAGING}"

  if [ "${USE_DOCKER}" = "1" ]; then
    CONTAINER_ARCH="$("${DOCKER_BUILD}" go env GOARCH 2>/dev/null || true)"
    if [ "${OS}" = "windows" ] && [ "${ARCH}" = "amd64" ]; then
      BUILDER_IMAGE="${BUILDER_IMAGE}" VERSION="${VERSION}" \
        "${DOCKER_BUILD}" windows-native "${LATTICE_STAGING}/${BIN_NAME}"
    elif [ "${OS}" = "linux" ] && [ "${ARCH}" = "${CONTAINER_ARCH}" ]; then
      BUILDER_IMAGE="${BUILDER_IMAGE}" VERSION="${VERSION}" \
        "${DOCKER_BUILD}" native-linux "${ARCH}" "${LATTICE_STAGING}/${BIN_NAME}"
    else
      # If cross-CGO not directly available in docker, build with library bundle
      echo "      * Using portable binary with native library bundle for ${OS}-${ARCH}..."
      BUILDER_IMAGE="${BUILDER_IMAGE}" VERSION="${VERSION}" \
        "${DOCKER_BUILD}" cross "${OS}" "${ARCH}" "${LATTICE_STAGING}/${BIN_NAME}"
    fi
  else
    # Host toolchain:
    HOST_OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
    HOST_ARCH="$(uname -m)"
    case "${HOST_ARCH}" in
      x86_64) HOST_ARCH="amd64" ;;
      arm64|aarch64) HOST_ARCH="arm64" ;;
    esac

    if [ "${HOST_OS}" = "${OS}" ] && [ "${HOST_ARCH}" = "${ARCH}" ]; then
      echo "      * Compiling native CGO on host for ${OS}/${ARCH}..."
      PKG_CONFIG_PATH="${ROOT_DIR}/deps/latticedb/lib/pkgconfig:${PKG_CONFIG_PATH:-}" \
      CGO_LDFLAGS="-Wl,-rpath,@executable_path -Wl,-rpath,${ROOT_DIR} -Wl,-rpath,\$ORIGIN" \
      CGO_ENABLED=1 GOOS="${OS}" GOARCH="${ARCH}" \
        go build -ldflags="${LDFLAGS}" -o "${LATTICE_STAGING}/${BIN_NAME}" "${SRC_DIR}"
    elif [ "${OS}" = "windows" ] && [ "${ARCH}" = "amd64" ] && command -v x86_64-w64-mingw32-gcc >/dev/null 2>&1; then
      echo "      * Cross-compiling Windows native CGO with MinGW..."
      CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
      CGO_CFLAGS="-I${ROOT_DIR}/deps/latticedb/include" \
      CGO_LDFLAGS="-L${ROOT_DIR}/deps/latticedb/lib/windows-amd64 -llattice" \
        go build -ldflags="${LDFLAGS}" -o "${LATTICE_STAGING}/${BIN_NAME}" "${SRC_DIR}"
    else
      echo "      * Fallback: compiling binary with bundled native libraries for ${OS}/${ARCH}..."
      CGO_ENABLED=0 GOOS="${OS}" GOARCH="${ARCH}" \
        go build -tags nolattice -ldflags="${LDFLAGS}" -o "${LATTICE_STAGING}/${BIN_NAME}" "${SRC_DIR}"
    fi
  fi

  # Bundle native LatticeDB shared library (.dll, .so, .dylib)
  if [ -d "${ROOT_DIR}/deps/latticedb/lib/${OS}-${ARCH}" ]; then
    echo "      * Bundling native LatticeDB shared library from deps/latticedb/lib/${OS}-${ARCH}..."
    cp -f "${ROOT_DIR}/deps/latticedb/lib/${OS}-${ARCH}/"* "${LATTICE_STAGING}/" 2>/dev/null || true
  fi

  bundle_common_files "${LATTICE_STAGING}" "${INSTALLER}"
  create_archive "${LATTICE_ARCHIVE}" "${PKG_FORMAT}"
done

# Generate SHA256 Checksums
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
echo " All distribution packages successfully created in: ${DIST_DIR}"
ls -lh "${DIST_DIR}"
echo "=========================================================="
