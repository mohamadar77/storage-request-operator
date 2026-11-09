# Operational safety contract

This document highlights the data-safety behaviors in [design v1.1](design.md). The design document remains authoritative.

## PVC ownership

The managed PVC must have:

- `app.kubernetes.io/managed-by=storage-request-operator`
- `storage.example.io/managed=true`
- `storage.example.io/request-uid=<StorageRequest UID>`

The request records the exact child object's UID in `status.pvcUID`. Both the request marker and recorded PVC UID must match before an existing PVC is mutated or deleted. Do not use name-only adoption. RBAC must prevent tenants from forging or changing managed metadata.

## Missing or replaced PVC

If `status.pvcUID` is already set and the PVC becomes `NotFound`, report `PVCMissing`; do not automatically create a replacement. If a same-name object appears with a different UID, report an ownership/replacement conflict and do not adopt it. This avoids silently presenting an empty volume as the old data-bearing claim.

## Capacity changes

- Never patch PV capacity or PVC status.
- Use `resource.Quantity.Cmp`; do not compare quantity strings or floats.
- A normal bound-claim increase requires a resolved StorageClass with `allowVolumeExpansion: true`.
- Patch only the PVC storage request.
- Never reduce actual volume capacity.
- The only lower target exception is terminal failed-resize recovery, and the new target must be strictly greater than actual reported capacity.
- Do not continuously retry the same terminally failed resize target.

## Deletion

The PVC intentionally has no owner reference. The operator finalizer owns cleanup orchestration.

### Retain

`Retain` is the default. On request deletion, verify both identities, mark the exact PVC as retained/orphaned, remove its active managed marker, and leave the PVC intact. If the PVC is absent, finalization is complete. If ownership does not match, do not modify the PVC. If another actor already started deleting it, emit a warning and do not claim that it was retained successfully.

### Delete

`Delete` deletes only the exact PVC whose request UID annotation and recorded PVC UID match. Use a PVC UID precondition and keep the request finalizer until the PVC is `NotFound`. Respect PVC protection; never delete consuming Pods or bypass protection.

Deleting the PVC is not equivalent to guaranteed backend deletion. The PV reclaim policy determines what happens to the backing storage. `Retain` is not a backup.

## Recovery rules

- Transient Kubernetes API errors: return an error for controller-runtime retry.
- Ownership mismatch: stop destructive action and surface an actionable condition/event.
- Operator restart: reconstruct all state from API objects; do not rely on memory.
- Never remove another controller's finalizer.
- Do not provide a normal-operation force-cleanup switch.
- Manual finalizer removal is an administrator recovery procedure only after understanding and handling PVC/backend implications.
