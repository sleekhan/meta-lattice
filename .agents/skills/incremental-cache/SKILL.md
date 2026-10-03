---
name: incremental-cache
description: Synchronizes Meta-Lattice AST cache incrementally in milliseconds using Go
---

# Incremental AST Cache

Use `sync_index` to refresh AST and graph edges after modifying source files:
- Ultra-fast incremental sync using SHA-256 and mtime
- Rebuilds cross-file IMPORTS and CALLS edges automatically
