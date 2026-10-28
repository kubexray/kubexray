# 0020. Values follow Kubernetes permissions

- Status: Accepted
- Date: 2026-10-28
- Extends: [0013](0013-sign-in.md), [0017](0017-values-in-diffs.md)

## Context

With values on (ADR-0017), everyone who can sign in to kubexray sees ConfigMap and environment values in every namespace it watches. In a cluster shared by teams, someone who may only read their own namespace with `kubectl` could read everyone's configuration in kubexray. Argo CD solves this with its own RBAC; most teams already have the answer in Kubernetes RBAC.

## Decision

### 1. The same question kubectl would answer

With `--values-access=kubernetes` (`diffs.access: kubernetes` in the chart), kubexray asks Kubernetes before it shows a person a namespace's values: may this person `get configmaps` in that namespace? It asks with a SubjectAccessReview, which reads nothing and changes nothing. kubexray needs permission to create them, and the chart grants it.

ConfigMaps stand for all values: a Deployment's environment values and an HPA's manifest in a namespace are shown to the people who may read its ConfigMaps. Objects without a namespace are asked about cluster-wide.

### 2. Who the person is to Kubernetes

Only OIDC sign-in says who someone is, so the setting needs `auth.mode: oidc`. It works when the cluster's API server trusts the same OIDC provider, so people are the same users and groups to both:

- The username is the claim the API server uses (`--oidc-username-claim`, set with `auth.oidc.usernameClaim`, default `email`), with its prefix (`--oidc-username-prefix`, set with `diffs.kubeUsernamePrefix`).
- Groups come from the groups claim, with the groups prefix (`diffs.kubeGroupsPrefix`). The session keeps up to 1.5 KB of groups, because it lives in a cookie. Groups that don't fit are dropped and logged: fewer groups can only hide more.

On EKS with IAM access entries and no OIDC provider, people aren't Kubernetes users, so this setting doesn't apply there.

### 3. Hidden everywhere they'd show

For each request, the namespaces a person may not read lose their values:

- the live stream (each connection's copy of each message)
- moments rewound to
- the change feed: diff lines carrying values, and the summaries and titles that quoted them ("LOG_LEVEL changed")
- incident cards, which show values from the feed
- exports with values included

Images, resources and names are not values (ADR-0015), and stay. Answers are cached for a minute, so a changed RoleBinding takes effect within it. Any error asking counts as no. Requests with the token (Alertmanager, scripts) see no values.

## Consequences

- Values follow the permissions teams already manage, with nothing new to configure in kubexray beyond turning it on.
- Each person costs one SubjectAccessReview per namespace per minute at most.
- The map, incidents and change history stay visible to everyone signed in. This decides values, not which objects exist.
