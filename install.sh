#!/usr/bin/env bash
set -euo pipefail

# Meta-Lattice One-Click Installer for Claude Code, OpenAI Codex & Google Antigravity
# Supports Linux & macOS (Native Go)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_PATH="${SCRIPT_DIR}/meta-lattice"

# Build binary if not already present.
# Prefers the host Go toolchain (native LatticeDB build); falls back to a
# reproducible Docker build of the host source (portable embedded engine)
# so hosts without Go installed still work.
if [ ! -f "${BIN_PATH}" ]; then
    echo "==> Building meta-lattice binary..."
    if command -v go >/dev/null 2>&1; then
        go build -ldflags="-s -w" -o "${BIN_PATH}" "${SCRIPT_DIR}/src"
    elif command -v docker >/dev/null 2>&1; then
        echo "    'go' not found; building host source inside Docker..."
        VERSION="${VERSION:-v1.0.0}" "${SCRIPT_DIR}/scripts/docker-build.sh" build "${BIN_PATH}"
    else
        echo "Error: neither 'go' nor 'docker' is available, and precompiled 'meta-lattice' was not found." >&2
        echo "Install Go (https://go.dev/dl) or Docker (https://docs.docker.com/get-docker), then retry." >&2
        exit 1
    fi
fi

# Execute installer
exec "${BIN_PATH}" install "$@"
