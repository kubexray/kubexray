# Threat Model

kubexray connects to the Kubernetes API with the user's (or a ServiceAccount's) credentials and streams cluster state to a browser UI. Those credentials can typically read the entire cluster, which makes kubexray a high-value target. This document lists what we protect, who we protect it from, and the controls that must hold.

It is a living document: update it whenever a change adds a listener, an API endpoint, a new data source, or a cluster write.

## Assets

| Asset | Why it matters |
|---|---|
| Cluster credentials (kubeconfig, ServiceAccount token) | Read, and with actions enabled write, access to the cluster |
| Secret values | Passwords, tokens, keys |
| Sensitive fields outside Secrets | Env var values, `kubectl.kubernetes.io/last-applied-configuration` (may embed Secret manifests), container logs |
| Cluster topology | Reconnaissance value: names, images, versions, network paths |
| Replay files | Snapshot of all of the above, designed to be shared |

## Deployment modes

1. **Local**: `kubexray` on a workstation, using the current kubeconfig context, UI served to the local browser.
2. **In-cluster**: installed via Helm, ServiceAccount with a read-only ClusterRole, UI reached through port-forward, Ingress, or a proxy.
3. **Hosted replay**: static UI on kubexray.com playing replay files. No cluster access at all.

## Trust boundaries

```
 Browser (other tabs, other sites)  ──►  kubexray UI/WebSocket  ──►  kubexray backend  ──►  Kubernetes API
        untrusted                          boundary A                                 boundary B
 Cluster object contents (names, labels, annotations, logs) ──► rendered in UI      boundary C
 Replay file ──► hosted / local UI                                                    boundary D
```

## Threats and controls

### A. Reaching the backend

| Threat | Control |
|---|---|
| A malicious web page opens a WebSocket to `localhost` (cross-site WebSocket hijacking) | Strict `Origin` allow-list; per-launch random session token required on every HTTP and WebSocket request (passed in the launch URL, then held in memory) |
| DNS rebinding against the local listener | Validate the `Host` header against the bound address |
| Other users or hosts on the network | Bind to `127.0.0.1` by default; binding elsewhere requires an explicit flag and a warning |
| Unauthenticated access to an in-cluster install | No anonymous mode in Helm; require a bearer token, OIDC sign-in with a required allow-list (ADR-0013), or an auth proxy (e.g. oauth2-proxy); chart ships a NetworkPolicy |
| Sign-in CSRF, code interception, token replay | Authorization code flow with PKCE, `state` and `nonce` in an HMAC-signed short-lived cookie; ID tokens verified against the provider's keys; verified email required; return paths kept on-site |
| Guessing tokens or forcing sign-ins | 48-character random tokens; bad bearer tokens rate limited per client address; sign-in codes single-use and bound to PKCE; audit log of sign-ins, denials and exports |
| Unverified emails passing an allow-list | Email and domain rules only match emails the provider marks verified; group rules use the groups claim |
| Resource exhaustion by a client | Connection limit, max message size, bounded per-client send buffers with back-pressure / disconnect |

### B. Talking to the cluster

| Threat | Control |
|---|---|
| Over-privileged access | Default ClusterRole is `get/list/watch` only; Secrets watched via the **metadata-only** client so values are never fetched |
| Unintended writes | All mutations disabled unless `--allow-actions`; every action checked with `SelfSubjectAccessReview`, confirmed in the UI, and audit-logged |
| Credential leakage | Credentials never sent to the UI or logged; error messages from client-go sanitised before display |
| API server overload on large clusters | Shared informers, single watch per resource type, pagination on initial list |

### C. Rendering cluster data

| Threat | Control |
|---|---|
| Script injection through names, labels, annotations, events or logs (relevant in multi-tenant clusters) | All cluster strings rendered as text; `dangerouslySetInnerHTML` and equivalent banned by lint; strict Content-Security-Policy with no inline scripts |
| Sensitive fields shown in the UI | Server-side scrubbing before data leaves the backend: env values, `last-applied-configuration`, Secret data, ServiceAccount token projections. Diffs (ADR-0015) carry environment variable names, sources and fingerprints keyed with a random per-run key that is never stored. With values on (ADR-0017, the default), literal env values, commands and ConfigMap contents are kept for the signed-in UI: credential-looking names and contents are masked before they reach the graph, system namespaces are skipped, Secrets are never read, and values are stripped from replays (unless included on export), anonymized replays, alerts and the AI model |
| Logs exposing secrets | Log tails are opt-in per request and require `pods/log` permission via `SelfSubjectAccessReview` |

### D. Replay files

| Threat | Control |
|---|---|
| Sharing a replay leaks sensitive data | Export reuses the same scrubbing as the UI stream; optional anonymisation of names and namespaces |
| Malicious replay file attacks the viewer | Replay parsed against a versioned schema with size limits; contents treated as untrusted (same rendering rules as C) |

### Supply chain

| Threat | Control |
|---|---|
| Compromised dependency or action | Actions pinned by SHA; Dependabot; govulncheck, CodeQL and OpenSSF Scorecard in CI; minimal workflow permissions |
| Tampered release | Reproducible `-trimpath` builds; images signed with cosign; SBOM and provenance attached (v0.3) |
| Leaked secrets in the repo | gitleaks on full history in CI; GitHub secret scanning with push protection |
| Container breakout impact | Distroless static image, non-root uid 65532; Helm sets read-only root filesystem, drops all capabilities, `RuntimeDefault` seccomp |

## Out of scope

- A user who already has equivalent cluster credentials (kubexray does not grant more than the identity it runs as).
- Compromise of the workstation or the Kubernetes control plane itself.
