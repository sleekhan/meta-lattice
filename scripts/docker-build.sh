#!/usr/bin/env bash
# Meta-Lattice Docker-based Go builder.
#
# Builds HOST source code (bind-mounted at /work) inside a reproducible Go
# toolchain container, so the host does NOT need Go or a CGO toolchain:
#
#   ./scripts/docker-build.sh builder        # (re)build the builder image (once)
#   ./scripts/docker-build.sh build          # portable binary for the HOST platform
#   ./scripts/docker-build.sh windows        # portable windows/amd64 binary
#   ./scripts/docker-build.sh cross OS ARCH OUT  # portable GOOS/GOARCH binary
#   ./scripts/docker-build.sh native-linux [ARCH] [OUT]  # linux CGO build (LatticeDB)
#   ./scripts/docker-build.sh test           # go test ./tests inside Docker
#   ./scripts/docker-build.sh shell          # interactive shell in builder
#   ./scripts/docker-build.sh go <args...>    # raw passthrough to `go`
#
# Portable binaries (CGO_ENABLED=0, -tags nolattice) run on the embedded
# pure-Go property-graph engine. Only `native-linux` links the prebuilt
# liblattice.so for the matching container architecture.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC_DIR="/work/src"
BUILDER_IMAGE="${BUILDER_IMAGE:-meta-lattice-builder:latest}"
GO_IMAGE="${GO_IMAGE:-golang:1-bookworm}"
# Named volume caching GOPATH (module + toolchain + build cache) across runs.
# Ephemeral per-run caches would re-download the toolchain on every build.
CACHE_VOL="${CACHE_VOL:-meta-lattice-gopath}"
VERSION="${VERSION:-v1.0.0}"
COMMIT="$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || echo "docker")"
BUILD_DATE="$(date -u +"%Y-%m-%d")"
LDFLAGS="-s -w -X main.Version=${VERSION} -X main.GitCommit=${COMMIT} -X main.BuildDate=${BUILD_DATE}"
PORTABLE_TAGS="nolattice"

