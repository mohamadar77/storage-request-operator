# API reference (v1alpha1)

The normative API contract is [design v1.1](design.md). This page is a reader-friendly summary and must be updated alongside the design whenever API behavior changes.

## Resource identity

- Kind: `StorageRequest`
- Scope: Namespaced
- API version: `storage.example.io/v1alpha1` (placeholder group; must be replaced before shared-cluster use)
- Plural: `storagerequests`
- One request manages one PVC with the same name in the same namespace.
- Status subresource: enabled.

## Example

```yaml
apiVersion: storage.example.io/v1alpha1
kind: StorageRequest
metadata:
  name: app-data
  namespace: demo
spec:
  storage:
    size: 20Gi
    storageClassName: fast-ssd
    accessModes:
      - ReadWriteOnce
    volumeMode: Filesystem
  lifecycle:
    onDelete: Retain
```

## Spec fields

| Field | Required | Default | Behavior |
|---|---:|---|---|
| `spec.storage.size` | Yes | None | Positive Kubernetes quantity. Supports growth. A decrease is rejected except for the explicit terminal failed-resize recovery rule. |
| `spec.storage.storageClassName` | No | Omitted | Omission lets Kubernetes default the class, including retroactive default assignment. A supplied class is immutable from the operator API perspective. Empty string is rejected. |
| `spec.storage.accessModes` | No | `[ReadWriteOnce]` | Non-empty supported Kubernetes access modes; no duplicates; immutable after PVC creation. |
| `spec.storage.volumeMode` | No | `Filesystem` | `Filesystem` or `Block`; immutable after PVC creation. |
| `spec.lifecycle.onDelete` | No | `Retain` | `Retain` or `Delete`; may change while active. The value at deletion time controls finalization. |

Supported access modes are `ReadWriteOnce`, `ReadOnlyMany`, `ReadWriteMany`, and `ReadWriteOncePod`. A value accepted by the API does not guarantee support by the installed CSI driver or storage backend.

## Status summary

| Field | Meaning |
|---|---|
| `status.observedGeneration` | Spec generation last processed. |
| `status.pvcName` | Managed PVC name (same as request name). |
| `status.pvcUID` | UID of the exact PVC object incarnation; once set, retained for the request lifetime. |
| `status.finalizationPolicy` | The `onDelete` value captured when finalization began; immutable once set, so a later `onDelete` change cannot redirect deletion. |
| `status.pvcPhase` | Observed PVC phase (`Pending`, `Bound`, `Lost`) when a PVC exists. |
| `status.volumeName` | Bound PV name when available. |
| `status.capacity` | Actual capacity reported by PVC status, not the requested target. |
| `status.conditions` | Standard `metav1.Condition` values. |

## Conditions and reasons

`Ready=True` means the PVC is `Bound`, reported actual capacity is at least the requested target, and Kubernetes reports no resize operation still pending/in progress.

`Degraded=True` is used for persistent, actionable problems. Transient Kubernetes API failures should normally be returned from `Reconcile` for retry, rather than persisted as permanent degraded state.

Expected machine-readable reasons include:

- `Provisioning`, `Bound`, `Resizing`, `VolumeLost`
- `PVCNameConflict`, `OwnershipMismatch`, `ImmutableFieldChange`
- `ShrinkNotSupported`, `ExpansionNotSupported`, `StorageClassNotFound`
- `ResizeRecoveryRejected`, `PVCMissing`, `PVCDeleting`, `ResizeFailed`
- `InvalidSpec` (the spec failed the controller's defensive validation; no PVC was created or changed)
- `AsExpected` (the `Degraded=False` reason when no problem is detected)

The reason constants live in `api/v1alpha1/storagerequest_types.go`. Reasons implemented so far: `Provisioning`, `Bound`, `Resizing`, `ResizeFailed`, `VolumeLost`, `PVCNameConflict`, `OwnershipMismatch`, `PVCMissing`, `PVCDeleting`, `InvalidSpec`, `AsExpected`. `ImmutableFieldChange`, `ShrinkNotSupported`, `ResizeRecoveryRejected`, `ExpansionNotSupported`, `StorageClassNotFound`, and `ResizeRejected` are reported by milestone 5 when a spec change cannot be applied to the claim. `ResizeRejected` means the API server itself refused the request change (for example a statically provisioned claim, or a recovery decrease on a cluster without failed-expansion recovery); the operator reports it rather than retrying the same patch forever.

## Events

The controller publishes events.k8s.io/v1 Events on the `StorageRequest`, once per state transition: `PVCRequested` when it creates the PVC, `PVCBound` (Normal) when `Ready` becomes `True`, `PVCResizing` (Normal) when the `Ready` reason becomes `Resizing`, and a Warning whose reason is the `Degraded` reason when `Degraded` becomes `True` or changes reason (for example `PVCMissing` or `OwnershipMismatch`). During finalization it also emits `StorageRetained` (Normal, Retain detached the claim), `PVCDeletionRequested` (Normal, Delete requested the claim's removal), and a Warning (`OwnershipMismatch` or `PVCDeleting`) when the claim cannot be proven owned or is already terminating. Event notes are truncated to the 1024-byte API limit.

A request for a smaller size than an already provisioned PVC may remain usable: `Ready` can remain true when actual capacity still satisfies the request, while `Degraded=True` explains that shrinking is unsupported and the existing claim was left unchanged.

## Mutability summary

| Field | Mutable after PVC creation? |
|---|---|
| Requested size | Increase; narrow failed-resize recovery only as specified in design v1.1 |
| StorageClass | No migration; an explicitly supplied value must match the existing PVC class |
| Access modes | No |
| Volume mode | No |
| `onDelete` | Yes while request is active; effective value at deletion time is used |

## Validation

Use CRD schema and CEL quantity validation for the Kubernetes 1.29+ baseline, with defensive Go validation. Use `resource.Quantity` for all quantity comparisons. Do not add arbitrary PVC templates or pass-through fields in v1.
