---
name: blast-radius
description: How to simulate side-effects and evaluate breaking changes using LatticeDB Blast Radius Estimator.
---

# Blast Radius Estimator Skill

## Purpose
Modifying shared utilities, core domain models, or public API signatures can cause cascading runtime breaks. Use `estimate_blast_radius` before making breaking edits.

## When to Use
- When planning refactorings of public methods or core functions.
- When changing parameter types or deleting an interface method.
- When renaming a function used across multiple domains.

## Workflow
1. Run `estimate_blast_radius(symbol_or_path="...", change_type="signature")`.
2. Inspect the **Blast Score** and **Risk Tier**:
   - `CRITICAL` / `HIGH`: Stop and inspect the **Top 10 Breaking Points**.
   - Note which external domain files directly call the function.
3. Update or deprecate the call sites simultaneously, or provide backwards-compatible default parameters.
