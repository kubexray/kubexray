# 0018. A manifest history for every kind

- Status: Accepted
- Date: 2026-10-27
- Extends: [0015](0015-diffs.md), [0017](0017-values-in-diffs.md)

## Context

Diffs (ADR-0015, ADR-0017) cover workloads' pod templates and ConfigMaps. Plenty of incidents start elsewhere: an HPA's `maxReplicas` lowered before a traffic peak, a NetworkPolicy that drops a port, a Role that loses a verb, a PodDisruptionBudget that blocks a drain, a CronJob's schedule. kubexray showed those as "changed" or not at all. Argo CD diffs any kind, including custom resources.

Writing a mapper for each kind doesn't scale, and custom resources have no types kubexray could know.

## Decision

### 1. One generic history

For kinds kubexray doesn't map itself, it watches objects through the dynamic client and keeps a **manifest**: the object's desired state, flattened to one value per field path.

```
spec.maxReplicas: 10
spec.scaleTargetRef.name: checkout
spec.ingress[0].ports[0].port: 8080
spec.template.spec.containers[api].env[LOG_LEVEL].value: info
metadata.labels["app.kubernetes.io/name"]: checkout
```

- `status`, and all metadata except labels, are left out. They change without anyone changing the object.
- Lists whose items all have a distinct `name` (containers, env, ports) are keyed by it, so inserting an item changes one line, not every line after it.
- Each field is a value like any other (ADR-0017): a fingerprint, plus its text when values are on. It is masked when the field name, or the name of any key or item on its path, looks like a credential (`env[DB_PASSWORD].value`), or when its contents do. The caps are the same: 4 KiB a value, 64 KiB an object, and at most 512 fields.

Ingresses and Services, which kubexray maps itself, get a manifest too.

### 2. Which kinds

By default:

- HorizontalPodAutoscalers and PodDisruptionBudgets
- NetworkPolicies
- CronJobs
- Roles, RoleBindings, ClusterRoles and ClusterRoleBindings
- StorageClasses and LimitRanges

More through `--resource group/version/resource`, or `diffs.extraResources` in the Helm chart, which also grants read access to each. Secrets are refused.

Before watching a resource, kubexray lists one object. If it may not list the resource, or the cluster doesn't have it, the resource is skipped and logged, and kubexray starts anyway.

Objects in system namespaces, and the cluster's own RBAC (names starting `system:`), are watched without a manifest: they change with every upgrade, and nobody's change is in them.

### 3. In the feed and the UI

A change to a kind with a manifest reads as what changed, like any other diff: "HorizontalPodAutoscaler checkout changed (spec.maxReplicas 10 → 3)". **See diff** shows each field. The Compare panel lists field changes too, and the details panel shows the manifest, and ConfigMap contents, as YAML rebuilt from the fields.

Writes by controllers are left out of the feed, as before (ADR-0012).

### 4. Replays

A manifest leaves a replay only when values are included (ADR-0017). Anonymized replays drop manifests entirely, because their paths can hold names (label keys, list item names) that the anonymizer can't tell apart from structure.

## Consequences

- "What changed?" has an answer for any kind kubexray may read, custom resources included, without code for each.
- Changes to these kinds are in the feed and the Changes list. Linking them to incidents, such as an HPA limit to an app that can't scale, is cause-engine work for later.
- Field paths are less friendly than a typed mapper's words ("spec.maxReplicas" rather than "max replicas"). In exchange, they're exact, and they're what `kubectl explain` and the YAML use.
- Memory grows with the number of these objects. Status is dropped as objects arrive, before they're cached, and the caps bound each object.
- The chart's ClusterRole reads more kinds, including RBAC objects. Those hold no credentials.
