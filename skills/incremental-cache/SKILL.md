---
name: incremental-cache
description: Managing LatticeDB AST index synchronization and incremental caching.
---

# Incremental AST Cache Skill

## Purpose
Meta-Lattice records each file's SHA-256 and modification time. Only added or modified files are re-parsed on synchronization (about 50-130 ms for a 250-file synthetic project).

## When to Use
- At the start of a coding session.
- After switching git branches or pulling remote changes.
- MCP query tools (`zoom_*`, `check_layer_violation`, `estimate_blast_radius`) index automatically only when the graph is empty; call `sync_index` afterwards to pick up later changes.

## Tools
- `sync_index()`: Synchronize changes incrementally.
- `get_index_status()`: Review total L0/L1/L2/L3 nodes and graph size.
