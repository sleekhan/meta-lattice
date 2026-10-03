---
description: Hierarchically inspect and drill down code from L0 architecture to L3 symbol without wasting tokens.
usage: /meta-lattice:zoom [overview | module <path> | symbol <name> | search <query>]
---

# Hierarchical Context Zoomer (`/meta-lattice:zoom`)

Drill down the codebase progressively using LatticeDB to save context tokens (savings depend on the repository; see docs/utility-and-performance.md):

1. **L0 Architecture Overview**:
   - Run `zoom_overview()` or `/meta-lattice:zoom overview` to get top-level domain boundaries and module catalog.
2. **L1 -> L2 Module Interfaces**:
   - Run `zoom_module(file_path)` or `/meta-lattice:zoom module <file_path>` to see class definitions, fields, and method signatures (omits method bodies).
3. **L2 -> L3 Symbol Implementation**:
   - Run `zoom_symbol(symbol_name)` or `/meta-lattice:zoom symbol <name>` to view full source code, parameters, callers, and outgoing calls for that specific function.
4. **BM25 Semantic/Keyword Search**:
   - Run `zoom_search(query)` or `/meta-lattice:zoom search <query>` to instantly locate classes, functions, and docstrings.
