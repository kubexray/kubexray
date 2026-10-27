# 0017. Values in diffs, kept inside the cluster

- Status: Accepted
- Date: 2026-10-27
- Amends: [0015](0015-diffs.md)

## Context

Diffs (ADR-0015) say which environment variable or ConfigMap key changed, never to what. "env LOG_LEVEL changed · values aren't recorded" still sends people to `kubectl` to find out what it is now, and it can't say what it was. Argo CD shows the values, behind a login, and people expect the same here.

kubexray is now behind a login too (OIDC, ADR-0013) and runs in the cluster. What ADR-0005 and ADR-0015 guarded against was values leaving the cluster in shared recordings and replays. A UI the team signs in to is not that.

## Decision

### 1. What is kept

With values on, the default, kubexray keeps:

- literal environment variable values
- each container's command and arguments, as one line
- ConfigMap contents, key by key, up to 4 KiB a value and 64 KiB a ConfigMap; binary data as a fingerprint

ConfigMaps in `kube-system`, `kube-public` and `kube-node-lease` are not kept. Secrets are still never read: kubexray watches them as metadata only.

Every value still has its fingerprint (ADR-0015), so with values off, or once values are stripped, diffs still say what changed.

### 2. Masked where it looks like a credential

Values are masked before they reach the graph, so nothing downstream can show them:

- **By name:** a variable or key whose name has `password`, `passwd`, `pwd`, `secret`, `token`, `api key`, `access key`, `private key`, `credential` or `auth` in it. Only its fingerprint is kept.
- **By contents:** credentials inside a value, such as `user:password@` in a URL, `--token=…`, `Bearer …` or a private key block, are replaced with `[redacted]`, and the rest is kept.
- **By size:** a longer value is cut, and says so.

Each masked value says why, and the diff shows it: "hidden: the name looks like a credential". Masking errs on hiding: a value someone wanted to see is a smaller problem than a credential on a screen.

### 3. Where values go

| Where | Values |
|---|---|
| The UI, behind the login | yes |
| The recording, in the cluster | yes |
| Replays | only when the person exporting ticks "Include configuration values" |
| Anonymized replays | never, and ConfigMap keys are renamed like other names |
| Slack, webhooks, the AI model | never: they're written from the cause, which names what changed, not values |

### 4. Turning it off

`--values=false`, or `diffs.values: false` in the Helm chart, keeps names and fingerprints only, as in ADR-0015. ConfigMaps are then watched as metadata only.

## Consequences

- "LOG_LEVEL info → verbose" is in kubexray itself, and a ConfigMap edit shows its diff, line by line for files.
- Anyone who can sign in to kubexray can read non-Secret configuration in every namespace kubexray watches. Until access follows Kubernetes permissions (SubjectAccessReview, ROADMAP v0.7), teams that need that boundary turn values off or limit who can sign in.
- Masking is a heuristic. A credential under an ordinary name with an ordinary shape (`FOO=abc123`) shows. Credentials belong in Secrets, which are never read.
- kubexray needs to read ConfigMaps in full, not only their metadata. Its ClusterRole already allows `list` on ConfigMaps, which RBAC can't limit to metadata.
- Recordings grow with ConfigMap contents, bounded by the caps above.
