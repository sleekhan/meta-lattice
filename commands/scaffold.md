---
description: Generate a new source file from a per-language template (class, interface, struct, enum, module).
usage: /meta-lattice:scaffold <file_path> [--kind <class|interface|struct|enum|module>] [--name <Type>]
---

# Code Generation - Scaffold Module (`/meta-lattice:scaffold`)

Create new modules without leaving the agent loop:

1. **Template per language**: Kotlin, C#, Swift, PHP, Go, Python, TS/JS, Java, Rust, C/C++.
2. **Safe by default**: refuses to overwrite unless `overwrite` is true; paths are confined to the workspace.
3. **Verify**: run `sync_index`, then `zoom_module` on the created file.
