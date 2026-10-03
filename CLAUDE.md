# Claude Code Agent Guidelines for Meta-Lattice (Go Native)

This project uses **Meta-Lattice** (compiled standalone Go binary) to optimize codebase navigation, token consumption, and architectural integrity via MCP tools.

## 🛠 Available MCP Tools

The `meta-lattice` MCP server provides the following tools:

1. **`zoom_overview(domain=None)`**: L0/L1 architectural overview of domains and modules. Use this instead of reading files indiscriminately.
2. **`zoom_module(file_path)`**: L1->L2 drill-down. Returns class definitions and method signatures WITHOUT method bodies.
3. **`zoom_symbol(symbol_name, file_path=None)`**: L2->L3 drill-down. Retrieves the exact implementation, lines, complexity, and call graph of a target function.
4. **`zoom_search(query, level=None, limit=15)`**: High-speed BM25 search across all indexed symbols, signatures, and docstrings.
5. **`check_layer_violation(file_path=None)`**: Detects layered architecture violations (Controller -> Service -> Repository) and circular dependencies (`A -> B -> C -> A`).
6. **`estimate_blast_radius(symbol_or_path, change_type="signature")`**: Simulates ripple effects and calculates a Blast Score (0-100) with Top 10 breaking change points.
7. **`sync_index(force=False)`**: Synchronizes the AST cache with workspace changes.
8. **`get_index_status()`**: Inspects graph statistics (L0, L1, L2, L3 counts).

## 🧭 Operating Rules for Claude Code

1. **Token Efficiency (Hierarchical Drill-Down)**:
   - When asked to analyze, explore, or inspect the codebase (e.g. "프로젝트를 분석해 주세요"), NEVER read multiple full files at once with Grep, Glob, or View.
   - ALWAYS call `zoom_overview()` first to inspect domain boundaries, layers, and module catalogs.
   - Then call `zoom_module(file_path)` to inspect interfaces, classes, and exported signatures.
   - Only call `zoom_symbol(symbol_name)` for the specific function(s) being debugged or modified.

2. **Architectural Guardrails**:
   - Before completing any multi-file edits or refactoring, execute `check_layer_violation()`.
   - If circular dependencies or forbidden layer imports are detected, resolve them immediately.

3. **Breaking Change Safety**:
   - When modifying function signatures, interfaces, or shared utilities, run `estimate_blast_radius(...)` first to inspect the Top 10 affected call sites.
