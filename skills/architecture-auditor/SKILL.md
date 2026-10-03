---
name: architecture-auditor
description: Guidelines for detecting layer boundary violations, circular dependencies, and high coupling using LatticeDB.
---

# Architecture Boundary Auditor Skill

## Purpose
Monorepos and enterprise applications decay when layers are bypassed (e.g. Repository importing Controller) or circular dependencies are introduced.

## When to Use
- **Before Committing Code**: Ensure newly added imports do not violate layered architecture or create cyclic imports.
- **After Adding Imports**: Call `check_layer_violation(file_path="...")` on the edited file.
- **Architecture Health Checks**: Call `check_layer_violation()` to audit the entire repository.

## Layer Rules
- **Controller / Presentation Layer**: Can depend on Services and Domain Models.
- **Service / Application Layer**: Can depend on Repositories and Domain Models. Cannot depend on Controllers.
- **Repository / Infrastructure Layer**: Can depend on Models/Entities. Cannot depend on Services or Controllers.
- **Domain / Model Layer**: Must remain pure and decoupled.

## Remediation Patterns
- **If Circular Dependency Detected**: Extract shared interfaces or DTOs into a separate leaf module, or apply Dependency Inversion Principle (DIP).
- **If Layer Violation Detected**: Pass dependencies through constructor injection or interfaces rather than importing upper layers.
