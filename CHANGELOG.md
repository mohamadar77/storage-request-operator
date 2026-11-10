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
- Milestone 1-2 review fixes: guarded size CEL rule (rejects pathological exponents before any quantity call, accepts integers), `status.pvcUID` immutable once set, CI check for stale generated files.
- Milestone 3: `StorageRequest` controller that persists its finalizer before creating a PVC, builds a deterministic PVC with identity labels and the request UID annotation (no owner reference), records the PVC UID in status immediately, and verifies identity on every reconcile. Same-name PVCs are never adopted (`PVCNameConflict`, `OwnershipMismatch`), a recorded PVC that disappears is never recreated (`PVCMissing`), and a terminating PVC is reported (`PVCDeleting`). Generated RBAC now grants only StorageRequest read/patch, status patch, and PVC read/create.

### Not yet implemented

- PVC watches, Kubernetes Events, and full status projection (milestone 4); resize (milestone 5); finalization (milestone 6). Until milestone 6 a deleted `StorageRequest` keeps its finalizer and stays in `Terminating`.
- Remaining operational hardening (milestone 7).
- CSI-backed end-to-end tests (milestone 8).
