# Security policy

## Supported versions

The project has not published a release yet. Until a supported release policy is established, security fixes should target the current default branch. Once releases exist, list supported versions here.

## Reporting a vulnerability

Please do **not** disclose suspected vulnerabilities in public GitHub issues, pull requests, or discussions.

Use GitHub's private vulnerability reporting feature for this repository (the **Security** tab → **Report a vulnerability**) when it is enabled. Include affected versions/commit, impact, reproduction steps, and any suggested mitigation. Do not include production credentials, customer data, or storage backend secrets.

If private vulnerability reporting is unavailable, contact the repository maintainers privately through their GitHub profile and ask for a secure reporting channel. Do not post exploit details publicly while arranging contact.

## Security scope

Security-sensitive areas include:

- PVC ownership markers and recorded PVC UID checks;
- finalizer and deletion behavior;
- namespace-scoped request authorization and effective ability to create PVCs;
- StorageClass selection and expansion permissions;
- controller RBAC, metrics endpoints, and multi-tenant isolation.

The operator must not accept arbitrary PVC annotations, backend credentials, secrets, or vendor-specific parameters in v1. See the security and RBAC sections of [`docs/design.md`](docs/design.md).
