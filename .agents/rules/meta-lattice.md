# Meta-Lattice Operating Rules for Antigravity

This repository uses **Meta-Lattice** (powered by LatticeDB) to optimize codebase navigation, token consumption, and architectural integrity.

## 🛠 Available MCP Tools
- **`zoom_overview(domain=None)`**: L0/L1 architectural overview of domains and modules. Use this instead of reading files indiscriminately.
- **`zoom_module(file_path)`**: L1->L2 drill-down. Returns class definitions and method signatures WITHOUT method bodies.
- **`zoom_symbol(symbol_name, file_path=None)`**: L2->L3 drill-down. Retrieves the exact implementation, lines, complexity, and call graph of a target function.
- **`zoom_search(query, level=None, limit=15)`**: High-speed BM25 search across all indexed symbols, signatures, and docstrings.
- **`check_layer_violation(file_path=None)`**: Detects layered architecture violations (Controller -> Service -> Repository) and circular dependencies (`A -> B -> C -> A`).
- **`estimate_blast_radius(symbol_or_path, change_type="signature")`**: Simulates ripple effects and calculates a Blast Score (0-100) with Top 10 breaking change points.
- **`sync_index(force=False)`**: Synchronizes the AST cache with workspace changes.
- **`get_index_status()`**: Inspects graph statistics (L0, L1, L2, L3 counts).

## 🧭 Agent Directives
1. **Hierarchical Drill-Down**: Before reading large files, call `zoom_overview()` or `zoom_search()`, inspect interfaces with `zoom_module()`, and only retrieve full code using `zoom_symbol()`.
2. **Blast Radius Check**: Before refactoring public methods, classes, or interfaces, call `estimate_blast_radius()`.
3. **Architecture Validation**: Before completing any multi-file feature or refactoring, run `check_layer_violation()`.
