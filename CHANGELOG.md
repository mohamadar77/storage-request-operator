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
- Milestone 4: watch on managed PVCs (label or request-UID annotation, old or new object) mapped to the same-name request; `status.pvcPhase`, `status.volumeName`, and `status.capacity` projected from a verified claim and cleared for a missing or foreign one; read-only resize-state observation (`Resizing`, `ResizeFailed`); events.k8s.io/v1 Events once per state transition (`PVCRequested`, `PVCBound`, `PVCResizing`, and a Warning per problem reason). RBAC adds Event `create`/`patch`.
- Milestone 5 review follow-ups: a resize patch the API server refuses outright (`Invalid`/`Forbidden`) is reported as `ResizeRejected` instead of looping in backoff with no condition; the design's unbound-grow wording is corrected to say such an increase is deferred until the claim binds (Kubernetes forbids the request change before bind).
- Milestone 5: safe spec updates. Immutable StorageClass/access-mode/volume-mode changes are reported (`ImmutableFieldChange`) and never replace the claim. Capacity is grow-only: a larger size is deferred while unbound and, once bound, patched onto `PVC.spec.resources.requests.storage` only when the resolved StorageClass allows expansion (`ExpansionNotSupported`, `StorageClassNotFound` otherwise). A decrease is rejected (`ShrinkNotSupported`) except the design 6.4 recovery from a terminal resize failure to a target above the actual capacity (`ResizeRecoveryRejected` otherwise). RBAC adds PVC `patch` and StorageClass `get`.
- Milestone 6: finalization. On deletion the operator runs the request's `onDelete` policy behind its finalizer. `Retain` (default) detaches the owned PVC (`storage.example.io/orphaned-from` annotation, managed label removed) and leaves it; `Delete` removes only the exact owned PVC with a UID precondition and keeps the finalizer until the claim is `NotFound`. Ownership is proven from the request-UID annotation and recorded PVC UID; an unprovable claim is never deleted (Delete keeps the finalizer for an administrator; Retain leaves the claim and completes). Events `StorageRetained`/`PVCDeletionRequested`; RBAC adds PVC `delete`.

### Changed

- Docs: rewrote `FILE-INDEX.md` to map the actual repository and updated the README status note.

### Not yet implemented

- Remaining operational hardening (milestone 7).
- CSI-backed end-to-end tests (milestone 8).
