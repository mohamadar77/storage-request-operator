# Development guide

This guide covers local setup, the pinned toolchain, and the standard checks for
working on the StorageRequest Operator. The authoritative behavioral contract
remains [`docs/design.md`](design.md) v1.1.

## Pinned toolchain

The scaffold (milestone 1) pins the following mutually compatible versions. CI
uses the same versions; update this table and the referenced files together.

| Tool | Version | Pinned in |
|---|---|---|
| Go (minimum) | 1.26 | `go.mod` (`go` directive) |
| Go (CI) | 1.26.0 | `go.mod` (`go` directive), read by the workflows through `go-version-file` |
| Go (local development) | 1.27.3 | not pinned; builds and tests pass, but see the lint note below |
| Kubebuilder | 4.16.0 | `PROJECT` (`cliVersion`) |
| controller-runtime | v0.25.0 | `go.mod` |
| Kubernetes client libraries | v0.37.0 | `go.mod` (`k8s.io/*`) |
| controller-gen | v0.22.0 | `Makefile` (`CONTROLLER_TOOLS_VERSION`) |
| kustomize | v5.8.1 | `Makefile` (`KUSTOMIZE_VERSION`) |
| golangci-lint | v2.13.1 | `Makefile` (`GOLANGCI_LINT_VERSION`) |
| logcheck lint plugin (`sigs.k8s.io/logtools`) | v0.11.0 | `.custom-gcl.yml` |
| kind (E2E CI) | v0.33.0 | `.github/workflows/test-e2e.yml` (`KIND_VERSION`) |
| envtest Kubernetes assets | derived from `k8s.io/api` (1.37) | `Makefile` (`ENVTEST_K8S_VERSION`) |

**Lint needs Go 1.26.** golangci-lint v2.13.1 cannot read packages compiled by
Go 1.27 (it fails with `export data version 5 is greater than maximum supported
version 4`). With a newer local Go, run `GOTOOLCHAIN=go1.26.0 make lint`. Do not
add a `toolchain go1.27.x` directive to `go.mod` until golangci-lint is bumped
to a release that supports it, or the CI lint job breaks.

**Target platform baseline:** Kubernetes **1.29+** (design baseline). The client
libraries above are newer than 1.29 and remain compatible with a 1.29+ API
server. The release support matrix and the Kubernetes version used for
CSI-backed E2E are decided per the release checklist, not by this table.

## Prerequisites

- Go (see the minimum above). `GOTOOLCHAIN=local` is recommended so the build
  uses your installed Go rather than downloading a toolchain.
- `make`.
- For envtest-based integration tests: no cluster is required; `make test`
  downloads the API-server test binaries via `setup-envtest`. A bare
  `go test ./...` does not download them: run `make setup-envtest` once first,
  or the integration package exits with a message saying so.
- For CSI-backed E2E (milestone 8): Docker plus `kind` and a disposable
  StorageClass. These are not needed for unit or envtest work.

Install the project-local tools (into `./bin`) on demand through the Makefile
targets; they are not committed.

## Standard local checks

Run these before opening a pull request. They mirror the milestone-1 acceptance
gate and the CI workflows.

```bash
make manifests   # regenerate CRDs and RBAC from markers
make generate    # regenerate deepcopy code
go fmt ./...
go vet ./...
go test ./...    # unit + envtest (excludes build-tagged e2e); needs `make setup-envtest` once
```

Additional targets:

```bash
make build       # build the manager binary into ./bin
make lint        # golangci-lint (with Go 1.27 locally: GOTOOLCHAIN=go1.26.0 make lint)
make test-e2e    # CSI-backed E2E (requires kind + Docker); milestone 8
```

The `e2e` suite is guarded by a `//go:build e2e` tag, so `go test ./...` does not
run it. Run it explicitly with `make test-e2e` against a disposable cluster.

## Generated artifacts

Commit regenerated output alongside the change that caused it:

- `config/crd/bases/*` and `config/rbac/*` come from `make manifests`.
- `zz_generated.deepcopy.go` files come from `make generate`.

CI fails if generated files drift from the committed sources.

## Repository conventions

- `main` is protected; use short-lived `feat/*`, `fix/*`, or `docs/*` branches.
- Keep commits focused with conventional-style messages.
- Every behavior change must preserve the v1.1 safety contract or update the
  design first. See [`CONTRIBUTING.md`](../CONTRIBUTING.md).
