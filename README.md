# kubexray

[![CI](https://github.com/kubexray/kubexray/actions/workflows/ci.yml/badge.svg)](https://github.com/kubexray/kubexray/actions/workflows/ci.yml)
[![CodeQL](https://github.com/kubexray/kubexray/actions/workflows/codeql.yml/badge.svg)](https://github.com/kubexray/kubexray/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/kubexray/kubexray/badge)](https://scorecard.dev/viewer/?uri=github.com/kubexray/kubexray)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**The flight recorder for your Kubernetes cluster.**

See what changed. See what broke. Share what happened.

> **Status:** early development, heading for v0.1. The live map, incident detection, rewind and `kubexray why` all work. See the [roadmap](ROADMAP.md).

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

- **Can I show my team?** Export the incident as a replay. Anyone can watch it at kubexray.com, without access to your cluster.

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

`SCENARIO=bad-image make break` ships an image tag that doesn't exist instead.

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
