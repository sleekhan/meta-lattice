# Meta-Lattice reproducible Go builder.
#
# Host source code is bind-mounted at /work at container RUN time
# (it is NOT baked into the image), so one image builds any checkout:
#   docker run --rm -v $(pwd):/work -w /work meta-lattice-builder go build ./...
#
# Default is CGO_ENABLED=0 (portable pure-Go fallback engine). Targets that
# need the native LatticeDB C library override with -e CGO_ENABLED=1 plus a
# PKG_CONFIG_PATH pointing at the matching deps/latticedb/lib/<os>-<arch>.

ARG GO_IMAGE=golang:1-bookworm
FROM ${GO_IMAGE}

ENV GOTOOLCHAIN=auto \
    CGO_ENABLED=0 \
    DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        pkg-config \
        gcc \
        git \
        zip \
        ca-certificates \
        curl \
        xz-utils \
        mingw-w64 \
    && rm -rf /var/lib/apt/lists/* \
    && dpkgArch="$(dpkg --print-architecture)" \
    && case "${dpkgArch}" in \
        amd64) ZIG_ARCH="x86_64" ;; \
        arm64) ZIG_ARCH="aarch64" ;; \
        *) echo "Unsupported architecture for zig: ${dpkgArch}" >&2; exit 1 ;; \
       esac \
    && curl -fsSL "https://ziglang.org/download/0.16.0/zig-${ZIG_ARCH}-linux-0.16.0.tar.xz" -o /tmp/zig.tar.xz \
    && tar -xJf /tmp/zig.tar.xz -C /usr/local \
    && mv /usr/local/zig-${ZIG_ARCH}-linux-0.16.0 /usr/local/zig \
    && ln -s /usr/local/zig/zig /usr/local/bin/zig \
    && rm -f /tmp/zig.tar.xz \
    && zig version \
    && go version

WORKDIR /work

CMD ["go", "version"]