usage() {
    sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

# --user causes permission errors on Docker Desktop bind mounts for macOS /
# Windows hosts, but is required on native Linux hosts so outputs are not
# root-owned. Kept as a plain string (not an array) for Bash 3.x compat.
if [ "$(uname -s)" = "Linux" ]; then
    DOCKER_USER="--user $(id -u):$(id -g)"
else
    DOCKER_USER=""
fi

ensure_builder() {
    if ! docker image inspect "${BUILDER_IMAGE}" >/dev/null 2>&1; then
        echo "==> Building builder image ${BUILDER_IMAGE} (base: ${GO_IMAGE})..."
        docker build \
            --build-arg "GO_IMAGE=${GO_IMAGE}" \
            -t "${BUILDER_IMAGE}" \
            -f "${ROOT_DIR}/docker/Dockerfile.builder" \
            "${ROOT_DIR}/docker"
    fi
    docker volume create "${CACHE_VOL}" >/dev/null
}

# docker_go <env KEY=VAL...> -- go <args...>
# NOTE: env values must not contain spaces (callers only pass KEY=VAL pairs).
docker_go() {
    local env_flags=""
    while [ $# -gt 0 ] && [ "$1" != "--" ]; do
        env_flags="${env_flags} -e $1"
        shift
    done
    if [ $# -gt 0 ]; then shift; fi
    # shellcheck disable=SC2086
    docker run --rm \
        -v "${ROOT_DIR}:/work" \
        -v "${CACHE_VOL}:/tmp/gopath" \
        -w /work \
        ${DOCKER_USER} \
        -e HOME=/tmp \
        -e GOPATH=/tmp/gopath \
        -e GOCACHE=/tmp/gopath/buildcache \
        -e GOTOOLCHAIN=auto \
        ${env_flags} \
        "${BUILDER_IMAGE}" \
        go "$@"
}

# Container paths differ from host paths: translate an absolute host output
# path under ROOT_DIR to its /work mount equivalent (relative names pass
# through, they already resolve against WORKDIR /work).
to_work_path() {
    local p="$1"
    case "${p}" in
        "${ROOT_DIR}"/*) echo "/work/${p#"${ROOT_DIR}"/}" ;;
        *) echo "${p}" ;;
    esac
}

# Map `uname -s -m` to GOOS/GOARCH for the host-portable binary.
host_goos_goarch() {
    local os arch
    os="$(uname -s)"
    arch="$(uname -m)"
    case "${os}" in
        Darwin) os="darwin" ;;
        Linux) os="linux" ;;
        MINGW*|MSYS*|CYGWIN*|Windows*) os="windows" ;;
        *) os="linux" ;;
    esac
    case "${arch}" in
        x86_64|amd64) arch="amd64" ;;
        arm64|aarch64) arch="arm64" ;;
        *) arch="amd64" ;;
    esac
    echo "${os} ${arch}"
}

container_arch() {
    local m
    m="$(docker run --rm "${BUILDER_IMAGE}" uname -m)"
    case "${m}" in
        x86_64) echo "amd64" ;;
        aarch64) echo "arm64" ;;
        *) echo "${m}" ;;
    esac
}

cmd_builder() {
    echo "==> (Re)building builder image ${BUILDER_IMAGE} (base: ${GO_IMAGE})..."
    docker build \
        --build-arg "GO_IMAGE=${GO_IMAGE}" \
        -t "${BUILDER_IMAGE}" \
        -f "${ROOT_DIR}/docker/Dockerfile.builder" \
        "${ROOT_DIR}/docker"
}

# Portable cross-compile: CGO off, pure-Go fallback engine. Always works.
cmd_cross() {
    local goos="${1:?usage: cross <GOOS> <GOARCH> <OUTPUT>}"
    local goarch="${2:?usage: cross <GOOS> <GOARCH> <OUTPUT>}"
    local out
    out="$(to_work_path "${3:?usage: cross <GOOS> <GOARCH> <OUTPUT>}")"
    ensure_builder
    echo "==> Docker build: ${goos}/${goarch} -> ${out} (portable, embedded engine)..."
    docker_go \
        "CGO_ENABLED=0" "GOOS=${goos}" "GOARCH=${goarch}" \
        -- build -tags "${PORTABLE_TAGS}" -ldflags="${LDFLAGS}" -o "${out}" "${SRC_DIR}"
}

cmd_build() {
    local out="${1:-}"
    local pair goos goarch
    pair="$(host_goos_goarch)"
    goos="${pair%% *}"
    goarch="${pair##* }"
    if [ -z "${out}" ]; then
        out="meta-lattice"
        [ "${goos}" = "windows" ] && out="meta-lattice.exe"
    fi
    cmd_cross "${goos}" "${goarch}" "${out}"
}

cmd_windows() {
    local out="${1:-meta-lattice.exe}"
    cmd_cross windows amd64 "${out}"
}

# Native linux build with CGO + prebuilt liblattice.so for the container arch.
# The .pc shipped in deps assumes libdir=deps/latticedb/lib, but per-arch
# binaries live in lib/linux-<arch>/, so stage a corrected .pc + lib pair.
cmd_native_linux() {
    local arch="${1:-}"
    local out="${2:-}"
    ensure_builder
    if [ -z "${arch}" ]; then
        arch="$(container_arch)"
    fi
    if [ -z "${out}" ]; then
        out="/work/meta-lattice-linux-${arch}-native"
    else
        out="$(to_work_path "${out}")"
    fi
    if [ ! -f "${ROOT_DIR}/deps/latticedb/lib/linux-${arch}/liblattice.so" ]; then
        echo "Error: no prebuilt LatticeDB for linux-${arch} (see deps/latticedb/lib/)." >&2
        echo "Falling back to portable build; run 'cross linux ${arch} ${out}' instead." >&2
        exit 1
    fi
    echo "==> Docker build: linux/${arch} -> ${out} (CGO, native LatticeDB)..."
    # shellcheck disable=SC2086
    docker run --rm \
        -v "${ROOT_DIR}:/work" \
        -v "${CACHE_VOL}:/tmp/gopath" \
        -w /work \
        ${DOCKER_USER} \
        -e HOME=/tmp \
        -e GOPATH=/tmp/gopath \
        -e GOCACHE=/tmp/gopath/buildcache \
        -e GOTOOLCHAIN=auto \
        -e "CGO_ENABLED=1" \
        -e "GOOS=linux" \
        -e "GOARCH=${arch}" \
        "${BUILDER_IMAGE}" \
        sh -c "mkdir -p /tmp/lat/lib /tmp/lat/pc \
          && cp /work/deps/latticedb/lib/linux-${arch}/liblattice.so /tmp/lat/lib/ \
          && sed -e 's|^libdir=.*|libdir=/tmp/lat/lib|' -e 's|^includedir=.*|includedir=/work/deps/latticedb/include|' /work/deps/latticedb/lib/pkgconfig/lattice.pc > /tmp/lat/pc/lattice.pc \
          && PKG_CONFIG_PATH=/tmp/lat/pc CGO_LDFLAGS='-Wl,-rpath,\$ORIGIN' \
             go build -ldflags='${LDFLAGS}' -o '${out}' /work/src \
          && cp /work/deps/latticedb/lib/linux-${arch}/liblattice.so \"\$(dirname '${out}')/\" \
          && ls -lh '${out}'"
}

cmd_test() {
    ensure_builder
    echo "==> Docker test: go test ./tests (portable engine)..."
    docker_go "CGO_ENABLED=0" -- test -count=1 "$@" ./tests
}

cmd_shell() {
    ensure_builder
    # shellcheck disable=SC2086
    docker run --rm -it \
        -v "${ROOT_DIR}:/work" \
        -v "${CACHE_VOL}:/tmp/gopath" \
        -w /work \
        ${DOCKER_USER} \
        -e HOME=/tmp \
        -e GOPATH=/tmp/gopath \
        -e GOCACHE=/tmp/gopath/buildcache \
        -e GOTOOLCHAIN=auto \
        "${BUILDER_IMAGE}" \
        bash
}

sub="${1:-build}"
case "${sub}" in
    builder) shift; cmd_builder "$@" ;;
    build) shift; cmd_build "$@" ;;
    windows) shift; cmd_windows "$@" ;;
    cross) shift; cmd_cross "$@" ;;
    native-linux) shift; cmd_native_linux "$@" ;;
    test) shift; cmd_test "$@" ;;
    shell) shift; cmd_shell "$@" ;;
    go) shift; ensure_builder; docker_go -- "$@" ;;
    -h|--help|help) usage ;;
    *) echo "Unknown subcommand: ${sub}" >&2; usage >&2; exit 1 ;;
esac
