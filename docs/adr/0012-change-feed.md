# 0012. Change feed: every change, what it did

- Status: Accepted
- Date: 2026-10-24

## Context

kubexray explains incidents, but most changes don't cause one, and people still want to see them: what was deployed this morning, who scaled what, which config was edited, whether yesterday's upgrade went well. Today that's scattered across the event log, timeline ticks and `kubexray changes`. The product's first promise is "see what changed" ([roadmap](../../ROADMAP.md), v0.5), so changes deserve a view of their own, with the one thing no plain audit log has: **what each change did**.

The approved design: a feed of change sets with a verdict on each, drift warnings, a "since you last looked" summary, an hourly activity strip, and a daily digest.

## Decision

### 1. Entries come from recorded history

The feed is built from the stream of deltas (ADR-0003), not from current state, so it knows before and after: "scaled 3 → 5", "image 1.27 → 1.29", "memory limit 64Mi → 16Mi". A tracker observes every delta as the hub emits it, and on start replays what the recorder holds, so a restarted in-cluster kubexray (ADR-0010) keeps its feed.

What becomes an entry:

| Object | Entries |
|---|---|
| Deployments, StatefulSets, DaemonSets | created, deleted, image, resources, probes, other template changes (revision), replicas |
| ConfigMaps, Secrets | created, deleted, edited (never contents) |
| Services, Ingresses, PVCs, ResourceQuotas, Namespaces, Jobs | created, deleted, changed |
| Nodes | joined, removed, not ready / ready again, cordoned / uncordoned |

Pods and ReplicaSets are not entries: they follow from the above, and their churn is noise. Writes by controllers are entries only for replicas (autoscalers) and nodes.

Authors come from managedFields (ADR-0005). Kubernetes records none for scales (ADR-0009), so a scale names its author only when a recorded write matches it.

### 2. Change sets

Entries by the same author, in the same namespace or release, within 30 seconds form one set: a Helm upgrade or an Argo CD sync is one line ("Helm upgrade shop 1.4.0 → 2.0.0, 3 objects"), its entries underneath. Versions come from the standard labels (`helm.sh/chart`, `app.kubernetes.io/version`) already in the model.

### 3. Verdicts

Each set that touches apps (directly, or through a ConfigMap, Secret or PVC they use) gets a verdict, updated as the cluster settles:

| Verdict | When |
|---|---|
| **Settling** | Its apps haven't finished rolling out yet (up to 10 minutes) |
| **Healthy** | All its apps were ready and rolled out afterwards, with the time it took |
| **Broke X** | The cause engine (ADR-0006) names one of its entries as the cause of an incident on X |
| **Fixed it** | It was made while one of its apps had an incident, which ended within 10 minutes |
| **No effect** | It touches no app (a node cordoned with room to spare, a new Service) |

### 4. Drift

A write by a person's tool (kubectl) to an object managed by Helm, Argo CD or Flux (`origin`, ADR-0005) is flagged: the next sync or upgrade will undo it, or the hand edit is the only copy of a fix.

### 5. Where it shows

`GET /api/feed` in the live UI, a `feed` message in replays (computed at export like incidents, ADR-0008), the Changes view, and a daily digest through the alert destinations (ADR-0011).

## Consequences

- People see every change and its outcome in one place, and deploy confidence becomes visible: changes that settled healthy, and how fast.
- The tracker keeps up to 10,000 entries (about a week of a busy cluster) in memory; older ones are dropped first.
- Replays gain a message type. Old players reject files that carry it, which is acceptable while the player and kubexray ship together.
