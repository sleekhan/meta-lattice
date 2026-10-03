---
description: Incrementally synchronize LatticeDB AST index with workspace changes using file SHA-256 and mtime caching.
usage: /meta-lattice:sync [--force]
---

# Incremental AST Cache Sync (`/meta-lattice:sync`)

Keep the index synchronized (tens of milliseconds for a few hundred files in our measurements):

1. **SHA-256 & Mtime Caching**:
   - Only added or modified files are re-parsed; cross-file IMPORTS/CALLS edges are then re-resolved for the whole graph.
   - Unchanged files are served directly from cache (0 AST parsing overhead).
2. **Lightning Fast**:
   - Measured: ~50-130 ms for a 250-file synthetic project. Larger repositories were not measured.
