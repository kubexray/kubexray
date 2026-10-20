# Security Policy

kubexray runs with credentials that can read your whole cluster, so we take security reports seriously.

## Reporting a vulnerability

**Please do not open a public issue.** Report vulnerabilities privately through
[GitHub private vulnerability reporting](https://github.com/kubexray/kubexray/security/advisories/new).

Include what you found, how to reproduce it, and the impact you expect. You should get an acknowledgement within 3 business days and an assessment within 10. Fixes are released as soon as possible and credited in the advisory, unless you prefer to stay anonymous.

## Supported versions

kubexray is pre-1.0. Only the latest release receives security fixes.

## Scope

Of particular interest:

- Ways for a web page or remote client to reach a local kubexray instance (CSWSH, DNS rebinding, missing auth)
- Leaks of Secret values, environment variables or other sensitive fields into the UI, logs or replay files
- Script injection through cluster-controlled strings (names, labels, annotations)
- Cluster writes possible without `--allow-actions` or without RBAC permission
- Supply-chain issues in released binaries, images or the Helm chart

See [docs/threat-model.md](docs/threat-model.md) for the full threat model.
