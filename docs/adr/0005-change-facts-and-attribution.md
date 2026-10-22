# 0005. Change facts and attribution

- Status: Proposed
- Date: 2026-10-22
- Extends: [0003](0003-graph-delta-schema.md) (additive fields, schema version stays 1)

## Context

kubexray's headline feature is answering "what changed, and who changed it?" when something breaks ([roadmap](../../ROADMAP.md), v0.1). The graph model from ADR-0003 only describes *current health*: phase, readiness, restarts, replica counts. It can't tell that a Deployment's image changed, that a ConfigMap was edited, or by whom.

At the same time, the model is an allow-list, and that's our main security control. Anything we add must be safe to stream to a browser and to put in a shared replay file.

### What Kubernetes gives us

We checked on a real cluster (kind, Kubernetes 1.37) what each object records about its changes:

- **`metadata.managedFields`** lists, for each *field manager* (kubectl, Helm, Argo CD, a controller…), the time of its last write, the operation and the subresource. It is present even on the **metadata-only** view we use for ConfigMaps and Secrets, so we can see *that* and *when* a ConfigMap's data was edited, and *by which tool*, without reading its contents.
  - `kubectl patch configmap api-config` → manager `kubectl-patch`, `Update`, time of the edit.
  - `kubectl set image` and `kubectl rollout undo` → manager `kubectl`.
  - Controller bookkeeping (for example the Deployment controller updating `deployment.kubernetes.io/revision`) is written with **subresource `status`**, so it can be told apart from human changes.
  - `kubectl scale` writes through subresource `scale`, sometimes without a timestamp.
- Only the **latest** write per manager is kept. If kubectl changes an object twice, the first time is lost. kubexray watches continuously, so it sees each change as it happens and records the attribution at that moment.
- `metadata.generation` increases on every spec change (not on status changes).
- A few annotations identify where an object came from: `meta.helm.sh/release-name`, `argocd.argoproj.io/tracking-id`, and the Deployment revision `deployment.kubernetes.io/revision`.

## Decision

### 1. Desired state: `spec`

Workloads (Deployment, StatefulSet, DaemonSet, ReplicaSet, Job) and Pods get an optional `spec`:

```json
"spec": {
  "generation": 7,
  "revision": "14",
  "containers": [
    { "name": "api", "image": "nginx:1.27-alpine",
      "requests": { "cpu": "100m", "memory": "128Mi" },
      "limits":   { "memory": "256Mi" } }
  ]
}
```

- `containers` covers init containers too (marked `"init": true`). For workloads it comes from the pod template.
- `revision` is the Deployment revision annotation, so a rollback can be named ("revision 14 → 13").
- **Not included:** commands, args, environment variables (plain values or references), probes' commands, security contexts. These can contain secrets or are too detailed to be useful here.

### 2. Last change: `change`

Every node gets an optional `change` describing the most recent **non-status** write:

```json
"change": { "at": "2026-10-22T20:27:43Z", "by": "kubectl-patch", "tool": "kubectl", "via": "patch" }
```

- Chosen from `managedFields` entries whose subresource is not `status`, latest `time` first. If the latest entry has no timestamp (for example `scale`), the time we observed the update is used.
- Known controllers (`kube-controller-manager`, `kube-scheduler`, `kubelet`) are only used if no other manager has written the object, so a human or GitOps change isn't hidden by controller follow-up writes.
- `by` is the raw manager name (truncated to 64 characters). `tool` is a normalized family: `kubectl`, `helm`, `argocd`, `flux`, `controller`, or `other`. `via` is the kubectl subcommand when it can be inferred (`patch`, `apply`, `edit`, `scale`).

### 3. Origin: `origin`

Where an object is managed from, read from a fixed allow-list of labels and annotations:

```json
"origin": { "tool": "helm", "name": "shop" }
```

| Source | Read from |
|---|---|
| Helm | `meta.helm.sh/release-name` annotation (or `app.kubernetes.io/managed-by: Helm` label) |
| Argo CD | `argocd.argoproj.io/tracking-id` annotation, or the `argocd.argoproj.io/instance` label |
| Flux | `kustomize.toolkit.fluxcd.io/name` or `helm.toolkit.fluxcd.io/name` labels |

Git commits and Helm revision numbers aren't stored on the objects themselves. Reading them means watching Argo CD `Application` / Flux `Kustomization` resources (status only) or Helm release Secrets. Argo CD and Flux enrichment is a follow-up with its own ADR. **Helm release Secrets will not be read**, because they contain the full rendered manifests, including Secret values.

### 4. ConfigMaps and Secrets: contents changed, never contents

ConfigMaps and Secrets stay metadata-only. Their `change` shows when they were last written and by whom. A write that doesn't change labels or owners is reported as a contents change. We never record which keys changed: key names of Secrets can themselves be sensitive.

### 5. What is stored in memory

The informer transform (`stripObject`) changes from "drop all annotations and managedFields" to:

- annotations: keep only the allow-listed keys above; drop everything else, including `last-applied-configuration`;
- managedFields: keep only `manager`, `operation`, `subresource` and `time` of each entry; drop `fieldsV1` (the bulky field sets).

## Alternatives considered

| Option | Why not |
|---|---|
| Kubernetes Events (`kubectl get events`) for change history | Events are sparse, expire after an hour, and don't say who made a change |
| Audit logs | The best source of "who", but needs API server configuration and isn't available on most managed clusters; possible later as an optional input |
| Diffing full objects in the backend | Needs the full spec, including env values and commands, in memory; contradicts the allow-list |
| Reading Helm release Secrets for revisions | Contain rendered manifests with Secret values |

## Consequences

- The causality engine (next step) can say *"image nginx:1.27-alpine → nginx:1.72-alpine by kubectl"* and *"api-config edited by kubectl-patch 2 minutes before the first crash"* without reading any sensitive content.
- `change.at` updates on every non-status write, so nodes update a little more often. Status-only updates are unaffected.
- Attribution is only as good as the field manager name. Tools that use a generic client (`Go-http-client`) show up as `other`.
- Replay files gain these fields. The scrubbing rules for shared replays (v0.2) must cover `by`, `origin.name` and image names when anonymizing.
