#!/usr/bin/env bash
# Script to compile LatticeDB C shared library (.dll / .so / .dylib) using Docker with Zig
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILDER_IMAGE="${BUILDER_IMAGE:-meta-lattice-builder:latest}"
TARGET="${1:-x86_64-windows-gnu}"
OPTIMIZE="${2:-ReleaseSafe}"
LATTICEDB_VERSION="${LATTICEDB_VERSION:-v0.15.0}"

echo "=========================================================="
echo " Building LatticeDB native library using Zig Docker"
echo " Target:   ${TARGET}"
echo " Optimize: ${OPTIMIZE}"
echo " Version:  ${LATTICEDB_VERSION}"
echo "=========================================================="

# Ensure builder container is ready
if ! docker image inspect "${BUILDER_IMAGE}" >/dev/null 2>&1; then
    echo "==> Builder image not found. Building ${BUILDER_IMAGE}..."
    "${ROOT_DIR}/scripts/docker-build.sh" builder
fi

# Determine destination directory
DEST_NAME=""
case "${TARGET}" in
    x86_64-windows-gnu) DEST_NAME="windows-amd64" ;;
    aarch64-windows-gnu) DEST_NAME="windows-arm64" ;;
    x86_64-linux-gnu)   DEST_NAME="linux-amd64" ;;
    aarch64-linux-gnu)  DEST_NAME="linux-arm64" ;;
    x86_64-macos)       DEST_NAME="darwin-amd64" ;;
    aarch64-macos)      DEST_NAME="darwin-arm64" ;;
    *) DEST_NAME="${TARGET}" ;;
esac

DEST_DIR="${ROOT_DIR}/deps/latticedb/lib/${DEST_NAME}"
mkdir -p "${DEST_DIR}"

echo "==> Compiling LatticeDB ${LATTICEDB_VERSION} inside Docker container..."
docker run --rm \
    -v "${ROOT_DIR}:/work" \
    -w /tmp \
    "${BUILDER_IMAGE}" \
    sh -c "
        set -eu
        mkdir -p /tmp/lat-build && cd /tmp/lat-build
        echo '--> Fetching LatticeDB source...'
        curl -fsSL 'https://github.com/jeffhajewski/latticedb/archive/refs/tags/${LATTICEDB_VERSION}.tar.gz' | tar -xz --strip-components=1
        
        echo '--> Building shared library with zig...'
        zig build shared -Dtarget=${TARGET} -Doptimize=${OPTIMIZE}
        
        echo '--> Copying artifacts to host /work/deps/latticedb...'
        mkdir -p /work/deps/latticedb/lib/${DEST_NAME}
        cp -f zig-out/bin/* /work/deps/latticedb/lib/${DEST_NAME}/ 2>/dev/null || true
        cp -f zig-out/lib/* /work/deps/latticedb/lib/${DEST_NAME}/ 2>/dev/null || true
        cp -f zig-out/include/lattice.h /work/deps/latticedb/include/ 2>/dev/null || true
        
        # If windows, make sure .pc points correctly
        mkdir -p /work/deps/latticedb/lib/${DEST_NAME}/pkgconfig
        if [ -f zig-out/lib/pkgconfig/lattice.pc ]; then
            cp -f zig-out/lib/pkgconfig/lattice.pc /work/deps/latticedb/lib/${DEST_NAME}/pkgconfig/
        fi
    "

echo "==> Successfully installed LatticeDB artifacts for ${DEST_NAME}:"
ls -lh "${DEST_DIR}"
