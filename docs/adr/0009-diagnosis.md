# 0009. Diagnosis: explaining the symptom itself

- Status: Accepted
- Date: 2026-10-23

## Context

The cause engine (ADR-0006) answers **what changed**. Many incidents need a different answer first: **what is Kubernetes actually saying is wrong?** A pod that is `Pending` could be waiting for memory, for a node without a taint, or for a volume. A pod that is `OOMKilled` hit a limit someone can name. A Deployment with no pods at all may have been refused by a quota or an admission webhook. Today the card says "3 pods Pending" and stops.

Kubernetes already knows these answers and writes them down: the scheduler on the pod's `PodScheduled` condition, the kubelet on the pod (`status.message` for evictions) and on each container's waiting state, the ReplicaSet controller on the Deployment's `ReplicaFailure` condition. Exit codes and probe definitions add the rest. None of this is in the graph model yet, which is an allow-list (ADR-0003): a field only leaves the backend if it is declared.

Two constraints shape what we may add:

- **Nothing an application writes.** A container's termination message and its logs are written by the app and can contain anything, including secrets. They stay out, as Secret and ConfigMap contents do.
- **Replays stay safe to share** (ADR-0008). Anything free-form must be removable by anonymization.

## Decision

### 1. Model additions (additive under ADR-0003)

| Field | On | Source | Notes |
|---|---|---|---|
| `status.message` | Pod | `PodScheduled` condition message (scheduler), else `status.message` (kubelet: eviction, shutdown), else the waiting message of the container that sets the reason (kubelet: `CreateContainerConfigError`, image pulls) | Capped at 300 characters |
| `status.message` | Deployment | The `ReplicaFailure` condition message (quota, admission, pod security) | Capped at 300 characters |
| `status.exit` | Pod | Exit code of the termination behind the reason (current, else last) | 0 is omitted |
| `status.container` | Pod | The container the reason, message and exit code come from | |
| `spec.containers[].probes` | Pods and templates | Whether liveness, readiness and startup probes are defined | Not their commands, paths or ports |

Never added: `terminated.message` (the app's termination message), probe handlers, events, logs.

Anonymized replays drop `status.message` entirely (it names nodes, volumes and keys) and anonymize `status.container` like container names.

### 2. Diagnosis

Each incident gains an optional **diagnosis**: one plain-English sentence on what is wrong, with the facts it is based on as evidence. It's separate from causes. A diagnosis says *what Kubernetes reports*; a cause says *what changed*. Both can be present.

| Symptom | Diagnosis | Evidence |
|---|---|---|
| `OOMKilled` (now or last restart) | The container ran out of memory at its limit, or with no limit set | Container, exit code, memory limit and request, restarts |
| Crashing (`CrashLoopBackOff`, `Error`) | The container keeps exiting, with what the exit code usually means (1, 126, 127, 137, 139, 143); 137 or 143 with a liveness probe may be the probe killing it | Container, exit code, restarts, probes |
| Running but not ready | Failing its readiness probe, so Services send it no traffic | Probe defined, containers running |
| `Unschedulable` | The scheduler's message, translated: not enough CPU or memory for the requests, an untolerated taint, node affinity or selector, an unbound volume | The scheduler's message verbatim, requests |
| A volume the app uses is `Pending` | The volume isn't bound, so the pods can't start | Volume name and phase |
| `Evicted` | The kubelet evicted the pods, with its message (usually node pressure) | The kubelet's message |
| `CreateContainerConfigError`, image pulls | The kubelet's message: the missing key, or why the pull failed | The kubelet's message |
| No pods, `ReplicaFailure` | Kubernetes refused to create the pods: quota, admission or pod security | The controller's message |

"Limit vs. usage" for memory: kubexray has no metrics, so the diagnosis states the limit and suggests `kubectl top` (metrics-server) to see usage. It never guesses usage.

### 3. Where it shows

The incident card and the selection panel, `kubexray why` (a `diagnosis` row and its evidence), post-mortem drafts, and replays (computed at export like causes).

## Consequences

- Pending, OOM and probe incidents get a specific answer instead of a pod count, from facts users can check with `kubectl describe`.
- The model carries a little free-form text for the first time. It comes only from Kubernetes components, is capped, and anonymization removes it.
- Translating scheduler messages relies on their wording, which Kubernetes doesn't guarantee. The verbatim message is always kept as evidence, so a missed translation degrades to "the scheduler says: …", not to a wrong answer.
- Wire format version stays 1: all fields are additive and optional.
