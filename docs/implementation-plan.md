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

- [x] Watch `StorageRequest` objects.
- [x] Watch managed PVC events and map each PVC to the same-namespace request name.
- [x] Re-check identity on every mapped reconcile.
- [x] Implement standard conditions, stable reasons, and accurate `observedGeneration`.
- [x] Implement PVC phase, PV name, actual capacity, and resize-state projection.
- [x] Preserve transition timestamps when conditions do not materially change.
- [x] Emit events only for meaningful transitions and actionable failures.
- [x] Add bounded fallback requeue and watch-predicate tests.

Milestone 4 notes:

- PVC events pass the watch predicate when the old or new object carries the `storage.example.io/managed=true` label or the request-UID annotation, so label removal, annotation removal, and deletion-timestamp updates still reconcile. `StorageRequest` events are not filtered. The mapping is by namespace and name only; identity is decided in `Reconcile`.
- An unmarked same-name PVC does not pass the predicate. Its appearance or removal (for example, a `PVCNameConflict` clearing) is noticed by the 30-second fallback requeue, which every active request keeps.
- Resize state is observed, not acted on: `Ready=False`, reason `Resizing`, while `status.allocatedResourceStatuses["storage"]` or the PVC resize conditions report pending or in-progress work, or while a bound claim reports less capacity than requested. An infeasible allocated-resource status (or its alpha name `ControllerResizeFailed`/`NodeResizeFailed`) sets `Degraded=True`, reason `ResizeFailed`. The controller still never patches the PVC size; that is Milestone 5.
- Events are published through the events.k8s.io/v1 API and emitted only after the status write that records a new state succeeds, so each transition yields one Event and a rejected write yields none. Ready and Degraded are judged independently: `PVCBound` when Ready becomes `True`, `PVCResizing` when Ready's reason becomes `Resizing`, and a Warning whose reason is the Degraded reason when Degraded becomes `True` or changes reason. `PVCRequested` is emitted on PVC creation. `OwnershipMismatch`, `VolumeLost`, and `InvalidSpec` Warnings are in addition to the design's Event list.
- Event notes are truncated to the 1024-byte events.k8s.io/v1 limit, because the API server rejects a longer note and the recorder never retries it. PVC condition text copied into status is bounded as well (512 bytes per condition, 2048 bytes in total).
- The live request read when the cache shows no PVC is validated before anything else uses it. While the spec is invalid the projected claim fields are cleared, because the PVC is not read. `PVCMissing` names the last observed PersistentVolume.
- Condition logic lives in `internal/conditions` and resize observation in `internal/storage/resize.go`, as laid out in design section 9.1.

## Milestone 5 — Safe updates and resize

- [x] Detect immutable StorageClass, access mode, and volume mode changes without replacing PVCs.
- [x] Compare quantities with `resource.Quantity.Cmp`.
- [x] Support initial size changes while a PVC is still unbound as specified by design v1.1.
- [x] For bound PVC expansion, verify the resolved StorageClass and `allowVolumeExpansion: true`.
- [x] Patch only `PVC.spec.resources.requests.storage`.
- [x] Implement the narrow terminal failed-resize target recovery rule.
- [x] Reject all other decreases and all recovery targets at or below actual capacity.
- [x] Keep `Ready=False` while capacity is below target or resize work is pending/in progress.
- [x] Surface terminal resize failure without repeatedly re-patching the same target.
- [x] Test quantity equivalence, recovery boundaries, unsupported classes, and status conditions.

Milestone 5 notes:

