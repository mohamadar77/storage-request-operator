# Release checklist

Do not publish a shared-cluster or production release until all applicable items are complete.

## API and compatibility

- [x] API group set to `storage.base-domain.io`, using a domain controlled by the project.
- [x] API group is fixed for v1alpha1; it will not be renamed after consumers exist (a rename is a breaking migration).
- [ ] Publish the exact Kubernetes version support matrix (design baseline: 1.29+).
- [ ] Publish exact OpenShift releases only after validation against bundled Kubernetes APIs and CSI stacks.
- [ ] Record the supported CSI drivers, StorageClasses, access modes, and expansion behavior.

## Implementation correctness

- [ ] All acceptance criteria in section 12 of `docs/design.md` pass.
- [ ] Finalizer is persisted before PVC creation.
- [ ] Request UID and recorded PVC UID checks protect every mutation/deletion.
- [ ] Previously observed missing PVCs are not recreated automatically.
- [ ] Resize behavior matches the ordinary grow path and narrow failed-resize recovery rule.
- [ ] Retain/Delete behavior is idempotent and restart-safe.
- [ ] No owner reference can bypass retention policy.

## Security and operations

- [ ] RBAC is least-privilege and excludes PV, Pod, Node, Secret, and CSI-resource management.
- [ ] Namespace permissions and quotas are documented.
- [ ] StorageClass selection policy is reviewed for multi-tenant environments.
- [ ] Metrics endpoints are secured and metric labels have bounded cardinality.
- [ ] Probes, leader election, resource requests/limits, and graceful shutdown are configured.
- [ ] Operational recovery and manual finalizer guidance are reviewed.

## Quality and release artifacts

- [ ] Unit tests and envtest pass.
- [ ] CSI-backed E2E tests pass for each supported matrix entry.
- [ ] Reclaim-policy implications have been tested only in isolated disposable environments.
- [ ] CI is green on the release commit.
- [ ] Changelog and release notes are prepared.
- [x] A license is selected and added: Apache-2.0 (`LICENSE`).
- [ ] Image provenance, signing, and vulnerability scanning requirements are decided for the release process.
