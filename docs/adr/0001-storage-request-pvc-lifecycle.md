# ADR 0001: StorageRequest manages one same-namespace PVC

- **Status:** Accepted
- **Date:** 2026-11-09
- **Source:** Approved StorageRequest Operator design v1.1

## Context

The project needs a declarative, namespaced API for requesting persistent storage without implementing or replacing Kubernetes storage provisioning. The lifecycle must avoid accidental adoption, replacement, or deletion of data-bearing claims.

## Decision

A `StorageRequest` manages exactly one PVC in the same namespace, using the same name. Kubernetes storage controllers and the CSI driver remain responsible for binding and provisioning. The operator uses request UID metadata plus a recorded PVC UID for identity and does not set an owner reference. A finalizer implements explicit `Retain` or `Delete` behavior, with `Retain` as the default.

Capacity increases are supported when the StorageClass permits expansion. Actual volume capacity is never reduced. Immutable PVC fields do not trigger replacement. A previously observed missing PVC is reported and is not automatically recreated.

## Consequences

- The API is intentionally narrow and easy to reason about.
- The controller must persist finalizer and child identity carefully because Kubernetes does not provide a transaction spanning the request and PVC.
- Retain/Delete finalization and PVC identity need thorough restart and collision tests.
- Snapshots, backups, migration, workload management, and CSI integration are out of scope for v1.
- Storage behavior remains dependent on the installed StorageClass and CSI driver.

## Alternatives rejected

- Direct PV or backend provisioning by this operator: duplicates Kubernetes/CSI responsibilities and expands privileges.
- Owner reference from PVC to request: could let garbage collection bypass explicit retention behavior.
- Automatic adoption by name: unsafe when a claim already exists or a request is recreated.
- Automatic PVC replacement after immutable changes or missing claim: risks data loss or silently creating an empty volume.
