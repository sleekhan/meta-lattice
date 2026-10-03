# Meta-Lattice One-Click Installer for Claude Code, OpenAI Codex & Google Antigravity
# Supports Windows x86 64-bit (Native Go)

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$BinPath = "$ScriptDir\meta-lattice.exe"

if (-not (Test-Path $BinPath)) {
    Write-Host "==> Building meta-lattice.exe for Windows x86 64-bit (windows/amd64)..."
    if (Get-Command go -ErrorAction SilentlyContinue) {
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        go build -ldflags="-s -w" -o $BinPath "$ScriptDir\src"
    } else {
        Write-Error "Error: 'go' was not found in PATH and precompiled 'meta-lattice.exe' does not exist."
        exit 1
    }
}

& $BinPath install $args
