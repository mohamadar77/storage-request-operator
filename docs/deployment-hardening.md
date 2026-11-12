# Deployment hardening

This guide collects the operational hardening for running the StorageRequest
operator (milestone 7). The authoritative contract is [`design.md`](design.md)
§10 (RBAC and security) and §11 (availability, scaling, operations); this page
is how the shipped manifests and flags realize it.

> The operator is not production-ready until milestones 7 and 8 and the
> [release checklist](release-checklist.md) are complete, including replacing
> the placeholder `storage.example.io` API group.

## Least-privilege RBAC

The generated manager role (`config/rbac/role.yaml`) grants only:

| API group | Resource | Verbs | Why |
|---|---|---|---|
| `""` (core) | `persistentvolumeclaims` | get, list, watch, create, patch, delete | Create, observe, resize, detach, and delete the managed PVC |
| `storage.k8s.io` | `storageclasses` | get | Check `allowVolumeExpansion` before a bound-claim resize |
| `events.k8s.io` | `events` | create, patch | Publish user-visible Events |
| `storage.example.io` | `storagerequests` | get, list, watch, patch | Read requests and maintain finalizers |
| `storage.example.io` | `storagerequests/status` | get, patch | Publish status |

It grants **no** access to PersistentVolumes, Pods, Nodes, Secrets,
`VolumeAttachment`, or any CSI resource (design §10.1). The controller writes
only through optimistic-lock patches, so `update` is not granted on the custom
resource. Keep it that way: regenerate with `make manifests` after changing the
`+kubebuilder:rbac` markers, and review the diff.

The exposed `storagerequest_{admin,editor,viewer}` roles are for granting tenants
access to the custom resource; `metrics_auth_role` and `metrics_reader_role`
gate the metrics endpoint.

## Namespace-scoped vs cluster-scoped install

By default the manager caches and watches **all** namespaces, so its cache holds
every managed PVC in the cluster. To restrict it:

- Pass `--watch-namespaces=ns-a,ns-b`. The informer cache is then limited to
  those namespaces (design §11), which lowers memory use and the blast radius.
- Bind the manager's permissions with namespaced `RoleBinding`s in those
  namespaces instead of the cluster-wide `ClusterRoleBinding`, **except**
  `storageclasses` `get`, which must stay a `ClusterRole` binding because
  StorageClasses are cluster-scoped.

Leave `--watch-namespaces` empty for a cluster-wide install (the default
manifests), and size resources accordingly (below).

## Availability and leader election

- Run with `--leader-elect` (set in `config/manager/manager.yaml`). Run one
  active replica; add standbys for faster recovery. Leader election ensures a
  single active reconciler.
- The manager sets `LeaderElectionReleaseOnCancel`, so a leader steps down on
  graceful shutdown and the next replica takes over without waiting out the full
  lease. This is safe here because every action is idempotent and no work runs
  after the manager stops; the binary exits immediately when `Start` returns.
- `terminationGracePeriodSeconds: 10` and the signal handler let an in-flight
  reconcile stop cleanly. Correctness does not depend on finishing a reconcile
  before shutdown — a restarted or newly elected leader rebuilds all state from
  the API server.

## Probes and metrics

- Liveness and readiness probes are served on `:8081` (`/healthz`, `/readyz`).
- Metrics are served securely by default (`--metrics-secure=true`) behind
  authentication/authorization (`filters.WithAuthenticationAndAuthorization`);
  restrict access to monitoring components with the metrics roles and the
  provided `NetworkPolicy`. HTTP/2 is disabled on the metrics and webhook
  servers to avoid the Rapid Reset / Stream Cancellation CVEs.
- Only controller-runtime's standard reconcile metrics are exposed. Object
  identity (request/PVC names, UIDs, namespaces) is never used as a metric
  label; it goes to logs and Events instead, to avoid high-cardinality metrics
  (design §11.1).

## Resource sizing

The default `resources` in `config/manager/manager.yaml` (limit 256Mi memory,
request 128Mi; limit 500m CPU, request 10m) target the cluster-scoped install,
whose cache grows with the number of managed PVCs. For a namespace-scoped
install you can lower the limit; for a very large cluster, measure reconcile and
cache memory under load and raise it. Set requests and limits from measured use,
not guesses.

## Pod security

The manager pod runs `runAsNonRoot: true` with a `RuntimeDefault` seccomp
profile; the container drops all capabilities, disables privilege escalation,
and uses a read-only root filesystem. It mounts no host paths and needs no CSI
socket access.

## Tenant and storage policy

Permission to create a `StorageRequest` is effectively permission to request
storage within the operator's limits (design §10.2). For multi-tenant clusters:

- Grant `storagerequests` write access only to the users/workloads allowed to
  consume storage in a namespace (use the `storagerequest_editor`/`admin`
  roles).
- Apply a `ResourceQuota` for PVC count and total requested storage per
  namespace, and a `LimitRange` for per-claim minimum/maximum size.
- Restrict selectable StorageClasses through admission policy or a
  deployment-level allowlist when tenants must not use every class; treat
  StorageClass names and access modes as policy-sensitive inputs.
- The operator accepts no arbitrary PVC annotations, volume attributes, secrets,
  or backend parameters from the custom resource in v1.
