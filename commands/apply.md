---
description: Apply a batch of file edits with dry-run validation and automatic rollback on failure.
usage: /meta-lattice:apply <plan.json> [--execute]
---

# Code Generation - Apply Edit Plan (`/meta-lattice:apply`)

Apply multi-file edits as one validated batch:

1. **Ops**: `create_file`, `replace_text`, `insert_after`, `delete_file`.
2. **Dry-run first**: validate without writing; re-run with `--execute` to write.
3. **Rollback**: on the first failure all applied ops are reverted.
4. **Verify**: run `sync_index`, then `check_layer_violation` on touched files.
