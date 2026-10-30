# 0021. ConfigMaps in full

- Status: Accepted
- Date: 2026-10-30
- Amends: [0017](0017-values-in-diffs.md)

## Context

ADR-0017 masks values that look like credentials, ConfigMaps included: `API_TOKEN` shows as "hidden: the name looks like a credential", and `user:password@` in a URL as `[redacted]`. For a ConfigMap this hides little. Anyone who may read it in kubexray may read it with `kubectl get configmap -o yaml` (ADR-0020), and it hides values people need to see during an incident, because what a ConfigMap holds is the thing that changed.

## Decision

Inside the cluster, kubexray keeps and shows ConfigMap values in full: no masking by name or by contents. The size caps stay (4 KiB a value, 64 KiB a ConfigMap), and ConfigMaps in `kube-system`, `kube-public` and `kube-node-lease` are still not kept.

Replays leave the cluster, so when a replay includes values, ConfigMap values are masked on export exactly as ADR-0017 describes. Anonymized replays still carry no values.

Environment variables, container commands and manifests are unchanged: still masked where they look like credentials.

## Consequences

- A ConfigMap edit shows exactly what changed, whatever the key is called.
- A credential someone put in a ConfigMap shows in kubexray to everyone who may read that ConfigMap, as it does in `kubectl`. Credentials belong in Secrets, which kubexray never reads.
- The recording holds ConfigMap values unmasked, so it is as sensitive as the ConfigMaps themselves.
