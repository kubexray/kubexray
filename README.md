# kubexray

[![CI](https://github.com/kubexray/kubexray/actions/workflows/ci.yml/badge.svg)](https://github.com/kubexray/kubexray/actions/workflows/ci.yml)
[![CodeQL](https://github.com/kubexray/kubexray/actions/workflows/codeql.yml/badge.svg)](https://github.com/kubexray/kubexray/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/kubexray/kubexray/badge)](https://scorecard.dev/viewer/?uri=github.com/kubexray/kubexray)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**The flight recorder for your Kubernetes cluster.**

See what changed. See what broke. Share what happened.

> **Status:** early development. The cluster recorder works today (`kubexray --dump`); the UI, rewind and incident detection are in progress. See the [roadmap](ROADMAP.md).

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

## Design goals

- **Free, open source, local.** A single binary that uses your kubeconfig. Nothing to install in the cluster to get started.
- **Read-only and private.** kubexray only lists and watches. Secret and ConfigMap contents are never read, and replays are scrubbed.
- **Explains, doesn't just display.** Problems come with a plain-English explanation and the command to run next.

## Try it

Requires Go and access to a cluster. Make sure your kubeconfig points where you expect; kubexray logs the context and API server before connecting.

```sh
make build
./bin/kubexray --dump --context <your-context>   # streams the cluster graph as JSON lines
```

## Documentation

- [Roadmap](ROADMAP.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md) and [threat model](docs/threat-model.md)
- [Architecture decisions](docs/adr/)
