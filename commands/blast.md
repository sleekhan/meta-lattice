---
description: Simulate side-effects and estimate blast radius before modifying core functions or interfaces.
usage: /meta-lattice:blast <symbol_or_path> [--type <signature|body|removal|rename>]
---

# Blast Radius Estimator (`/meta-lattice:blast`)

Simulate breaking changes and ripple effects before refactoring:

1. **Blast Score (0-100)**:
   - Weighted reverse traversal over LatticeDB `CALLS`, `IMPORTS`, and `REFERENCES` edges.
   - Weights hop distance ($1.0 \times 0.5^{d-1}$), symbol visibility (public vs private), and cross-domain boundaries.
2. **Risk Tiers**:
   - `CRITICAL` (Score >= 75): Widespread breaking risk across core domains.
   - `HIGH` (45 <= Score < 75): Extensive ripple effect.
   - `MODERATE` (20 <= Score < 45): Direct callers affected.
   - `LOW` (Score < 20): Localized change.
3. **Top 10 Breaking Points**:
   - Ranked list of the 10 most critical functions/files that will break if the contract changes.
