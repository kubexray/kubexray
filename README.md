# kubexray

[![CI](https://github.com/kubexray/kubexray/actions/workflows/ci.yml/badge.svg)](https://github.com/kubexray/kubexray/actions/workflows/ci.yml)
[![CodeQL](https://github.com/kubexray/kubexray/actions/workflows/codeql.yml/badge.svg)](https://github.com/kubexray/kubexray/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/kubexray/kubexray/badge)](https://scorecard.dev/viewer/?uri=github.com/kubexray/kubexray)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**The flight recorder for your Kubernetes cluster.**

See what changed. See what broke. Share what happened.

> **Status:** early development. The live map, incident detection with grouped causes, rewind, `kubexray why`, shareable replays and post-mortem drafts all work. See the [roadmap](ROADMAP.md).
>
> **[Watch a recorded incident in your browser →](https://kubexray.github.io/kubexray/)** No install, no cluster.

## Why

Dashboards tell you a pod is crashing. During an incident you need to know more:

- **What changed?** kubexray records every rollout, image change, scale and config edit, and lets you rewind the cluster to any moment.
- **What did it break?** When something goes red, kubexray traces it back to the change that caused it:

  ```
  Ingress web → errors
    └─ Service api has 0 ready pods
         └─ 3 pods ImagePullBackOff
              └─ caused by: rollout of api at 14:02:11
                   image  nginx:1.27  →  nginx:1.72
  ```

- **Can I show my team?** Export the incident as a replay. Anyone can watch it in the [kubexray player](https://kubexray.github.io/kubexray/), without access to your cluster, and kubexray drafts the post-mortem for you.

## Performance

Measured on a simulated cluster of 1,000 pods in 100 apps (`make bench`, `make perf`):

| | |
|---|---|
| Finding every incident and its likely cause | 1 ms |
| Applying a batch of 50 pod changes (server / browser) | 0.02 ms / under 1 ms |
| From opening the page to the full map rendered in Chrome | about 80 ms |
| Memory and CPU, kubexray plus the simulated API server, with a pod changing every 100 ms | 77 MB, 4 % of a core |

CI enforces budgets for the browser numbers.

## Design goals

- **Free, open source, local.** A single binary that uses your kubeconfig. Nothing to install in the cluster to get started.
- **Read-only and private.** kubexray only lists and watches. Secret and ConfigMap contents are never read, and replays are scrubbed.
- **Explains, doesn't just display.** Problems come with a plain-English explanation and the command to run next.

## Try the demo

Breaks a small shop app on a local [kind](https://kind.sigs.k8s.io) cluster and lets kubexray explain it. Needs Go, Node, Docker, kind and kubectl. The demo uses its own kubeconfig and doesn't touch your kubectl setup.

```sh
make demo        # create the cluster, deploy the shop app, open kubexray
make break       # in another terminal: edit a ConfigMap; a minute later a pod restarts and crash-loops
make fix         # put it back
make demo-down   # delete the cluster
```

Other ways to break it, each with the answer kubexray should give:

| `SCENARIO=` | What happens | kubexray says |
|---|---|---|
| `bad-image` | api ships an image tag that doesn't exist | can't pull it; very likely the image change |
| `node` | a worker node stops | one incident for every app on it; the node isn't ready |
| `oom` | reports' memory limit is cut to 16Mi | it runs out of memory at its limit; very likely the resources change |
| `bad-probe` | api gets a readiness probe on a path nginx doesn't serve | running but failing its readiness probe; very likely the probe added |
| `unschedulable` | checkout's memory request is raised to 64Gi | the scheduler can't place it: nodes have too little memory; very likely the resources change |
| `bad-config` | a key api reads is renamed in its Secret | can't create the container: the key is missing; very likely the Secret edit |
| `scale-zero` | api is scaled to 0 while the web Ingress routes to it | scaled to zero with traffic still routed to it; very likely the scale |
| `quota` | a 64Mi memory quota is added to the namespace, then api restarts | Kubernetes refuses its new pods; very likely the new ResourceQuota |

`make e2e` runs every scenario and checks kubexray's answers; CI does the same on each change to the engine.

## Use it on your cluster

Requires Go, Node and access to a cluster. kubexray uses your kubeconfig like kubectl does, prints which cluster it's reading before it connects, and only reads.

```sh
make build
./bin/kubexray --context <name>              # opens the live map in your browser
./bin/kubexray why deploy/checkout -n shop   # or ask in the terminal
```

```text
checkout (Deployment, shop) is degraded: 2 of 3 pods ready, since 00:10:47 (50s ago)
  impact            Service checkout → 2 of 3 pods ready
  symptom           1 pod CrashLoopBackOff
  likely cause      ConfigMap checkout-config was edited by kubectl patch at 00:09:47 (1m ago)
  evidence          1 pod started after the edit is failing
                    2 pods started before it are healthy
                    no other changes to checkout or what it uses in the 30 minutes before
  next              kubectl -n shop logs checkout-5d6d48b89-4cpfx --previous   # why the last attempt failed
                    kubectl -n shop rollout restart deployment/checkout        # after fixing checkout-config
```

## Record around the clock in your cluster

Run kubexray in the cluster so the incident from 3 a.m. is still there in the morning ([ADR-0010](docs/adr/0010-in-cluster-mode.md)):

```sh
helm install kubexray charts/kubexray -n kubexray --create-namespace
```

It records to a volume (7 days or 1 GiB by default, whichever comes first), keeps its history across restarts, and has read-only access to the kinds it maps. There's no anonymous mode: every request needs the token from the `kubexray` Secret (the install notes print how to open it), or you put an auth proxy such as oauth2-proxy in front with `auth.mode: proxy`. Secrets aren't watched unless you set `rbac.secrets: true`, because Kubernetes can't grant "list Secrets" without their contents.

The images aren't published yet; until the first release, build one with `make docker`.

**Alerts with the cause attached.** Add kubexray as an Alertmanager receiver and give it a Slack incoming webhook (`alerts.slack.existingSecret`) or any webhook (`alerts.webhook.existingSecret`). When an alert about an app fires, kubexray sends the app's state, what Kubernetes says is wrong, the likely cause and who made the change, with a link that opens kubexray at the moment it started ([ADR-0011](docs/adr/0011-alerts.md)):

```text
KubePodCrashLooping in shop: Pod is crash looping
checkout is degraded: 1 pod CrashLoopBackOff
What's wrong: The container checkout keeps exiting with code 1
Likely cause: ConfigMap checkout-config was edited by kubectl patch at 13:59:00 UTC
Open in kubexray at the moment it started
```

## Every change, and what it did

The **Changes** view lists every change to the cluster, newest first, and what each one did once things settled ([ADR-0012](docs/adr/0012-change-feed.md)):

```text
5:17 PM  ConfigMap checkout-config edited     by kubectl patch       Fixed it     checkout healthy 12s later
5:15 PM  ConfigMap checkout-config edited     by kubectl patch       Broke checkout   degraded 45s later · over at 5:18 PM
5:15 PM  api scaled 3 → 4 replicas            by kubectl scale       Healthy      settled in 1m 0s
```

A Helm upgrade or Argo CD sync is one line with its version ("Helm upgrade shop 1.4.0 → 2.0.0"). Hand edits to objects Helm or a GitOps tool manages are flagged as **drift**. When you come back, it tells you what happened **since you last looked**. Filter by what broke, drift or what's still settling, search by app, person or image, and open a change to rewind the map to just before it.

## Compare two moments

Press **Compare** on the timeline, then move to another moment: the side panel lists what changed in between. You see what was added or removed, new images, resources and probes, revisions, replicas and readiness, pods replaced or failing, and who edited a ConfigMap or Secret (never its contents). It works live, rewound and in replays.

## Share an incident

On the incident card, **Share replay** downloads the incident as a `.kxr` file: a few minutes of what kubexray recorded, with the incidents and causes it found. Names, labels and images are anonymized by default; Secret and ConfigMap contents, environment values and annotations are never in a replay ([ADR-0008](docs/adr/0008-replays-and-sharing.md)).

Anyone can play it:

- drop it on the [kubexray player](https://kubexray.github.io/kubexray/), which runs entirely in the browser and uploads nothing, or
- run `./bin/kubexray play incident.kxr`.

The player's timeline plays and scrubs the incident, and **Copy link to this moment** shares a link that opens on the same moment and app, for replays hosted at an https address.

**Post-mortem draft** turns the incident into Markdown to paste into a doc or an issue: status, duration, impact, the likely cause and who made the change, a timeline, the suggested fix commands and the replay link, with TODOs for what kubexray can't see.

Other commands:

```sh
./bin/kubexray changes -n shop --since 1h   # recent changes and who made them
./bin/kubexray dump                         # the live cluster graph as JSON lines
```

## Documentation

- [Roadmap](ROADMAP.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md) and [threat model](docs/threat-model.md)
- [Architecture decisions](docs/adr/)
- [Releasing](docs/releasing.md): how releases are built, signed and verified
