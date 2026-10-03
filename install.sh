#!/usr/bin/env bash
set -euo pipefail

# Meta-Lattice One-Click Installer for Claude Code, OpenAI Codex & Google Antigravity
# Supports Linux & macOS (Native Go)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_PATH="${SCRIPT_DIR}/meta-lattice"

# Build binary if not already present
if [ ! -f "${BIN_PATH}" ]; then
    echo "==> Building meta-lattice binary..."
    if command -v go >/dev/null 2>&1; then
        go build -ldflags="-s -w" -o "${BIN_PATH}" "${SCRIPT_DIR}/src"
    else
        echo "Error: 'go' is not installed or not in PATH, and precompiled 'meta-lattice' was not found." >&2
        exit 1
    fi
fi

# Execute installer
exec "${BIN_PATH}" install "$@"
