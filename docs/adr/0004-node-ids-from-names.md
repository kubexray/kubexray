# 0004. Node IDs from kind, namespace and name

- Status: Accepted
- Date: 2026-10-20
- Amends: [0003](0003-graph-delta-schema.md) (node `id` field)

## Context

ADR-0003 used `metadata.uid` as the node ID. While building the cluster watcher it became clear that Kubernetes objects mostly refer to each other **by name**, not UID:

| Reference | Has UID? |
|---|---|
| Pod → Node (`spec.nodeName`) | No |
| Pod → ConfigMap / Secret / PVC (volumes, env) | No |
| Ingress → Service (backends) | No |
| Object → owner (`ownerReferences`) | Yes |

With UID-based IDs, every name reference needs a name → UID lookup. When objects arrive out of order (a Pod seen before its Node or ConfigMap), it also needs a reverse index so the edge can be fixed up later. That's a lot of bookkeeping and a common source of bugs.

## Decision

A node's ID is built from its kind, namespace and name:

- Namespaced: `<Kind>/<namespace>/<name>`, e.g. `Pod/default/web-0`
- Cluster-scoped: `<Kind>/<name>`, e.g. `Node/worker-1`

Kubernetes names can't contain `/`, so IDs are unambiguous. Any reference can be turned into an ID directly. If the target doesn't exist yet, the graph already holds the edge back until it does (ADR-0003).

`parent` uses the same IDs. A parent may refer to a node that doesn't exist (yet); the UI treats such nodes as top-level.

## Consequences

- No lookup tables or reverse indexes in the watcher: references map straight to edges.
- An object that's deleted and recreated with the same name (for example a StatefulSet pod) keeps its ID. If both happen within one batch, clients see a single update rather than a remove and an add. The new status (Pending, `created` timestamp) still shows that it's a new object.
- Kinds are matched by name only. If two API groups ever define the same kind (for example custom resources), the ID will need the group; that's a schema change with its own ADR.
