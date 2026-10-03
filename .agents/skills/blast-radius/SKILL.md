---
name: blast-radius
description: Simulates blast radius and ripple effects before changing symbols using Meta-Lattice in Go
---

# Blast Radius Estimator

Use `estimate_blast_radius` when modifying core interfaces or symbols:
- Identifies Top 10 breaking change points via reverse BFS traversal
- Computes normalized Blast Score (0-100) and risk tier (CRITICAL, HIGH, MODERATE, LOW)
- Visualizes impact cascade tree
