# Implementation plan

This roadmap translates the approved [design v1.1](design.md) into implementation work. It is a task plan, not evidence that any item has been completed.

## Milestone 1 — Kubebuilder scaffold

- [x] Initialize the Kubebuilder project for Go and controller-runtime.
- [x] Select and pin mutually compatible Go, Kubernetes libraries, Kubebuilder, and controller-runtime versions.
- [x] Record supported tool versions in the developer documentation and CI.
- [x] Configure project metadata, manager entry point, scheme, logging, health checks, readiness checks, and leader election.
- [x] Generate the initial CRD, RBAC, manager, and sample directories.
- [x] Verify `make manifests`, `make generate`, `make test`, and `go vet ./...` work in a clean checkout (`go test ./...` additionally needs `make setup-envtest` once for the envtest binaries).

## Milestone 2 — API types and validation

- [x] Define `StorageRequestSpec`, storage and lifecycle fields, and status fields in `api/v1alpha1`.
- [x] Add the status subresource and structural schema.
- [x] Add defaults for access modes, volume mode, and `onDelete`.
- [x] Add CEL positive-quantity validation using the Kubernetes 1.29+ quantity library.
- [x] Add enum, non-empty, minimum-item, and unique-item validation for the other fields.
- [x] Add defensive Go validation using `resource.ParseQuantity`, `Sign`, and supported enums.
- [x] Add a valid sample manifest and API validation tests.

## Milestone 3 — PVC creation and identity

- [x] Implement a deterministic PVC builder that sets only operator-owned fields.
- [x] Add the request UID annotation and managed labels.
- [x] Persist the finalizer before any PVC creation.
- [x] Create the PVC and immediately persist its UID to `status.pvcUID`.
- [x] Implement crash recovery for the interval between PVC creation and status persistence.
- [x] Enforce request UID and recorded PVC UID matching.
- [x] Handle `AlreadyExists` by re-reading and verifying identity; never auto-adopt.
- [x] Report `PVCMissing` instead of recreating a previously recorded claim.
- [x] Detect a PVC already marked for deletion while its request is active.
- [x] Test collisions, replacement, deletion, and retry paths.

Milestone 3 notes:

- Nothing is decided from the cache when a PVC appears to be absent. The controller re-reads the request from the API server before it creates a PVC, re-reads the PVC after `AlreadyExists`, and reports `PVCMissing` only after the API server confirms the recorded PVC is gone.
- Status and finalizer writes carry the resourceVersion they were computed from, so a decision made on a stale object is rejected as a conflict and retried.
- Finalization is Milestone 6. Until then a deleted `StorageRequest` keeps its finalizer and stays in `Terminating`; the controller does not remove the finalizer or touch the PVC.
- The controller does not watch PVCs yet (Milestone 4). Every active request, `Ready` ones included, is re-checked on a 30-second fallback requeue, so a PVC that is bound, deleted, replaced, or lost is noticed within that interval.
- `Ready` reflects PVC phase and reported capacity only; resize awareness arrives with Milestone 5. A `Lost` claim is reported as `Ready=False` and `Degraded=True`, reason `VolumeLost`.
- Kubernetes Events are added in Milestone 4. Conflicts are reported through conditions and logs for now.
- Stale-cache, failed-write, and restart paths are tested with client interceptors against envtest. They are not yet tested against a real informer cache under load, and no E2E test creates a `StorageRequest` yet, so the generated RBAC is not exercised in a real cluster.

## Milestone 4 — Watches, status, and events

- [ ] Watch `StorageRequest` objects.
- [ ] Watch managed PVC events and map each PVC to the same-namespace request name.
- [ ] Re-check identity on every mapped reconcile.
- [ ] Implement standard conditions, stable reasons, and accurate `observedGeneration`.
- [ ] Implement PVC phase, PV name, actual capacity, and resize-state projection.
- [ ] Preserve transition timestamps when conditions do not materially change.
- [ ] Emit events only for meaningful transitions and actionable failures.
- [ ] Add bounded fallback requeue and watch-predicate tests.

## Milestone 5 — Safe updates and resize

- [ ] Detect immutable StorageClass, access mode, and volume mode changes without replacing PVCs.
- [ ] Compare quantities with `resource.Quantity.Cmp`.
- [ ] Support initial size changes while a PVC is still unbound as specified by design v1.1.
- [ ] For bound PVC expansion, verify the resolved StorageClass and `allowVolumeExpansion: true`.
- [ ] Patch only `PVC.spec.resources.requests.storage`.
- [ ] Implement the narrow terminal failed-resize target recovery rule.
- [ ] Reject all other decreases and all recovery targets at or below actual capacity.
- [ ] Keep `Ready=False` while capacity is below target or resize work is pending/in progress.
- [ ] Surface terminal resize failure without repeatedly re-patching the same target.
- [ ] Test quantity equivalence, recovery boundaries, unsupported classes, and status conditions.

## Milestone 6 — Finalization

- [ ] Implement `Retain` finalization using both identity checks.
- [ ] Mark retained PVC metadata and detach it from active reconciliation as specified by design.
- [ ] Do not claim retention succeeded if another actor has already initiated PVC deletion.
- [ ] Implement `Delete` with PVC UID precondition.
- [ ] Keep the request finalizer until the exact PVC is `NotFound`.
- [ ] Treat mismatched ownership as a safety block; never delete an unverified claim.
- [ ] Test repeated finalization, restart, delayed PVC protection, and policy-at-deletion behavior.

## Milestone 7 — Hardening and deployment

- [ ] Generate least-privilege RBAC for requests, status, PVCs, StorageClasses, and Events.
- [ ] Verify no PV, Pod, Node, Secret, or CSI-resource permissions are granted.
- [ ] Configure leader election for multi-replica deployment.
- [ ] Add probes, secured metrics, resource requests/limits, and graceful shutdown.
- [ ] Scope caches/watches to authorized namespaces where supported by the deployment model.
- [ ] Review tenant policy, ResourceQuota, LimitRange, and StorageClass allowlisting guidance.
- [ ] Add structured logs without high-cardinality metric labels.

## Milestone 8 — E2E, documentation, and release

- [ ] Run envtest using the generated CRD.
- [ ] Run CSI-backed E2E tests on every supported platform/storage combination.
- [ ] Verify filesystem mount and marker-file read/write behavior.
- [ ] Verify expansion and recovery using disposable test storage.
- [ ] Verify both Retain and Delete with documented PV reclaim policies in isolated environments.
- [ ] Test operator restarts during provisioning, resizing, and finalization.
- [ ] Complete the platform support matrix and operational troubleshooting guide.
- [ ] Replace the placeholder API group before shared-cluster or public CRD release.
- [ ] Select and add a license before public contribution.
- [ ] Complete [`release-checklist.md`](release-checklist.md).

## Definition of done

A milestone is complete only when its deliverables and tests are committed and the acceptance criteria in `design.md` are satisfied. Do not mark a milestone complete based only on a successful local build.
