## Summary

<!-- What changed and why? -->

## Design alignment

- [ ] This change follows `docs/design.md` v1.1.
- [ ] Any change to the implementation contract is documented and reviewed first.
- [ ] API/CRD/RBAC generated artifacts are included when applicable.

## Safety review

- [ ] PVC ownership checks use request UID and recorded PVC UID.
- [ ] No previously observed missing PVC can be silently recreated.
- [ ] No actual volume capacity is reduced.
- [ ] No PVC is replaced to work around immutable-field changes.
- [ ] Finalizer behavior remains idempotent and restart-safe.
- [ ] No unrelated PVC or another controller's finalizer can be modified.
- [ ] No new privilege or secret-handling path was introduced without review.

## Validation

- [ ] Unit tests
- [ ] `envtest` / controller integration tests (where applicable)
- [ ] CSI-backed E2E tests (where applicable)
- [ ] Formatting, generation, vet, and lint checks

**Checks run:**

**Checks not run and why:**

## Documentation

- [ ] README/docs updated where behavior or operations changed.
- [ ] Changelog updated when user-visible behavior changed.
