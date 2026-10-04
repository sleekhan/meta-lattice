---
name: codegen
description: Scaffolds new modules and applies multi-file edit plans with dry-run and rollback using Meta-Lattice
---

# Code Generation

Use Meta-Lattice MCP tools to generate code instead of writing files blindly:

1. `scaffold_module`: create a new source file from a language template
   (kind `class`|`interface`|`struct`|`enum`|`module`). Workspace-confined,
   no overwrite by default.
2. `apply_plan`: apply batched edits (`create_file`, `replace_text`,
   `insert_after`, `delete_file`). ALWAYS `dry_run` first for multi-file
   edits; on failure everything rolls back automatically.
3. After writing: `sync_index`, then `check_layer_violation` on touched files.
