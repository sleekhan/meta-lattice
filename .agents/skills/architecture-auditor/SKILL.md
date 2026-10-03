---
name: architecture-auditor
description: Audits layered architecture boundaries and circular dependencies using Meta-Lattice in Go
---

# Architecture Boundary Auditor

Use `check_layer_violation` before finishing multi-file refactoring:
- Detects circular dependencies (e.g. A -> B -> C -> A) using Tarjan SCC
- Flags forbidden layer imports (e.g. repository importing controller)
- Reports Afferent (Ca), Efferent (Ce), and Instability (I) metrics