- Immutable-field and resize logic is pure and lives in `internal/storage/pvc_validation.go` (`CheckImmutableFields`) and `internal/storage/resize.go` (`PlanResize`), as laid out in design section 9.1. The controller patches only `PVC.spec.resources.requests.storage`, with the claim's resourceVersion as an optimistic lock, and never replaces a claim.
- `CheckImmutableFields` compares StorageClass, access modes (as a set), and volume mode, applying the same defaults the builder uses. An omitted `spec.storage.storageClassName` matches any class the claim has, so Kubernetes retroactive default assignment is not a change. Every differing field is named in one `ImmutableFieldChange` message; nothing is patched.
- `PlanResize` is grow-only. A larger size on an unbound claim is deferred (Kubernetes rejects a request change on an unbound PVC) and applied once bound. A larger size on a bound claim requires the resolved StorageClass to set `allowVolumeExpansion: true`, else `ExpansionNotSupported` or `StorageClassNotFound`; it then patches only the storage request. The StorageClass is read live (not cached) and only when a resize is actually pending, so steady-state reconciles read no StorageClass.
- A decrease is rejected (`ShrinkNotSupported`) unless it is the design 6.4 recovery: the claim is bound, Kubernetes reports a terminal resize failure (`*ResizeInfeasible`, or the alpha `*ResizeFailed` names), the actual capacity is known, and the new target is strictly above it. A lower target without a terminal failure is `ResizeRecoveryRejected`. Recovery still requires expansion to be allowed.
- A rejected size change or a changed immutable field is a `Degraded=True` problem layered on the claim assessment: `Ready` keeps the claim's own state, so a claim that still satisfies the request stays `Ready=True` while `Degraded` explains the refusal (design section 4.4).
- If the API server itself refuses the resize patch (`Invalid`/`Forbidden`, for example a statically provisioned claim, or a recovery decrease on a cluster without `RecoverVolumeExpansionFailure`), the controller reports `ResizeRejected` rather than returning the error and retrying the identical patch in backoff forever. Other patch errors are still returned for retry.


## Milestone 6 — Finalization

- [x] Implement `Retain` finalization using both identity checks.
- [x] Mark retained PVC metadata and detach it from active reconciliation as specified by design.
- [x] Do not claim retention succeeded if another actor has already initiated PVC deletion.
- [x] Implement `Delete` with PVC UID precondition.
- [x] Keep the request finalizer until the exact PVC is `NotFound`.
- [x] Treat mismatched ownership as a safety block; never delete an unverified claim.
- [x] Test repeated finalization, restart, delayed PVC protection, and policy-at-deletion behavior.

Milestone 6 notes:

- Finalization lives in `internal/controller/finalize.go`. The PVC is read live (`APIReader`), never from the cache, because a deletion decision must not act on a stale view. Ownership is proven only when `CheckIdentity` returns `Verified` or `Unrecorded` (request-UID annotation matches, and the recorded PVC UID matches or is not yet recorded). The `onDelete` value is frozen once the request is deleting, so the cached request is authoritative for the policy.
- `Retain` detaches the owned claim: it adds `storage.example.io/orphaned-from=<request UID>` and removes the active `storage.example.io/managed` label, then removes the finalizer. The claim and its data stay. An unprovable same-name claim is left untouched and finalization still completes (Retain changes nothing anyway); a claim already terminating under another actor gets a warning and is not falsely reported as retained.
- `Delete` deletes only the exact claim, with a `metav1.Preconditions{UID}` so a same-name replacement is never deleted, and keeps the finalizer until the API server confirms the claim is `NotFound`. A claim whose ownership cannot be proven is a safety block: the finalizer is kept (the request stays `Terminating`) for an administrator to resolve, never deleted. PVC protection delaying deletion is waited out, never bypassed.
- RBAC adds `persistentvolumeclaims` `delete`. Events: `StorageRetained`, `PVCDeletionRequested`, and Warnings (`OwnershipMismatch`, `PVCDeleting`) during finalization.
- The deletion-time `onDelete` is frozen through the immutable `status.finalizationPolicy` (design section 7.4): the first finalization reconcile records the current policy, and every later reconcile finalizes from the recorded value. CRD CEL cannot read `metadata.deletionTimestamp`, so the freeze lives in status; this closes the window where a concurrent `onDelete` flip (for example GitOps self-heal) could redirect a terminating request to delete a data-bearing claim.

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
