---
name: hierarchical-zoom
description: Hierarchical context zoomer (L0->L3) saving tokens using Meta-Lattice in Go
---

# Hierarchical Context Zoomer

Use Meta-Lattice MCP tools to drill down into the codebase:
1. `zoom_overview`: View L0 domain boundaries and L1 module list
2. `zoom_module`: Inspect L2 class/interface signatures without method bodies
3. `zoom_symbol`: View L3 full implementation code of specific functions
4. `zoom_search`: BM25 full-text search across all symbols
