# Testing strategy

The test strategy follows [design v1.1](design.md). A fake client is useful for fast logic tests but cannot prove behavior of a real CSI driver.

## Unit tests

Cover pure helpers and controller decisions:

- PVC builder: namespace/name, requested size, class omission/presence, access modes, volume mode, labels, and request UID annotation.
- StorageClass omission and Kubernetes defaulting behavior assumptions.
- Ownership: missing/wrong request UID, matching request UID, recorded PVC UID match/mismatch, and same-name replacement.
- Immutable-field detection without PVC mutation or replacement.
- Quantity comparisons, including equivalent representations such as `1Gi` and `1024Mi`.
- Capacity increase, no-op, ordinary shrink rejection, and every failed-resize recovery boundary.
- Expansion checks for missing class, disabled expansion, and allowed expansion.
- Ready/Degraded conditions for Pending, Bound, Lost, resizing, failed resize, missing PVC, and immutable changes.
- Retain behavior, including metadata detachment and an already-terminating PVC.
- Delete behavior, UID preconditions, delayed deletion, and repeated finalization.
- Idempotency and retries after status-write/API failures.

## API and controller integration tests

Use `envtest` with the generated CRD to verify:

- CRD schema, CEL validation, and defaults.
- Status subresource behavior.
- Finalizer persistence and deletion timestamps.
- PVC create/update/delete API interactions.
- Optimistic concurrency and conflict retry behavior.
- PVC watch-to-request mapping and predicates.
- Crash recovery between PVC create and `status.pvcUID` persistence.

`envtest` does not provide a CSI driver or real volume provisioner. Do not claim it proves successful provisioning or mounting.

## CSI-backed end-to-end tests

Use disposable namespaces and storage only:

1. Create a request and verify one same-namespace PVC is created.
2. Verify the PVC becomes `Bound` and the request becomes `Ready=True`.
3. Mount the PVC from a test Pod and write/read a marker file for `Filesystem` mode.
4. Increase requested capacity where the selected StorageClass supports expansion.
5. Verify `Ready` remains false until actual capacity and resize state converge.
6. Verify an ordinary shrink is rejected without reducing actual capacity.
7. Exercise failed-resize recovery only when the backend can safely reproduce the terminal failure state.
8. Verify Retain leaves the exact PVC intact and detached after request deletion.
9. Verify Delete removes only the exact owned PVC after consumers stop.
10. Test both PV reclaim policies only in isolated test environments and verify backend implications explicitly.
11. Restart the operator during provisioning, resize, and finalization; verify safe convergence.

### Running the suite

The suite lives in `test/e2e/` behind the `//go:build e2e` tag and runs with
`make test-e2e`, which creates a disposable Kind cluster, builds and loads the
manager image, deploys the operator, and runs the specs. It needs Docker and
`kind`; it does not run under `make test`.

`test/e2e/storagerequest_test.go` covers the StorageRequest lifecycle: create →
`Bound` → `Ready=True`, marker-file persistence across two pods (steps 1–3),
ordinary-shrink rejection (step 6), and Retain/Delete finalization (steps 8–9).
Environment variables:

- `E2E_STORAGE_CLASS` — the StorageClass for the lifecycle tests. Empty uses the
  cluster default (on Kind, the local-path provisioner).
- `E2E_EXPAND_STORAGE_CLASS` — a StorageClass with `allowVolumeExpansion: true`
  for the expansion test (step 4). The test is skipped when unset, because the
  default Kind provisioner cannot expand; install a CSI driver that supports
  expansion (for example `csi-driver-host-path`) and point this at its class.

Not yet automated in the suite, pending backend-specific setup: failed-resize
recovery (step 7, needs a driver that can reproduce a terminal resize failure),
explicit PV reclaim-policy verification (step 10), and operator-restart
convergence (step 11). The controller-side behavior of all three is covered by
the envtest suites; the E2E gaps are tracked in the milestone-8 issues.

## Acceptance gate

The complete acceptance checklist is in section 12 of `design.md`. All criteria must pass before production deployment. Record the cluster version, OpenShift release where applicable, CSI driver, StorageClass configuration, and test outcome for each E2E matrix entry.
