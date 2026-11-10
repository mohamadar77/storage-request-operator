# Changelog

All notable project changes will be documented here.

The format is inspired by [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and releases will use semantic versioning once release compatibility commitments are established.

## [Unreleased]

### Added

- Initial repository documentation package based on the approved StorageRequest Operator design v1.1.
- Architecture, API summary, implementation roadmap, testing strategy, operational safety, and release checklist.
- Contribution, security, support, and code-of-conduct guidance.
- Milestone 1: Kubebuilder project scaffold, manager entry point (leader election, health/readiness probes, auth-filtered metrics, zap structured logging), Makefile targets, generated CRD/RBAC/manager manifests, and generated CI workflows (lint, test, e2e).
- Developer guide (`docs/development.md`) with the pinned toolchain versions.
- Milestone 2: `StorageRequest` v1alpha1 API (`StorageRequestSpec`/`StorageSpec`/`LifecycleSpec`/`StorageRequestStatus`), CRD with CEL positive-quantity validation, enum/minItems/set/minLength constraints, server-side defaults (access modes, volume mode, `onDelete`), and the status subresource. Shared identity, finalizer, and condition constants. Defensive Go validation with table-driven unit tests, an envtest integration suite (CRD install, CEL, defaults, status subresource), and a filled sample manifest.

### Not yet implemented

- Reconciliation controller: PVC lifecycle, ownership, resize, and finalization (milestones 3-6).
- Least-privilege operator RBAC and operational hardening (milestone 7).
- CSI-backed end-to-end tests (milestone 8).
