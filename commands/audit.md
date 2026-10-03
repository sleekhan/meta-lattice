---
description: Audit architecture boundaries, detect circular dependencies, and compute coupling metrics.
usage: /meta-lattice:audit [--file <path>]
---

# Architecture Boundary Auditor (`/meta-lattice:audit`)

Ensure clean layered architecture and prevent technical debt in monorepos:

1. **Layer Rule Enforcement**:
   - Controller -> Service -> Repository -> Model unidirectional flow.
   - Forbids inverted or bypassing dependencies (e.g. Repository depending on Controller).
2. **Circular Dependency Detection**:
   - Discovers direct and multi-hop cycles (`A -> B -> C -> A`) using Tarjan's SCC algorithm on LatticeDB edges.
3. **Coupling & Instability Metrics**:
   - Afferent Coupling ($C_a$), Efferent Coupling ($C_e$), and Instability metric ($I = \frac{C_e}{C_a + C_e}$).
