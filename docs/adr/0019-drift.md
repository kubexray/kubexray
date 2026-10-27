# 0019. Drift: against what the tool applied, with a way back

- Status: Accepted
- Date: 2026-10-27
- Extends: [0012](0012-change-feed.md), [0018](0018-manifest-history.md)

## Context

The feed flagged a `kubectl` edit to an object Helm, Argo CD or Flux manages with one sentence: it's managed, "the next helm upgrade puts it back". It didn't say how the object now differs from what the tool applied, and the sentence was wrong. Helm 3 and 4 merge: a rollback or upgrade restores fields the chart sets, but leaves list items and fields added by hand. On a Service whose port was edited, it even fails, because the hand-set port and the chart's port end up as two ports without names.

Helm keeps what it applied in a Secret per revision. kubexray never reads Secrets.

## Decision

### 1. What the tool applied, from kubexray's own record

When an object's latest write is by the tool that manages it, kubexray keeps that version as **applied**: its pod template spec, ConfigMap contents and manifest (ADR-0017, ADR-0018). When it starts, an object whose latest write is by its tool counts as applied as first seen.

A write by anyone else is **drift**: `kubectl`, Lens, a script, even Helm editing an Argo CD object. Controllers don't count. A drift entry carries the diff from applied to now, which builds up across several hand edits. Without an applied version (kubexray started after the hand edit), the entry says it can't show the difference.

### 2. A way back that works

- **Helm:** the object is replaced with the one in the release's manifest, at the latest revision Helm deployed:

  ```
  helm get manifest R -n NS --revision N
    | kubectl create -n NS --dry-run=client -o json -f -
    | jq 'select(.kind == "K" and .metadata.name == "NAME") | .metadata.annotations += {…Helm's ownership…}'
    | kubectl replace -n NS -f -
  ```

  - The revision comes from the labels on Helm's Secrets (`status`, `version`), which kubexray reads as metadata. Failed revisions are skipped.
  - The namespace is passed at every step. Helm's manifest has none, and kubectl would otherwise use the kubeconfig's.
  - Helm's ownership annotations are added back. They aren't in the manifest, and without them the next upgrade refuses the object.
  - Only the drifted object is replaced. Replacing the whole release would also reset fields controllers own, such as replicas under an HPA.

  It needs `jq`, which macOS and most Linux distributions include.
- **Argo CD:** `argocd app sync APP`. **Flux:** `flux reconcile kustomization|helmrelease NAME --with-source`. Both re-apply from Git, and the UI says that fields added by hand may stay.

### 3. In the UI

The drift note says the edit isn't in the chart or Git, and that the next upgrade or sync won't fully undo it. Below it: the diff "against what Helm applied", and the command with a Copy button.

## Consequences

- People see exactly what a hand edit changed relative to the source of truth, and can put it back with one command, or carry the change into the chart.
- The applied version is kubexray's recording, not Helm's record. After a restart, objects already drifted have no applied version until their tool writes them again.
- `kubectl replace` writes the whole object. A field that only a controller sets, and that the chart leaves out, goes back to its default until the controller sets it again.
- Checked against Helm 4 on the demo cluster: a hand-edited Service port and an added environment variable go back, the release's next upgrade succeeds, and nothing lands in another namespace.
