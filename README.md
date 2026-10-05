<p align="center">
  <img src="docs/assets/meta-lattice-logo.svg" alt="Meta-Lattice Logo" width="160" />
</p>

<h1 align="center">Meta-Lattice (Go Native)</h1>

<p align="center">
  <strong>Hierarchical Context Zoomer · Architecture Boundary Auditor · Blast Radius Estimator · Incremental AST Cache</strong><br>
  <em>Native Standalone Go Implementation with Embedded LatticeDB & Multi-Language Parsers</em>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-blue.svg?style=flat-square" alt="License: MIT" /></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go Version" /></a>
  <a href="https://github.com/jeffhajewski/latticedb"><img src="https://img.shields.io/badge/LatticeDB-v0.15.0-6366F1?style=flat-square" alt="LatticeDB" /></a>
  <a href="#-building--cross-compilation"><img src="https://img.shields.io/badge/Platform-macOS%20%7C%20Linux%20%7C%20Windows-green.svg?style=flat-square" alt="Platform Support" /></a>
  <a href="https://modelcontextprotocol.io"><img src="https://img.shields.io/badge/MCP-2024--11--05-orange.svg?style=flat-square" alt="MCP Protocol" /></a>
  <a href="#-testing"><img src="https://img.shields.io/badge/Tests-25%20Passing-brightgreen.svg?style=flat-square" alt="Tests" /></a>
</p>

<p align="center">
  <strong>🇺🇸 English</strong> · <a href="README.ko.md">🇰🇷 한국어</a>
</p>

---

## 📖 Overview

**Meta-Lattice (Go Native)** is a high-performance tool written in Go that reduces AI agents' context token consumption in **Claude Code**, **OpenAI Codex**, and **Google Antigravity** environments (savings vary by usage and repository; see `docs/utility-and-performance.md` for measured examples), and pre-checks architecture rule violations and change impact before edits.

