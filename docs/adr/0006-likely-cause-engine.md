# 0006. Likely-cause engine

- Status: Accepted
- Date: 2026-10-23

## Context

The flight recorder's promise is to answer, when something breaks: **what changed, and did it cause this?** ([roadmap](../../ROADMAP.md), v0.1 "Caused by"). The answer appears in three places: incident cards in the UI, `kubexray why <object>` in the terminal, and later post-mortems and grouped incidents (v0.2).

A wrong answer is worse than none: blame the wrong change once and people stop trusting the tool. So the engine has to be **explainable** (every conclusion backed by facts the user can check), **honest about uncertainty**, and **able to say "I don't know"**.

The inputs are the allow-listed graph model only (ADR-0003, ADR-0005): no logs, no Secret or ConfigMap contents, no metrics.

## Decision

### 1. Symptoms: what counts as broken

An **app** (Deployment, StatefulSet, DaemonSet, Job, or a standalone Pod) is unhealthy when any of these hold for longer than a grace period (default 30 s, so a normal rollout isn't an incident):

| Symptom | Detected from |
|---|---|
| Pods failing to start | `ImagePullBackOff`, `ErrImagePull`, `CreateContainerConfigError`, `Init:*` |
| Pods crashing | `CrashLoopBackOff`, `Error`, `OOMKilled`, restarts increasing |
| Pods not scheduled | `Pending` with the `PodScheduled` condition `False` |
| Not enough ready pods | ready < desired |
| Pods evicted | `Evicted` |

The **start** of an incident is the earliest moment its pods went bad. To know it without history, pods gain `status.since`: the last transition time of their `Ready` condition (an additive field under ADR-0003).

### 2. Candidates: what could have caused it

Within a lookback window (default 30 minutes before the start, plus 10 seconds after for clock skew), the engine collects:

| Candidate | Where it comes from without history | With the recorder |
|---|---|---|
| **Image change** of the app | The Deployment's old ReplicaSets, which Kubernetes keeps (`revisionHistoryLimit`, default 10): previous image, revision, creation time | Every change, for every kind |
| **Resource change** (limits, requests) | Same | Same |
| **Other spec change** | `spec.generation` increased, but nothing we model changed | Same |
| **Scale change** | Replica count history | Same |
| **Config change**: a ConfigMap or Secret the app uses was edited | `change.at` and `change.by` on that object (ADR-0005) | Every edit, not only the latest |
| **Missing dependency**: a referenced ConfigMap, Secret or PVC doesn't exist | Edges to absent nodes | Same, plus when it disappeared |
| **Node problem**: a node running the app's pods is NotReady or cordoned | Node status | When it happened |

Each candidate records **who** made the change (`change.by`, `change.tool`, `origin`) when known.

### 3. Ranking: fixed, explainable rules

No statistics or machine learning. Each candidate gets a **confidence** from whether its *mechanism* matches the symptom:

| Confidence | When | Example |
|---|---|---|
| **Very likely** | The change can directly produce exactly this symptom | Image changed → `ImagePullBackOff` on the new image · memory limit lowered → `OOMKilled` · referenced Secret missing → `CreateContainerConfigError` · node NotReady → its pods failing |
| **Likely** | Plausible mechanism, plus the **before/after test**: pods created *after* the change fail while pods created *before* it are healthy | ConfigMap edited → only pods started since the edit crash-loop |
| **Possible** | A change to the app or what it uses happened in the window, without a matching mechanism | Image changed, pods `CrashLoopBackOff`, no before/after evidence |

Ties are broken by recency: the change closest to the start comes first. Changes made *after* the incident started are never causes. They're reported as possible **fixes** if the app recovered after them.

The before/after test is the strongest evidence available without logs, and it covers the most common "nobody would connect these" case: a config edit that only bites when pods restart minutes later.

### 4. Output: a cause with evidence, alternatives, and a next step

```text
api is down (0/3 ready) since 14:02:11, 4 minutes ago
  impact   Ingress web → Service api → 0 of 3 pods ready
  symptom  3 pods CrashLoopBackOff
  likely cause   api-config was edited at 13:59:40 by kubectl-patch
  evidence       3 pods started after the edit are crash-looping
                 2 pods started before it are healthy
                 no other changes to api or what it uses in the last 30 minutes
  also changed   (nothing else)
  next           kubectl -n shop logs api-6d9f-x2k --previous
                 kubectl -n shop rollout restart deploy/api   (after fixing api-config)
```

- Every **evidence** line is a fact from the model that the user can verify.
- **Also changed** lists the other candidates, so a wrong top pick is easy to spot.
- When nothing qualifies, the engine says so: *"No change to api or what it uses in the last 30 minutes. The cause may be outside the cluster: a dependency, the network, or traffic."*
- **Next** steps are read-only commands, except a clearly labelled fix (rollback, restart) that the user runs themselves. kubexray never runs them.

### 5. One engine, three front ends

The engine is a library (`internal/cause`) with no I/O: it takes a graph state and, when available, recorded history, and returns incidents with causes as data (JSON-serializable). The UI's incident cards, `kubexray why` and `kubexray changes`, and later post-mortems all render the same result.

`kubexray why <kind>/<name>` connects, syncs (a few seconds), runs the engine on the current state and exits. No running server or prior recording is needed; a running kubexray adds history.

## Alternatives considered

| Option | Why not |
|---|---|
| Statistical correlation or ML across changes and failures | Can't explain itself; needs data we don't have; unpredictable on small clusters |
| Reading container logs | Needs `pods/log` permission and can contain secrets; considered later as opt-in evidence ("log says: invalid LOG_LEVEL") |
| Kubernetes Events as the main input | Expire after an hour, and don't say who made a change; may be added later as extra evidence |
| Only "most recent change" without confidence | Simple, but wrong whenever an unrelated change happened last, which is exactly when users most need the right answer |

## Consequences

- Answers are limited to what the model can see. Incidents caused outside the cluster get an honest "no change found", not a guess.
- Without history, only Deployments have previous images (from old ReplicaSets). StatefulSet and DaemonSet image history needs the recorder, or reading `ControllerRevisions` later.
- Rules are fixed and tested against the `make break` scenarios. Each new rule needs a scenario that proves it.
- Pods gain `status.since`; the UI and replay files get it too (additive, schema version stays 1).
