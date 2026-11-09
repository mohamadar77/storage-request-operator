# GitHub repository setup

Proposed repository metadata and settings for the project. These are recommendations, not changes already applied to GitHub.

## Repository identity

| Setting | Recommendation |
|---|---|
| Repository name | `storage-request-operator` |
| Description | `A Kubernetes Operator that manages one PersistentVolumeClaim per StorageRequest, with ownership-safe reconciliation, capacity expansion, status reporting, and explicit Retain/Delete finalization.` |
| Suggested topics | `kubernetes`, `kubernetes-operator`, `kubebuilder`, `controller-runtime`, `golang`, `persistent-volume-claim`, `pvc`, `csi`, `storageclass`, `openshift`, `cloud-native` |
| Default branch | `main` |
| Website | Leave unset until project documentation/site exists |
| Wiki | Disable; keep versioned documentation in this repository |
| Issues | Enable |
| Projects | Optional; useful once the implementation roadmap becomes active |
| Discussions | Optional; enable when there is a community to support |

## Visibility

Keep the repository private while the owner/domain decision, license choice, and initial scaffold are being settled. Make it public only after reviewing the license, API group, security posture, and all committed content. If this is intentionally a public portfolio/open-source project, public from the beginning is also reasonable once a license is selected and placeholder values are clearly marked.

## Branch and pull-request workflow

- `main` is the protected default branch.
- Use short-lived branches such as `feat/api-types`, `feat/pvc-controller`, `fix/finalizer-retry`, and `docs/operational-guide`.
- Require pull requests for changes to `main`; disallow force pushes and branch deletion for `main`.
- Enable squash merging and automatically delete merged head branches.
- Require CI checks after the Go scaffold and CI workflow are in place.
- Approval rule: for a solo-maintained repository, do not require an approval that the sole maintainer cannot provide on their own PR. Use zero required approvals with required passing checks, or add another trusted reviewer with write access and then require one approval.
- Keep PRs focused and link implementation work to an issue/milestone.

## Recommended labels

| Label | Purpose |
|---|---|
| `good first issue` | Small, well-scoped newcomer work |
| `help wanted` | Work where contributions are welcome |
| `bug` | Incorrect behavior |
| `enhancement` | New capability within approved scope |
| `documentation` | Documentation change |
| `design` | Design/ADR work |
| `api` | CRD or API behavior |
| `controller` | Reconciliation behavior |
| `storage` | PVC/StorageClass/CSI behavior |
| `security` | Security or RBAC concern; never use as a substitute for private vulnerability reporting |
| `testing` | Unit, envtest, or E2E work |
| `blocked` | Waiting on an explicit dependency or decision |
| `breaking-change` | API or user-visible incompatibility |

## Automation

- Add Go module and GitHub Actions Dependabot updates after the Go module and workflow files exist.
- Configure CI for formatting, generation/manifests drift, `go test`, `go vet`, linting, and envtest.
- Use least-privilege `permissions` on every workflow.
- Do not add a passing placeholder CI job that skips all checks before the Go project exists; add the real pipeline as part of scaffold milestone 1.

## Create the GitHub repository from this kit

The connected GitHub integration did not expose repository creation in this session. To create the repository under the account selected by your local GitHub CLI authentication:

```bash
unzip storage-request-operator-github-kit.zip
cd storage-request-operator-github-kit
git init -b main
git add .
git commit -m "chore: initialize StorageRequest Operator repository"
gh auth status
gh repo create storage-request-operator --private --source=. --remote=origin --push
```

The command creates a **private** repository. Change `--private` to `--public` only after selecting a license, replacing the placeholder API group for public CRD publication, and reviewing the security/release gates. The command uses the owner associated with your active `gh` authentication; verify `gh auth status` first.

After repository creation, add the About description and topics from the metadata table above, then configure the `main` branch rules. The workflow intentionally does not create GitHub Actions CI before the Go/Kubebuilder scaffold exists; add real formatting, generation, test, vet, lint, and envtest checks in milestone 1.

## License decision

**Not selected yet.** For a Kubernetes/Go open-source operator, Apache-2.0 is a sensible default recommendation because it includes an express patent license and is familiar in cloud-native projects. MIT is a simpler permissive alternative. The repository owner must select the license and add the canonical license text before external contributions or public release. This repository kit intentionally does not assert a license choice.

## Project metadata and releases

- Start with design/API version `v1alpha1`; do not imply a stable API until compatibility commitments are established.
- Use annotated tags for releases, for example `v0.1.0-alpha.1` for an explicitly pre-release implementation.
- Reserve `v1.0.0` for a release that has a stable API group, documented support matrix, and all production release gates complete.
- Keep `docs/design.md` as the canonical implementation contract. Update this setup page when repository policy changes.
