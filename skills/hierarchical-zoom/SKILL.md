---
name: hierarchical-zoom
description: Progressive L0 -> L1 -> L2 -> L3 drill-down strategy for Claude Code to reduce context token consumption.
---

# Hierarchical Context Zooming Skill

## Overview
When exploring or debugging a large repository, DO NOT read whole files into context blindly. Doing so exhausts token limits, increases latency, and degrades reasoning quality.

Instead, follow the **Hierarchical Drill-Down Principle** powered by LatticeDB:

```
L0: Architectural Domain (e.g. auth, billing, core)
  ↓
L1: Module / File (e.g. src/auth/service.py - LOC, exports, imports)
  ↓
L2: Class / Interface Signatures (method signatures ONLY, no bodies)
  ↓
L3: Function / Symbol Implementation (full code of ONLY the target function)
```

## Workflow Guide

1. **Start with L0 Architecture Overview**:
   Call `zoom_overview()`.
   - Review which domain owns the functionality.
   - Note the relevant file paths from the domain catalog.

2. **Inspect L2 Interface Signatures**:
   Call `zoom_module(file_path="src/...")`.
   - Review class definitions, base classes, and public method signatures.
   - Notice method bodies are omitted, saving tokens compared with reading the whole file while retaining the contract.

3. **Drill Down to L3 Target Symbol**:
   Call `zoom_symbol(symbol_name="login", file_path="...")`.
   - Only retrieve full code implementation for the function(s) you need to debug or edit.
   - Review incoming callers and outgoing calls to understand context.

4. **Instant Symbol Search**:
   Call `zoom_search(query="TokenValidator")` to instantly locate symbol IDs and files using LatticeDB BM25 search.