It ships official [LatticeDB](https://github.com/jeffhajewski/latticedb) v0.15.0 CGO bindings to persist the source-code knowledge graph in an embedded single-file property graph (`.lattice/knowledge.lattice`), and release packages bundle platform-specific native shared libraries (`liblattice.dylib` / `liblattice.so`) so it runs immediately with no extra setup. (In CGO-disabled environments it automatically falls back to the built-in pure-Go engine.)

---

## 🏛 Key Advantages

- **Official LatticeDB single-file graph database**: manages the L0–L3 knowledge graph and edge relations in a single binary file `.lattice/knowledge.lattice` via the official `github.com/jeffhajewski/latticedb/bindings/go`, with auto-generated B-Tree property indexes for fast lookups.
- **Native libraries auto-bundled in release artifacts**: release archives ship the `liblattice` shared library with preconfigured RPATH (`@executable_path`, `$ORIGIN`), so no system library installation is needed.
- **Multi-core parallel parsing optimized for large projects**: a `runtime.GOMAXPROCS(0)`-based worker pool parses thousands to tens of thousands of files at full CPU speed.
- **Built-in enterprise multi-language AST parsers**: natively parses **Go** (`go/ast`, `go/parser`), **Python**, **TypeScript/JavaScript**, **Java**, **Kotlin** (`.kt`, `.kts`), **C#** (`.cs`), **Swift** (`.swift`), **PHP** (`.php`), **Rust** (`.rs`), and **C/C++** (`.c`, `.cpp`, `.cc`, `.cxx`, `.h`, `.hpp`) to extract symbols, signatures, classes, and call graphs. Frontend support: **Vue SFC** (`.vue`), **Svelte** (`.svelte`), **React JSX/TSX** (JSX usage edges, PascalCase component detection), **Angular** (`@Component` decorator recognition).
- **Monorepo path mapping**: auto-detects TypeScript `tsconfig.json` path aliases (`@/*`, `~/*`) and multiple `go.mod` module paths in subdirectories to build accurate cross-file dependency edges.
- **Built-in MCP (Model Context Protocol) server**: implements MCP over `stdio` JSON-RPC 2.0 (protocolVersion `2024-11-05`, `tools` capability) for Codex, Antigravity, and Claude Code.

---

## 🚀 4 Key Features

### 1. Hierarchical Context Zoomer
* **L0 (Domain/Package)**: domain boundaries, per-package module counts, top-level role summaries.
  * **Token Guard**: when a full overview is requested on a large project with more than 50 modules, it automatically switches to per-domain summary mode (`summary_mode: true`) to prevent LLM context-window blowup. Drill into a specific domain with `zoom_overview(domain="...")` when needed.
* **L1 (Module/File)**: file metadata (LOC, language, docstring, export/import lists).
* **L2 (Class/Interface)**: class inheritance, fields, **method signatures only (method bodies omitted for major token savings)**.
* **L3 (Function/Symbol)**: full implementation, parameters, call relations, and cyclomatic complexity for the specific function under investigation.
* **BM25 full-text search (`zoom_search`)**: built-in BM25 ranking finds symbols and signatures in milliseconds. `snake_case` and `camelCase` are indexed as sub-words, so `symbol` finds `ZoomSymbol` and `http` finds `parseHTTPServer`.

### 2. Layered Architecture Boundary & Coupling Auditor
* **Layer rule audit**: tracks `import` statements to detect violations of the unidirectional layering principle (`controller -> service -> repository -> model`).
* **Cycle detection**: Tarjan SCC (Strongly Connected Components) instantly finds multi-hop cycles (`A -> B -> C -> A`).
* **Coupling metrics**: afferent coupling ($C_a$), efferent coupling ($C_e$), instability ($I = \frac{C_e}{C_a + C_e}$).

### 3. Blast Radius Estimator
* **Reverse graph traversal**: reverse BFS over `CALLS`, `IMPORTS`, `REFERENCES` graphs (excluding `CONTAINS` edges).
* **Weight model**: hop-distance decay ($0.5^{d-1}$), symbol visibility (public ×1.8), change type (signature ×2.0, removal ×2.5, etc.), cross-domain (×1.5).
* **Large-scale safety guard (Exploration Limit)**: when a widely-called symbol (top-level utility, logger, …) changes, BFS is capped at 250 nodes (`truncated: true`) and the most destructive spots are reported first to avoid latency and context overflow.
* **Result briefing**: 0–100 **Blast Score**, 4 risk tiers (`CRITICAL`, `HIGH`, `MODERATE`, `LOW`), Top 10 breaking points, and an ASCII impact-propagation tree.

### 4. Incremental AST Indexer & Local Cache Engine
* **SHA-256 & mtime mapping**: file hashes and mtimes are recorded in cache state (`.lattice/cache_state.json`, v1.3). A version mismatch triggers automatic full re-indexing on the next sync.
* **Localized delta edge updates**: on file modification only the directly related dependency edges are selectively deleted and rebuilt in $O(\Delta)$ — no full-graph wipe — achieving millisecond re-syncs on tens of thousands of files.
* **Automatic cross-file dependency restoration**: `IMPORTS` edges between files and optimally-scoped (same class first, same file first, imported files first) `CALLS` edges between L3 symbols.
  * **Go**: resolves package imports against `go.mod` module paths, linking `IMPORTS` edges to workspace-internal `.go` files (excluding tests). Multi-`go.mod` monorepos supported.
  * **TypeScript / JavaScript**: resolves relative/absolute imports and `tsconfig.json` path aliases (`@/*`, `~/*`) to file paths.
  * **Python / Java / Rust / C/C++**: resolves each language's standard and relative import/include paths.
* **Auto recovery**: if `knowledge.lattice` is deleted or corrupted (preserved as `.corrupt` with a stderr warning), the next sync fully re-indexes automatically.
* **Lazy indexing (MCP)**: calling a query tool (`zoom_*`, `check_layer_violation`, `estimate_blast_radius`) on an empty graph syncs first automatically. Later changes require `sync_index`.

#### Ignore rules
* **Directory-name patterns** (`.git`, `node_modules`, `dist`, `build`, `out`, `bin`, `target`, `obj`, `ref`, `venv`, …) apply to **directories only**. Files like `out.py` or `ref.go` are still indexed.
* **File patterns** (`*.min.js`, `*.bundle.js`, `*.map`, `package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`) apply to files.
* **Workspace custom ignores (`.latticeignore`)**: place a `.latticeignore` file in the repo root to add one glob pattern per line (blank lines and `#` comments ignored, duplicates collapsed) to the default ignore list. Use it for generated code, vendored trees, snapshots, and other large-monorepo-specific excludes.
* After changing ignore rules, rebuild with `./meta-lattice sync --force`.

---

## 🛠 Building & Cross-Compilation

**Docker-first**: no host Go/CGO toolchain required. `scripts/docker-build.sh` bind-mounts the host source into a container (`/work`) and builds it with a reproducible Go toolchain.

```bash
# Prepare the builder image (once; reused afterwards)
make docker-builder

# 1. Compile Windows LatticeDB DLL (using Zig Docker)
make latticedb-dll   # Generates deps/latticedb/lib/windows-amd64/lattice.dll

# 2. LatticeDB Native CGO Builds
make build-native    # Host platform native binary (CGO enabled)
make windows-native  # Windows x86_64 native binary (meta-lattice.exe + lattice.dll)

# 3. Pure-Go Portable Standalone Builds (nolattice)
make build-nolattice # Host platform pure-Go binary
make windows-nolattice # Windows x86_64 pure-Go standalone binary

# 4. Multi-Flavor & Release Packaging
make build-all-flavors # Batch-build all Native & Nolattice flavors across platforms (dist/flavors/)
make package           # Full release archives across platforms (dist/)
```

### Build matrix: native LatticeDB vs embedded pure-Go engine

| Command | CGO / Tag | Engine in use (check `status`) | Artifacts & Details |
| :--- | :---: | :--- | :--- |
| `make windows-native` | ON (`CGO_ENABLED=1`) | LatticeDB v0.15.0 | `meta-lattice.exe` + bundled `lattice.dll` (high-speed B-Tree property graph) |
| `make windows-nolattice` | OFF (`-tags nolattice`) | Embedded Go Property-Graph | Standalone `meta-lattice.exe` (no DLL needed, zero external deps) |
| `make build-native` | ON (`CGO_ENABLED=1`) | LatticeDB v0.15.0 | Links native shared library (`liblattice.so` / `liblattice.dylib`) |
| `make build-nolattice` | OFF (`-tags nolattice`) | Embedded Go Property-Graph | Portable pure-Go standalone binary |
| `make latticedb-dll` | - | - | Cross-compiles LatticeDB v0.15.0 to `lattice.dll` via Zig Docker |
| `make build-all-flavors` | BOTH | BOTH | Generates both Native & Nolattice binaries for all targets in `dist/flavors/` |
```

### 1. Native binary build (macOS / Linux, host toolchain)
```bash
# Native build
go build -ldflags="-s -w" -o meta-lattice ./src

# Or run the convenience shell script
./install.sh
```

### 2. Windows x86 64-bit cross-compilation (Windows x86 64-bit / amd64)
Go's built-in cross-compilation produces a Windows 64-bit executable (`.exe`) immediately on macOS or Linux:
```bash
# Windows x86 64-bit binary
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o meta-lattice.exe ./src

# Auto build & install on Windows PowerShell
powershell -ExecutionPolicy Bypass -File .\install.ps1
```

### 3. Using the Makefile (build & packaging)
```bash
make build       # host binary (meta-lattice, Docker by default)
make windows     # Windows x86 64-bit binary (meta-lattice.exe, Docker by default)
make build-all   # host + Windows binaries together
make package     # all-platform (Windows, Linux, macOS) release archives in dist/ (Docker by default)
make test        # unit/regression tests (Docker by default)
make docker-builder  # (re)build the Docker Go toolchain image
make docker-shell    # interactive shell in the builder container

# Build directly with the host Go toolchain with DOCKER=0
make build DOCKER=0
make test DOCKER=0
```

### 4. GitHub Actions Release & Publish
Builds Windows, Linux, and macOS binaries at high speed without tests, bundles only the essential files, and publishes them to GitHub Releases automatically.

- **Triggers**:
  1. **Git tag push**:
     ```bash
     git tag v1.0.0
     git push origin v1.0.0
     ```
  2. **Manual run from the GitHub web UI (`workflow_dispatch`)**:
     Go to the repo's `Actions` tab -> `Release & Publish` -> `Run workflow` (version tag optional).

- **Artifacts (dist/)**:
  - `meta-lattice-<version>-windows-amd64.zip` (Windows 64-bit)
  - `meta-lattice-<version>-linux-amd64.tar.gz` (Linux x86_64)
  - `meta-lattice-<version>-linux-arm64.tar.gz` (Linux ARM64)
  - `meta-lattice-<version>-darwin-arm64.tar.gz` (macOS Apple Silicon)
  - `meta-lattice-<version>-darwin-amd64.tar.gz` (macOS Intel)
  - `checksums.txt` (SHA256 checksums of all artifacts)

- **Package contents (essential files only)**:
  - Binary (`meta-lattice` or `meta-lattice.exe`)
  - One-click installer (`install.sh` or `install.ps1`)
  - Agent skills (`skills/`) and slash commands (`commands/`)
  - `README.md`, `LICENSE`

---

---

## 🚀 One-Click Agent Install & Integration Guide

Meta-Lattice ships built-in one-click installers for **Claude Code (plugin & MCP)**, **OpenAI Codex**, and **Google Antigravity**, fully supporting each platform's plugin specs and config-file standards.

### 1. All-in-One install
One command registers the MCP server plus dedicated commands/skills across all three platforms (Claude Code, Codex, Antigravity):

```bash
# macOS / Linux
./install.sh

# Windows (PowerShell)
powershell -ExecutionPolicy Bypass -File .\install.ps1

# Or run the compiled binary directly
./meta-lattice install
```

---

### 2. Per-platform setup

---

#### 🟣 Claude Code

With Claude Code, install as a **(1) Plugin**, via the **(2) auto installer**, or by **(3) direct MCP registration**.

##### Option A. Install as a Claude Code Plugin
Meta-Lattice follows the official Claude Code plugin spec ([`.claude-plugin/plugin.json`](file:///.claude-plugin/plugin.json)) and marketplace spec ([`.claude-plugin/marketplace.json`](file:///.claude-plugin/marketplace.json)).

1. **Register a local marketplace folder and install**:
   ```bash
   # 1. Add the extracted folder as a local marketplace (must use a './' path)
   claude plugin marketplace add ./

   # 2. Install the meta-lattice plugin (by plugin name)
   claude plugin install meta-lattice
   ```

2. **Register the GitHub remote marketplace directly**:
   ```bash
   # Add the GitHub repo as a marketplace, then install the plugin
   claude plugin marketplace add sleekhan/meta-lattice
   claude plugin install meta-lattice
   ```

##### Option B. Meta-Lattice auto installer (recommended)
```bash
./meta-lattice install --claude
```
- **Global MCP registration**: auto-adds the binary path to `mcpServers.meta-lattice` in `~/.claude.json`.
- **Slash command deployment**: the 6 commands below land in `~/.claude/commands/`, ready to invoke with `/`.
  - `/zoom`: hierarchical context navigation (L0 domains -> L3 symbol bodies)
  - `/audit`: unidirectional layer architecture & cycle audit
  - `/blast`: pre-change blast-score simulation
  - `/sync`: incremental AST cache sync
  - `/scaffold`: new module generation from language templates
  - `/apply`: batched file-edit application (dry-run validation + rollback)

##### Option C. Register MCP directly with the Claude Code CLI
```bash
claude mcp add meta-lattice $(pwd)/meta-lattice mcp
```

##### Option D. Project-level workspace config (`.mcp.json`)
Create [`.mcp.json`](file:///.mcp.json) in the repo root so every teammate gets Meta-Lattice automatically on entry, with no extra setup:
```json
{
  "mcpServers": {
    "meta-lattice": {
      "command": "./meta-lattice",
      "args": ["mcp"]
    }
  }
}
```

---

#### 🟢 OpenAI Codex

In Codex, connect the MCP server via the global config file or a project-local config, and let the agent guidelines ([`AGENTS.md`](file:///AGENTS.md)) drive optimal exploration behavior.

##### Option A. Meta-Lattice auto installer (recommended)
```bash
./meta-lattice install --codex
```
- **Global config update**: safely appends an `[mcp_servers.meta-lattice]` block to `~/.codex/config.toml` (existing config preserved, duplicates prevented).

##### Option B. Edit the global config directly (`~/.codex/config.toml`)
```toml
[mcp_servers.meta-lattice]
enabled = true
command = "/absolute/path/meta-lattice"
args = ["mcp"]
```

##### Option C. Project-local config (`.codex/config.toml`)
Register in the project root's [`.codex/config.toml`](file:///.codex/config.toml) for per-workspace isolation:
```toml
[mcp_servers.meta-lattice]
enabled = true
command = "./meta-lattice"
args = ["mcp"]
```

##### 📋 Codex agent guidelines (`AGENTS.md`)
Codex auto-loads the project root [`AGENTS.md`](file:///AGENTS.md) and follows this code of conduct:
- **Gradual zoom drill-down**: never read many files indiscriminately; query `zoom_overview` -> `zoom_module` -> `zoom_symbol` in order to avoid context waste.
- **Architecture guardrails**: `check_layer_violation()` is mandatory before finishing multi-file refactors.
- **Breaking-change safety**: simulate with `estimate_blast_radius(...)` before touching shared functions or interfaces.

---

#### 🔵 Google Antigravity (CLI & IDE)

In Google Antigravity (IDE and CLI), MCP server integration comes with **5 agent Skills**, **operating rules (GEMINI.md)**, and **auto Hooks**.

##### Option A. Meta-Lattice auto installer (recommended)
```bash
./meta-lattice install --antigravity
```
- **MCP server registration**: auto-registers the `meta-lattice` server in `~/.gemini/config/mcp_config.json`.
- **Auto-deploy 5 agent skills**: generates skill manifests ([`SKILL.md`](file:///skills/hierarchical-zoom/SKILL.md)) under `~/.gemini/config/skills/` so Antigravity can pick tools autonomously per task.

##### Option B. Manual global config (`~/.gemini/config/mcp_config.json`)
```json
{
  "mcpServers": {
    "meta-lattice": {
      "command": "/absolute/path/meta-lattice",
      "args": ["mcp"]
    }
  }
}
```

##### Option C. Project-local config (`.agents/mcp_config.json`)
Register in the project root's [`.agents/mcp_config.json`](file:///.agents/mcp_config.json) for per-project runs:
```json
{
  "mcpServers": {
    "meta-lattice": {
      "command": "./meta-lattice",
      "args": ["mcp"]
    }
  }
}
```

##### 🧠 Antigravity skills & rules integration
1. **Skill specs (Skills)**:
   - [`.agents/skills/hierarchical-zoom/SKILL.md`](file:///.agents/skills/hierarchical-zoom/SKILL.md): hierarchical zoom workflow for 80%+ token savings
   - [`.agents/skills/architecture-auditor/SKILL.md`](file:///.agents/skills/architecture-auditor/SKILL.md): unidirectional layer & cycle audit workflow
   - [`.agents/skills/blast-radius/SKILL.md`](file:///.agents/skills/blast-radius/SKILL.md): pre-refactor blast-radius simulation workflow
   - [`.agents/skills/incremental-cache/SKILL.md`](file:///.agents/skills/incremental-cache/SKILL.md): AST incremental-cache refresh workflow
2. **Agent rulebook (`GEMINI.md`)**:
   - The project root [`GEMINI.md`](file:///GEMINI.md) defines the operating rules Antigravity agents must follow during coding work.
3. **Auto hooks (`hooks/hooks.json`)**:
   - Hooks ([`hooks/hooks.json`](file:///hooks/hooks.json)) can refresh the incremental cache in the background on code edits.

---

### 3. Status check & clean removal (Status & Uninstall)

```bash
# Check install status across all platforms (MCP registration + skill/command counts)
./meta-lattice install --status

# Safely remove all registered platform configs and skills/commands
./meta-lattice install --uninstall

# Remove only specific platforms
./meta-lattice install --uninstall --claude
./meta-lattice install --uninstall --codex
./meta-lattice install --uninstall --antigravity
```

---

## 💻 CLI Quickstart

```bash
# 1. Fast incremental index sync (full re-index: --force)
./meta-lattice sync

# 2. Index status and node/edge counts
./meta-lattice status

# 3. Hierarchical context zoom
./meta-lattice zoom overview                       # L0/L1 domain & module overview
./meta-lattice zoom module src/indexer/go_parser.go # L1->L2 module detail (signatures only)
./meta-lattice zoom symbol ParseGoFile             # L3 full implementation + call graph
./meta-lattice zoom search BlastRadius             # BM25 full-text search

# 4. Architecture & cycle audit
./meta-lattice audit
./meta-lattice audit --file src/features/auditor/auditor.go

# 5. Blast radius simulation
./meta-lattice blast EstimateBlastRadius --type signature --hops 4

# 5b. Code generation (scaffold & edit plans)
./meta-lattice scaffold --path svc/user_service.py --kind class --name UserService
./meta-lattice apply-plan --file plan.json            # dry-run validation (writes nothing)
./meta-lattice apply-plan --file plan.json --execute  # apply for real (rolls back on failure)

# 6. Run the MCP server (stdio)
./meta-lattice mcp

# 7. Check platform install status
./meta-lattice install --status

# Machine-readable output with --json (sync, status, zoom, audit, blast, scaffold, apply-plan)
./meta-lattice zoom search BlastRadius --json
./meta-lattice audit --json
```

---

## 🤖 AI Agent Integration (MCP Tools)

| Tool (MCP) | Parameters | Description |
| :--- | :--- | :--- |
| `zoom_overview` | `domain?: string` | L0/L1 domain & module catalog (fewer tokens than reading files) |
| `zoom_module` | `file_path: string` | L1->L2 module detail: class/interface signatures only (bodies excluded) |
| `zoom_symbol` | `symbol_name: string, file_path?: string` | L2->L3 symbol detail: full implementation + call graph |
| `zoom_search` | `query: string, level?: string, limit?: int` | Instant BM25 search over symbols and signatures |
| `check_layer_violation` | `file_path?: string` | Layer violations, circular dependencies, coupling metrics |
| `estimate_blast_radius` | `symbol_or_path: string, change_type?: string` | Blast Score + Top 10 breaking-point briefing |
| `sync_index` | `force?: bool` | Fast incremental index sync |
| `get_index_status` | none | Node/edge counts and cache statistics |
| `scaffold_module` | `file_path: string, kind?: string, name?: string, namespace?: string, imports?: string[], overwrite?: bool` | Code generation: new source file from a language template (workspace-confined, no overwrite by default) |
| `apply_plan` | `operations: array, dry_run?: bool` | Code generation: batched file edits (`create_file`, `replace_text`, `insert_after`, `delete_file`) with dry-run validation and automatic rollback |

---

## 🧪 Testing

All modules are verified with Go's built-in test framework:

```bash
go test -v ./tests
```

```text
=== RUN   TestLayerViolationAndCycles
--- PASS: TestLayerViolationAndCycles (0.04s)
=== RUN   TestBlastRadiusEstimation
--- PASS: TestBlastRadiusEstimation (0.02s)
=== RUN   TestBlastRadiusExplorationLimit
--- PASS: TestBlastRadiusExplorationLimit (0.00s)
=== RUN   TestIncrementalCacheLifecycleAndEdgesSurvive
--- PASS: TestIncrementalCacheLifecycleAndEdgesSurvive (0.08s)
=== RUN   TestAmbiguousCallsAreNotMerged
--- PASS: TestAmbiguousCallsAreNotMerged (0.01s)
=== RUN   TestStaleCacheVersionReindexesAndEmptyDomainRemoved
--- PASS: TestStaleCacheVersionReindexesAndEmptyDomainRemoved (0.03s)
=== RUN   TestMCPServerProtocolsAndTools
--- PASS: TestMCPServerProtocolsAndTools (0.01s)
=== RUN   TestModelsToProperties
--- PASS: TestModelsToProperties (0.00s)
=== RUN   TestLayerDetectionUsesWholeTokens
--- PASS: TestLayerDetectionUsesWholeTokens (0.00s)
=== RUN   TestPythonParserMultiLineSignatureAndImports
--- PASS: TestPythonParserMultiLineSignatureAndImports (0.00s)
=== RUN   TestTSParserImportsAndArrowConsts
--- PASS: TestTSParserImportsAndArrowConsts (0.00s)
=== RUN   TestGoParser
--- PASS: TestGoParser (0.00s)
=== RUN   TestJavaParser
--- PASS: TestJavaParser (0.00s)
=== RUN   TestRustParser
--- PASS: TestRustParser (0.00s)
=== RUN   TestCCPPParser
--- PASS: TestCCPPParser (0.00s)
=== RUN   TestGoParserBodylessFunc
--- PASS: TestGoParserBodylessFunc (0.00s)
=== RUN   TestGoImportEdgesResolveViaGoMod
--- PASS: TestGoImportEdgesResolveViaGoMod (0.01s)
=== RUN   TestSyncRebuildsWhenGraphDBIsLost
--- PASS: TestSyncRebuildsWhenGraphDBIsLost (0.02s)
=== RUN   TestCodexInstallIsIdempotentAndUninstallClean
--- PASS: TestCodexInstallIsIdempotentAndUninstallClean (0.00s)
=== RUN   TestClaudeCodeInstallIsIdempotentAndUninstallClean
--- PASS: TestClaudeCodeInstallIsIdempotentAndUninstallClean (0.00s)
=== RUN   TestMCPIgnoresUnknownNotifications
--- PASS: TestMCPIgnoresUnknownNotifications (0.00s)
=== RUN   TestIgnorePatternsOnlyPruneDirectories
--- PASS: TestIgnorePatternsOnlyPruneDirectories (0.00s)
=== RUN   TestSearchSplitsCamelCase
--- PASS: TestSearchSplitsCamelCase (0.01s)
=== RUN   TestCorruptGraphDBIsRecovered
--- PASS: TestCorruptGraphDBIsRecovered (0.02s)
=== RUN   TestMCPLazyIndexesForQueryTools
--- PASS: TestMCPLazyIndexesForQueryTools (0.01s)
=== RUN   TestBlastReportsCallerDomains
--- PASS: TestBlastReportsCallerDomains (0.01s)
=== RUN   TestHierarchicalZoom
--- PASS: TestHierarchicalZoom (0.01s)
=== RUN   TestZoomOverviewSummaryMode
--- PASS: TestZoomOverviewSummaryMode (0.00s)
PASS
ok  	meta-lattice/tests	0.566s
```

---

## 📄 License

This project is distributed under the [MIT License](LICENSE). See the [LICENSE](LICENSE) file for details.
