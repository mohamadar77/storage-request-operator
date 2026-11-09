# Contributing

Thanks for your interest in the StorageRequest Operator.

## Before starting

1. Read [`docs/design.md`](docs/design.md), the authoritative v1.1 implementation contract.
2. Check [`docs/implementation-plan.md`](docs/implementation-plan.md) and existing issues before starting substantial work.
3. For behavior that is not specified, open a design issue or propose an ADR rather than inventing a controller policy.

## Non-negotiable safety invariants

Contributions must not:

- adopt a PVC based on name alone;
- mutate/delete a PVC unless request UID and recorded PVC UID checks pass;
- automatically recreate a previously observed missing PVC;
- shrink actual volume capacity;
- replace PVCs to accommodate immutable field changes;
- bypass PVC protection or delete workloads during finalization;
- remove finalizers owned by other controllers;
- imply that PVC retention is a backup or that deleting a PVC always deletes backend storage.

## Development workflow

The exact tool versions and commands will be pinned during scaffold milestone 1. Once the Go scaffold exists, the expected local checks will include:

```bash
make manifests
make generate
go fmt ./...
go vet ./...
go test ./...
```

Run envtest/controller integration tests where applicable. CSI-backed tests must use disposable namespaces and test storage only.

## Pull requests

- Keep changes focused and explain the user-visible or operational impact.
- Link the relevant issue and milestone.
- Add or update tests for new behavior and every safety boundary.
- Update `docs/design.md` first when changing the implementation contract; update summary docs in the same PR.
- Include generated CRD/RBAC changes when the API or markers change.
- Do not include credentials, kubeconfigs, backend secrets, or real production resource identifiers.
- State which checks were run and list any checks not run, with reasons.

## Design changes

A change to API semantics, ownership, resizing, finalization, or RBAC requires explicit design review. Record durable decisions under `docs/adr/` and update the canonical design document. Do not let an implementation detail silently redefine the contract.
